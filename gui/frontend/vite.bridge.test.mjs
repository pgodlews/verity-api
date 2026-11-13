// Run with: node --test vite.bridge.test.mjs
// Mounts the dev-bridge middleware on a fake server and checks that requests
// other pages could send are refused before any CLI process starts.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import ts from "typescript";

const directory = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

function loadBridge(spawned) {
  const source = readFileSync(join(directory, "vite.bridge.ts"), "utf8");
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 },
  });
  const module = { exports: {} };
  const localRequire = (id) =>
    id === "node:child_process"
      ? { spawn: (...args) => { spawned.push(args); throw new Error("spawn not allowed in this test"); } }
      : require(id);
  new Function("module", "exports", "require", "__dirname", outputText)(module, module.exports, localRequire, directory);
  return module.exports;
}

function mount(bridge) {
  let handler;
  bridge.verityBridge().configureServer({ middlewares: { use: (_path, fn) => { handler = fn; } } });
  return handler;
}

function call(handler, { url = "/RunProject", method = "POST", headers = {} }) {
  const res = { statusCode: 200, headers: {}, body: undefined, setHeader(k, v) { this.headers[k] = v; }, end(b) { this.body = b; } };
  const req = { url, method, headers, on() {} };
  handler(req, res);
  return res;
}

const good = { host: "localhost:5173", origin: "http://localhost:5173", "x-verity-bridge": "1", "sec-fetch-site": "same-origin" };

test("bridge refuses requests without the header, from other origins, or for non-loopback hosts", () => {
  const spawned = [];
  const handler = mount(loadBridge(spawned));
  const refused = {
    "missing header": { ...good, "x-verity-bridge": undefined },
    "wrong header value": { ...good, "x-verity-bridge": "yes" },
    "foreign origin": { ...good, origin: "https://evil.example" },
    "other localhost port": { ...good, origin: "http://localhost:8080" },
    "null origin": { ...good, origin: "null" },
    "cross-site fetch": { ...good, "sec-fetch-site": "cross-site" },
    "rebinding host": { ...good, host: "evil.example:5173", origin: "http://evil.example:5173" },
    "missing host": { ...good, host: undefined },
  };
  for (const [name, headers] of Object.entries(refused)) {
    const res = call(handler, { headers });
    assert.equal(res.statusCode, 403, name);
    assert.equal(JSON.parse(res.body).ok, false, name);
  }
  assert.equal(spawned.length, 0, "no CLI process may start for a refused request");
});

test("bridge allows same-origin requests that carry the header", () => {
  const { bridgeForbidden } = loadBridge([]);
  assert.equal(bridgeForbidden(good), null);
  assert.equal(bridgeForbidden({ host: "127.0.0.1:5173", "x-verity-bridge": "1" }), null);
  assert.equal(bridgeForbidden({ host: "[::1]:5173", origin: "http://[::1]:5173", "x-verity-bridge": "1" }), null);
});

test("frontend dev bindings send the bridge header", () => {
  const api = readFileSync(join(directory, "src", "api.ts"), "utf8");
  assert.match(api, /"X-Verity-Bridge": "1"/);
});
