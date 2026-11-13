// Run with: node --test src/lib/protocols.test.mjs
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
const { protocolList } = loadTypeScript("protocols.ts");

test("footer protocol list comes from the catalog, known types first", () => {
  assert.equal(protocolList(["soap", "grpc", "rest", "graphql"]), "REST · SOAP · GraphQL · gRPC");
  assert.equal(protocolList(["zeta", "rest", "mqtt", "rest"]), "REST · mqtt · zeta");
  assert.equal(protocolList([]), "");
  assert.equal(protocolList(undefined), "");
});
