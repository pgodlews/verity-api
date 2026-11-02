// vite.bridge.ts lets `npm run dev` work in an ordinary browser, outside the
// Wails shell. It serves the methods app.go binds at POST /__verity/<Method>
// by running `verity gui-call <Method>` with the JSON arguments on stdin.
// That command calls the same core/workspace functions as the desktop app,
// so the browser sees real, already-redacted data and the same behaviour.
//
// Dev-only: the plugin is applied by `vite serve`, never included in builds.
// The CLI binary is $VERITY_BIN, else ../../verity (go build -o verity
// ./cmd/verity at the repo root), else `verity` on PATH.

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
