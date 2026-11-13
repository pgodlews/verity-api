// vite.bridge.ts lets `npm run dev` work in an ordinary browser, outside the
// Wails shell. It serves the methods app.go binds at POST /__verity/<Method>
// by running `verity gui-call <Method>` with the JSON arguments on stdin.
// That command calls the same core/workspace functions as the desktop app,
// so the browser sees real, already-redacted data and the same behaviour.
//
// Dev-only: the plugin is applied by `vite serve`, never included in builds.
// The CLI binary is $VERITY_BIN, else ../../verity, else `verity` on PATH.
// It must be built with the dev bridge, which release builds leave out:
// go build -tags devbridge -o verity ./cmd/verity (at the repo root).

import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { resolve } from "node:path";
import type { Plugin } from "vite";

const repoBin = resolve(__dirname, "../../verity");
const bin = process.env.VERITY_BIN || (existsSync(repoBin) ? repoBin : "verity");

function guiCall(method: string, args: string): Promise<string> {
  return new Promise((ok) => {
    const child = spawn(bin, ["gui-call", method], { stdio: ["pipe", "pipe", "pipe"] });
    let out = "";
    let err = "";
    child.stdout.on("data", (c) => (out += c));
    child.stderr.on("data", (c) => (err += c));
    child.on("error", (e) => ok(JSON.stringify({ ok: false, error: `cannot run ${bin}: ${e.message}` })));
    child.on("close", () =>
      ok(out.trim() ? out : JSON.stringify({ ok: false, error: err.trim() || `${method} produced no output` }))
    );
    child.stdin.end(args || "[]");
  });
}

type Headers = Record<string, string | string[] | undefined>;

const loopbackHosts = new Set(["localhost", "127.0.0.1", "[::1]"]);

function single(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value[0] : value;
}

/**
 * bridgeForbidden returns why a bridge request must be refused, or null when
 * it is allowed. The bridge runs real commands (save, commit, push, run), so
 * a page on another site must not reach it: the Host must be a loopback name
 * (no DNS rebinding), the request must carry X-Verity-Bridge: 1 (a custom
 * header, so a cross-origin browser request cannot be a "simple" no-preflight
 * POST), and an Origin or Sec-Fetch-Site header, when present, must name this
 * dev server itself.
 */
export function bridgeForbidden(headers: Headers): string | null {
  const host = single(headers.host);
  if (!host) return "missing Host header";
  let self: URL;
  try {
    self = new URL(`http://${host}`);
  } catch {
    return "invalid Host header";
  }
  if (!loopbackHosts.has(self.hostname)) return "Host is not a loopback name";
  if (single(headers["x-verity-bridge"]) !== "1") return "missing X-Verity-Bridge header";
  const origin = single(headers.origin);
  if (origin !== undefined) {
    let from: URL;
    try {
      from = new URL(origin);
    } catch {
      return "invalid Origin header";
    }
    if (from.host !== self.host || !/^https?:$/.test(from.protocol)) return "cross-origin request";
  }
  const site = single(headers["sec-fetch-site"]);
  if (site !== undefined && site !== "same-origin") return "cross-site request";
  return null;
}

export function verityBridge(): Plugin {
  return {
    name: "verity-dev-bridge",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use("/__verity/", (req, res) => {
        const method = (req.url ?? "").replace(/^\//, "").split("?")[0];
        if (!/^[A-Za-z]+$/.test(method) || req.method !== "POST") {
          res.statusCode = 404;
          res.end();
          return;
        }
        const reason = bridgeForbidden(req.headers);
        if (reason) {
          res.statusCode = 403;
          res.setHeader("Content-Type", "application/json");
          res.end(JSON.stringify({ ok: false, error: `dev bridge refused: ${reason}` }));
          return;
        }
        let body = "";
        req.on("data", (c) => (body += c));
        req.on("end", async () => {
          res.setHeader("Content-Type", "application/json");
          res.end(await guiCall(method, body));
        });
      });
    },
  };
}
