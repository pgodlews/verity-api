// Run with: node --test src/lib/events.test.mjs
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import ts from "typescript";

const directory = dirname(fileURLToPath(import.meta.url));
function loadTypeScript(name) {
  const source = readFileSync(join(directory, name), "utf8");
  const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 } });
  const module = { exports: {} };
  new Function("module", "exports", "require", outputText)(module, module.exports, () => ({}));
  return module.exports;
}
const { onEvent } = loadTypeScript("events.ts");

test("onEvent subscribes through the Wails runtime and unsubscribes", (t) => {
  const listeners = new Map();
  const offs = [];
  globalThis.window = {
    runtime: {
      EventsOn(name, cb) {
        listeners.set(name, cb);
        return () => offs.push(name);
      },
    },
  };
  t.after(() => delete globalThis.window);
  const seen = [];
  const off = onEvent("load:live", (data) => seen.push(data), () => assert.fail("fallback used"));
  listeners.get("load:live")({ vus: 2 });
  assert.deepEqual(seen, [{ vus: 2 }]);
  off();
  assert.deepEqual(offs, ["load:live"]);
});

test("onEvent falls back to EventsOff when EventsOn returns nothing", (t) => {
  const offed = [];
  globalThis.window = { runtime: { EventsOn() {}, EventsOff: (name) => offed.push(name) } };
  t.after(() => delete globalThis.window);
  onEvent("git:progress", () => {})();
  assert.deepEqual(offed, ["git:progress"]);
});

test("onEvent outside Wails uses the fallback bus, or nothing", () => {
  let subscribed = 0;
  const off = onEvent("run:event", () => {}, () => {
    subscribed++;
    return () => subscribed--;
  });
  assert.equal(subscribed, 1);
  off();
  assert.equal(subscribed, 0);
  const none = onEvent("project:changed", () => {});
  assert.equal(typeof none, "function");
  none();
});
