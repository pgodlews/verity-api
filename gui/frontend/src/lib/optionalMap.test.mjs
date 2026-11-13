// Run with: node --test src/lib/optionalMap.test.mjs
// Adding and then removing a header (or query parameter or step variable)
// must leave the draft unchanged, so it is not dirty and saving does not
// write an empty map.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import ts from "typescript";

const directory = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);
function loadTypeScript(name) {
  const source = readFileSync(join(directory, name), "utf8");
  const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 } });
  const module = { exports: {} };
  const localRequire = (id) => id.startsWith("./") ? loadTypeScript(id.slice(2) + (id.endsWith(".ts") ? "" : ".ts")) : require(id);
  new Function("module", "exports", "require", outputText)(module, module.exports, localRequire);
  return module.exports;
}
const { makeDraft, materialize, withOptionalMap } = loadTypeScript("workspace.ts");

test("adding then removing the last header leaves the draft clean", () => {
  const step = { name: "get", protocol: "rest", request: { method: "GET", url: "${base}/x" } };
  const draft = makeDraft(step);
  const baseline = JSON.stringify(draft);
  const req = draft.step.request;
  const added = { ...draft, step: { ...draft.step, request: withOptionalMap(req, "headers", { key: "" }) } };
  assert.notEqual(JSON.stringify(added), baseline);
  const removed = { ...added, step: { ...added.step, request: withOptionalMap(added.step.request, "headers", {}) } };
  assert.equal(JSON.stringify(removed), baseline, "draft is dirty after add+remove");
  assert.equal("headers" in materialize(removed).request, false, "save would write an empty headers map");
});

test("withOptionalMap keeps non-empty maps and other fields", () => {
  const out = withOptionalMap({ url: "u", query: { a: "1" } }, "query", { b: "2" });
  assert.deepEqual(out, { url: "u", query: { b: "2" } });
  assert.deepEqual(withOptionalMap({ name: "s", vars: { a: "1" } }, "vars", {}), { name: "s" });
});
