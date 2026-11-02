// Run with: node --test src/lib/graphqlInsert.test.mjs
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
const { effectiveGraphQLSchema, applyGraphQLTemplate, operationLabel, mockGraphQLTemplates } = loadTypeScript("graphqlInsert.ts");

const draft = {
  step: { name: "Search", protocol: "graphql", request: { url: "${baseUrl}/graphql", query: "{ old }" } },
  bodyMode: "none",
  bodyText: "",
  variablesText: "{}\n",
  jsonInBody: false,
};

test("the nearest schema source wins and an empty source is ignored", () => {
  const file = { file: "shop.graphql" };
  const introspect = { introspect: true };
  assert.deepEqual(effectiveGraphQLSchema({ schema: file }, { schema: introspect }, { schema: { file: "step.graphql" } }), { file: "step.graphql" });
  assert.deepEqual(effectiveGraphQLSchema({ schema: file }, { schema: introspect }, {}), introspect);
  assert.deepEqual(effectiveGraphQLSchema({ schema: file }, {}, {}), file);
  assert.equal(effectiveGraphQLSchema({}, {}, {}), undefined);
  assert.equal(effectiveGraphQLSchema({ schema: {} }, null, null), undefined);
});

test("inserting a template replaces the query, variables, and operation name", () => {
  const next = applyGraphQLTemplate(draft, mockGraphQLTemplates[1]);
  assert.equal(next.step.request.query, mockGraphQLTemplates[1].query);
  assert.equal(next.step.request.operationName, "widget");
  assert.equal(next.variablesText, mockGraphQLTemplates[1].variables);
  assert.equal(next.step.request.url, "${baseUrl}/graphql");
  assert.equal(draft.step.request.query, "{ old }");
});

test("the mock schema offers required arguments as variables", () => {
  assert.deepEqual(mockGraphQLTemplates.map(operationLabel), ["Query.widgets", "Query.widget"]);
  const widgets = mockGraphQLTemplates[0];
  assert.match(widgets.query, /\$q: String!/);
  assert.match(widgets.query, /widgets\(q: \$q\)/);
  assert.match(widgets.variables, /"q"/);
  assert.doesNotMatch(widgets.query, /friend/);
});

test("insert keeps variable values whose names match and asks before replacing", () => {
  const { needsReplaceConfirm, mergeVariableText, blankVariables } = loadTypeScript("graphqlInsert.ts");
  const template = mockGraphQLTemplates[1];
  assert.equal(blankVariables("{}\n"), true);
  assert.equal(blankVariables("{ \"id\": \"42\" }\n"), false);
  assert.equal(needsReplaceConfirm("", "{}\n", template), false);
  assert.equal(needsReplaceConfirm("${query}", "{}\n", template), true);
  assert.equal(needsReplaceConfirm(template.query, template.variables, template), false);
  const kept = mergeVariableText("{\n  \"id\": \"42\",\n  \"extra\": 1\n}\n", template.variables);
  assert.match(kept, /"id": "42"/);
  assert.doesNotMatch(kept, /extra/);
  const next = applyGraphQLTemplate({ ...draft, variablesText: "{\n  \"id\": \"42\"\n}\n" }, template);
  assert.equal(next.step.request.query, template.query);
  assert.match(next.variablesText, /"id": "42"/);
  const source = readFileSync(join(directory, "../components/GraphQLInsert.tsx"), "utf8");
  assert.match(source, /Replace the current query and variables\?/);
  assert.match(source, /Undo insert/);
  assert.doesNotMatch(source, /window\.confirm/);
});

test("insert keeps matching variable number literals byte-for-byte", () => {
  const { mergeVariableText } = loadTypeScript("graphqlInsert.ts");
  const current = '{"id":9007199254740993,"huge":1e400,"zero":-0,"decimal":0.1}';
  const template = '{"id":"","huge":0,"zero":0,"decimal":0}';
  const merged = mergeVariableText(current, template);
  for (const literal of ["9007199254740993", "1e400", "-0", "0.1"]) assert.match(merged, new RegExp(literal.replace(".", "\\.")));
});
