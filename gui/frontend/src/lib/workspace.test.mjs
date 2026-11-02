// Run with: node --test src/lib/workspace.test.mjs
// Uses only the existing TypeScript, esbuild and React development toolchain.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import ts from "typescript";
import { build } from "esbuild";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

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
const { resolvePreview, effectiveAuth, makeDraft, materialize, jsonLines, findResult, locationKey, method, sentResponse } = loadTypeScript("workspace.ts");
const storage = loadTypeScript("storage.ts");
const step = (request, protocol = "rest") => ({ name: "Request", protocol, request });

test("URL preview resolves plain references recursively but preserves secrets, unknowns and cycles", () => {
  assert.equal(resolvePreview("${base}/${id}/${password}/${missing}/${loop}", {
    base: "${prefix}/api", prefix: "/service", id: "42", password: "${env:PASSWORD}", loop: "${loop}",
  }), "/service/api/42/${env:PASSWORD}/${missing}/${loop}");
  assert.equal(resolvePreview("${secret:vault:key}/${env:TOKEN}", { "env:TOKEN": "do-not-use" }), "${secret:vault:key}/${env:TOKEN}");
  assert.equal(resolvePreview("${toString}", {}), "${toString}");
});

test("auth inheritance adds providers, replaces them, and can explicitly clear auth", () => {
  assert.deepEqual(effectiveAuth([{ use: ["transport"] }, { use: ["token"] }, undefined]), ["transport", "token"]);
  assert.deepEqual(effectiveAuth([{ use: ["transport"] }, { inherit: false, use: ["basic"] }, { use: ["key"] }]), ["basic", "key"]);
  assert.deepEqual(effectiveAuth([{ use: ["token"] }, { inherit: false, use: [] }]), []);
});

test("drafts preserve imported JSON strings, typed JSON, refs and extra request fields", () => {
  const imported = step({ method: "POST", body: '{"password":"${env:PASSWORD}"}', headers: { Accept: "application/json" }, custom: "keep" });
  const original = structuredClone(imported);
  const draft = makeDraft(imported);
  assert.equal(draft.bodyMode, "json");
  draft.bodyText = '{"password":"${env:PASSWORD}","count":2}';
  assert.deepEqual(materialize(draft).request, { ...imported.request, body: draft.bodyText });
  assert.deepEqual(imported, original);
  const typed = makeDraft(step({ json: { active: false, count: 0, data: null } }));
  typed.bodyText = '[true,2,null,"${token}"]';
  assert.equal(materialize(typed).request.jsonText, typed.bodyText);
});

test("JSON number literals survive load, an unrelated edit, and save unchanged", () => {
  const source = '{"big":9007199254740993,"infinity":1e400,"negativeZero":-0,"decimal":0.1}';
  for (const request of [{ jsonText: source }, { body: source }]) {
    const draft = makeDraft(step(request));
    draft.step.name = "Edited elsewhere";
    const saved = materialize(draft);
    assert.equal(saved.request.jsonText ?? saved.request.body, source);
    assert.equal(saved.name, "Edited elsewhere");
  }
  const graphql = makeDraft(step({ query: "{ old }", variablesText: source }, "graphql"));
  graphql.step.request.query = "{ edited }";
  assert.equal(materialize(graphql).request.variablesText, source);
});

test("body mode changes remove mutually exclusive fields without losing headers", () => {
  const draft = makeDraft(step({ json: { old: true }, headers: { Accept: "text/plain" } }));
  draft.bodyMode = "form"; draft.step.request.form = { key: "${value}" };
  assert.deepEqual(materialize(draft).request, { headers: { Accept: "text/plain" }, form: { key: "${value}" } });
  draft.bodyMode = "none";
  assert.deepEqual(materialize(draft).request, { headers: { Accept: "text/plain" } });
});

test("invalid JSON and non-object GraphQL variables fail before saving", () => {
  const draft = makeDraft(step({ json: {} }));
  draft.bodyText = '{"partial":';
  assert.throws(() => materialize(draft), /valid JSON/);
  const graphql = makeDraft(step({ query: "query { items { id } }", variables: {} }, "graphql"));
  for (const text of ["{", "[]", "null", "1"]) {
    graphql.variablesText = text;
    assert.throws(() => materialize(graphql), /GraphQL variables/);
  }
  graphql.variablesText = '{"id":"${id}"}';
  assert.equal(materialize(graphql).request.variablesText, graphql.variablesText);
});

test("SOAP drafts retain action/version and edit only the envelope", () => {
  const original = step({ body: "<Envelope/>", soapAction: "Lookup", version: "1.2", headers: { Accept: "application/xml" } }, "soap");
  const draft = makeDraft(original); draft.bodyText = "<Envelope>${id}</Envelope>";
  assert.deepEqual(materialize(draft).request, { ...original.request, body: draft.bodyText });
});

test("JSON lines remain valid pretty JSON and carry exact typed values and escaped paths", () => {
  const value = { items: [{ id: 0, active: false, value: null }], "a.b": { "it's\\odd": "text" }, "line\nbreak": [] };
  const lines = jsonLines(value);
  assert.deepEqual(JSON.parse(lines.map(line => line.text).join("\n")), value);
  assert.equal(lines.find(line => line.path === "$.items[0].id").value, 0);
  assert.equal(lines.find(line => line.path === "$.items[0].active").value, false);
  assert.equal(lines.find(line => line.path === "$.items[0].value").value, null);
  assert.equal(lines.find(line => line.path === "$['a.b']['it\\'s\\\\odd']").value, "text");
  assert.deepEqual(lines.find(line => line.path === "$['line\\nbreak']").value, []);
  assert.deepEqual(jsonLines(null), [{ text: "null", path: "$", value: null }]);
});

test("lossless prettify and response lines retain every number token", () => {
  const { formatJSONText, jsonTextLines } = loadTypeScript("jsonText.ts");
  const source = '{"big":9007199254740993,"infinity":1e400,"negativeZero":-0,"decimal":0.1}';
  const pretty = formatJSONText(source);
  for (const literal of ["9007199254740993", "1e400", "-0", "0.1"]) assert.match(pretty, new RegExp(literal.replace(".", "\\.")));
  const lines = jsonTextLines(source);
  assert.equal(lines.map(line => line.text).join("\n"), pretty);
  assert.equal(lines.find(line => line.path === "$.big").text, '  "big": 9007199254740993,');
});

test("truncated JSON containers require explicit closing delimiters", () => {
  const { parseJSONText, formatJSONText } = loadTypeScript("jsonText.ts");
  for (const source of ["[1,", '{"x":1,']) {
    assert.throws(() => parseJSONText(source), /unterminated JSON container/);
    assert.throws(() => formatJSONText(source), /unterminated JSON container/);
    const draft = makeDraft(step({ json: {} }));
    draft.bodyText = source;
    assert.throws(() => materialize(draft), /valid JSON/);
  }
});

test("numeric response assertions remain structured-clone safe through save", () => {
  const { jsonTextLines } = loadTypeScript("jsonText.ts");
  const value = jsonTextLines('{"n":1}').find(line => line.path === "$.n").value;
  assert.equal(value, 1);
  const draft = makeDraft(step({ json: {} }));
  draft.step.assertions = [{ type: "jsonpath", expr: "$.n", op: "eq", value }];
  const saved = materialize(draft);
  assert.equal(saved.assertions[0].value, 1);
});

test("response values retain __proto__ and share reconstructed subtrees", () => {
  const { jsonTextLines } = loadTypeScript("jsonText.ts");
  const lines = jsonTextLines('{"__proto__":{"a":1},"n":2}');
  const root = lines.find(line => line.path === "$").value;
  assert.equal(Object.hasOwn(root, "__proto__"), true);
  assert.equal(JSON.stringify(root), '{"__proto__":{"a":1},"n":2}');
  assert.equal(root.__proto__, lines.find(line => line.path === "$.__proto__").value);
  const nested = jsonTextLines('[[{"x":1}]]');
  const outer = nested.find(line => line.path === "$").value;
  assert.equal(outer[0], nested.find(line => line.path === "$[0]").value);
  assert.equal(outer[0][0], nested.find(line => line.path === "$[0][0]").value);
});

test("results match suite, case, step name and index, using the latest data iteration", () => {
  const at = { suite: "Suite", caseName: "Case", index: 1 };
  const result = { name: "Duplicate", status: "failed" };
  const run = { suites: [{ name: "Suite", cases: [
    { name: "Case", steps: [{ name: "Duplicate", status: "passed" }, { name: "Duplicate", status: "passed" }] },
    { name: "Case", steps: [{ name: "Duplicate", status: "error" }, result] },
  ] }] };
  assert.equal(findResult(run, at, "Duplicate"), result);
  assert.equal(findResult(run, at, "Renamed"), undefined);
  assert.equal(findResult(run, { ...at, caseName: "Other" }, "Duplicate"), undefined);
  assert.equal(findResult(run, { ...at, index: 2 }, "Duplicate"), undefined);
  assert.equal(findResult(null, at, "Duplicate"), undefined);
  assert.notEqual(locationKey({ suite: "a/b", caseName: "c", index: 0 }), locationKey({ suite: "a", caseName: "b/c", index: 0 }));
  assert.equal(method(step({}, "graphql")), "GQL");
  assert.equal(method(step({}, "soap")), "SOAP");
});

test("blocked and malformed local storage are recoverable; recents are deduplicated and bounded", () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  try {
    Object.defineProperty(globalThis, "localStorage", { configurable: true, get() { throw new Error("blocked"); } });
    assert.deepEqual(storage.recentProjects(), []);
    assert.deepEqual(storage.rememberProject("project.yaml"), ["project.yaml"]);
    assert.equal(storage.initialTheme(), "dark");
    const values = new Map([["verity.recent-projects", "malformed"]]);
    Object.defineProperty(globalThis, "localStorage", { configurable: true, value: { getItem: key => values.get(key), setItem: (key, value) => values.set(key, value) } });
    assert.deepEqual(storage.recentProjects(), []);
    for (let i = 0; i < 10; i++) storage.rememberProject(`project-${i}.yaml`);
    assert.equal(storage.recentProjects().length, 8);
    storage.rememberProject("project-8.yaml");
    assert.equal(storage.recentProjects()[0], "project-8.yaml");
    assert.equal(new Set(storage.recentProjects()).size, 8);
  } finally {
    if (previous) Object.defineProperty(globalThis, "localStorage", previous); else delete globalThis.localStorage;
  }
});

let componentBundle;
async function loadComponents() {
  if (componentBundle) return componentBundle;
  const output = await build({ stdin: { contents: `
    export { App } from '../App';
    export { WorkspaceView } from '../views/WorkspaceView';
    export { RequestEditor } from '../components/RequestEditor';
    export { ResponsePane } from '../components/ResponsePane';
    export { mockBindings, noteMockDiskChange } from '../dev/mock';
  `, resolveDir: directory, loader: "tsx" }, bundle: true, platform: "node", format: "cjs", write: false,
    external: ["react", "react-dom"], loader: { ".css": "empty" }, define: { "import.meta.env.DEV": "false" } });
  const module = { exports: {} };
  new Function("module", "exports", "require", output.outputFiles[0].text)(module, module.exports, require);
  componentBundle = module.exports;
  return componentBundle;
}

test("React server rendering covers welcome, protocol editors, JSON actions and transport errors", async () => {
  const { App, WorkspaceView, RequestEditor, ResponsePane } = await loadComponents();
  const noop = () => {};
  const previousWindow = globalThis.window;
  globalThis.window = {};
  try { assert.match(renderToStaticMarkup(createElement(App)), /Welcome to Verity/); }
  finally { if (previousWindow === undefined) delete globalThis.window; else globalThis.window = previousWindow; }
  const sample = step({ method: "POST", url: "${base}/resource", json: {} });
  const info = { path: "project.yaml", project: { schemaVersion: "1.0", name: "Collection", suites: [{ name: "Suite", cases: [{ name: "Case", steps: [sample] }] }] }, envRefs: [], undefinedVars: [], captures: [] };
  const at = { suite: "Suite", caseName: "Case", index: 0 };
  const workspace = renderToStaticMarkup(createElement(WorkspaceView, { info, environment: "", lastRun: null, running: false, active: true, openStep: null, filter: "", onFilter: noop, onSaveStep: noop, onSend: noop, onImport: noop, onDirtyChange: noop }));
  assert.match(workspace, /Open requests/);
  assert.match(workspace, /Send a request to see its response/);
  for (const protocol of ["rest", "graphql", "soap"]) {
    for (const tab of ["params", "headers", "body", "auth", "assertions", "capture", "vars"]) {
      const draft = makeDraft({ ...sample, protocol, assertions: [{ type: "status", value: 200 }], transfers: [{ name: "id", from: "jsonpath", expr: "$.id" }] });
      const html = renderToStaticMarkup(createElement(RequestEditor, { info, environment: "", at, draft, onChange: noop, tab, onTab: noop, dirty: false, busy: false, onSave: noop, onSend: noop }));
      assert.match(html, /aria-label="Request"/);
    }
  }
  const result = { name: "Request", status: "passed", durationMs: 10, response: { statusCode: 200, body: '{"nested":{"id":0}}' } };
  const html = renderToStaticMarkup(createElement(ResponsePane, { result, busy: false, onAssert: noop, onCapture: noop }));
  assert.match(html, /Assert \$\.nested\.id/);
  assert.match(html, /Capture \$\.nested\.id/);
  assert.match(html, /tok-key/);
  const errorHtml = renderToStaticMarkup(createElement(ResponsePane, { result: { ...result, error: { code: "transport_error", message: "Connection failed" } }, busy: false, onAssert: noop, onCapture: noop }));
  assert.match(errorHtml, /transport_error/);
  assert.match(errorHtml, /Connection failed/);
});


test("mock inspect returns step and file hashes, and a disk change moves them", async () => {
  const { mockBindings, noteMockDiskChange } = await loadComponents();
  const first = await mockBindings.InspectProject();
  assert.ok(first.stepFiles.length > 1);
  assert.equal(first.fileHashes.length, 1);
  assert.equal(first.fileHashes[0].path, first.path);
  for (const step of first.stepFiles) {
    assert.match(step.hash, /^[0-9a-f]{64}$/);
    assert.equal(typeof step.occurrence, "number");
    assert.equal(step.file, first.path);
  }
  assert.match(first.fileHashes[0].hash, /^[0-9a-f]{64}$/);
  noteMockDiskChange();
  const second = await mockBindings.InspectProject();
  assert.notEqual(second.stepFiles[0].hash, first.stepFiles[0].hash);
  assert.notEqual(second.stepFiles[1].hash, first.stepFiles[1].hash);
  assert.notEqual(second.fileHashes[0].hash, first.fileHashes[0].hash);
  assert.match(second.stepFiles[0].hash, /^[0-9a-f]{64}$/);
});

test("HTTP status classes stay independent from failed checks", async () => {
  const { ResponsePane } = await loadComponents();
  const noop = () => {};
  const assertions = [true, true, true, false].map(passed => ({ type: "jsonpath", passed }));
  for (const [code, colour] of [[200, "passed"], [204, "passed"], [301, "neutral"], [304, "neutral"], [400, "failed"], [503, "failed"]]) {
    const result = { name: "Request", status: "failed", durationMs: 10, assertions, response: { statusCode: code, body: "{}" } };
    const html = renderToStaticMarkup(createElement(ResponsePane, { result, busy: false, onAssert: noop, onCapture: noop }));
    assert.match(html, new RegExp(`class="pill status-${colour}"`));
    assert.match(html, /class="check-fail">3 of 4 checks passed/);
  }
  const html = renderToStaticMarkup(createElement(ResponsePane, {
    result: { name: "Request", status: "error", durationMs: 1, error: { code: "transport_error", message: "No response" } },
    busy: false, onAssert: noop, onCapture: noop,
  }));
  assert.match(html, /class="pill status-error"/);
});

test("compact editors expose labelled controls, last-run glyphs, quick adds and URL chips", async () => {
  const { RequestEditor } = await loadComponents();
  const noop = () => {};
  const assertions = [{ type: "status", op: "eq", value: 200 }, { type: "response_time", op: "lt", value: 1000 }, { type: "jsonpath", expr: "$.id", op: "eq", value: 42 }];
  const sample = { ...step({ url: "${baseUrl}/items/${env:ITEM_ID}" }), assertions, transfers: [{ name: "id", from: "jsonpath", expr: "$.id" }] };
  const info = { path: "project.yaml", project: { schemaVersion: "1.0", suites: [{ name: "Suite", cases: [{ name: "Case", steps: [sample] }] }] }, envRefs: [], undefinedVars: [], captures: [] };
  const props = {
    info, at: { suite: "Suite", caseName: "Case", index: 0 }, environment: "", draft: makeDraft(sample), tab: "assertions",
    result: { name: sample.name, assertions: [{ type: "status", passed: true }, { type: "response_time", passed: true }, { type: "jsonpath", passed: false }] },
    onChange: noop, onTab: noop, onSave: noop, onSend: noop, dirty: false, busy: false,
  };
  const html = renderToStaticMarkup(createElement(RequestEditor, props));
  assert.match(html, /aria-label="Last run: passed"/);
  assert.match(html, /aria-label="Last run: failed"/);
  assert.doesNotMatch(html, /aria-label="Assertion [12] expression"/);
  assert.match(html, /aria-label="Assertion 3 expression"/);
  assert.match(html, /<select[^>]*aria-label="Assertion 1 type"/);
  assert.match(html, /<select[^>]*aria-label="Assertion 1 operator"/);
  for (const label of ["Status", "JSONPath", "Header", "Body contains", "Response time", "JSON Schema"]) {
    assert.match(html, new RegExp(`<button[^>]*>${label}</button>`));
  }
  assert.match(html, /class="url-chips mono" aria-hidden="true"/);
  assert.match(html, /class="var-chip">\$\{baseUrl\}/);
  assert.match(html, /class="var-chip secret">\$\{env:ITEM_ID\}/);
  const capture = renderToStaticMarkup(createElement(RequestEditor, { ...props, tab: "capture" }));
  assert.match(capture, /aria-label="Capture 1 name"/);
  assert.match(capture, /<select[^>]*aria-label="Capture 1 from"/);
  assert.match(capture, /aria-label="Capture 1 expression"/);
});

test("credential findings mark the field, badge the tab, and block save without repeating the value", async () => {
  const { RequestEditor } = await loadComponents();
  const literal = "literal-secret";
  const sample = step({
    method: "POST",
    headers: { "X-Api-Key": literal, Accept: "application/json" },
    json: { password: literal, note: "keep" },
  });
  const findings = [
    { path: ["request", "headers", "X-Api-Key"], key: "X-Api-Key", reason: "name", envVar: "X_API_KEY" },
    { path: ["request", "json", "password"], key: "password", reason: "name", envVar: "PASSWORD" },
  ];
  const info = { path: "project.yaml", project: { schemaVersion: "1.0", name: "Collection", suites: [{ name: "Suite", cases: [{ name: "Case", steps: [sample] }] }] }, envRefs: [], undefinedVars: [], captures: [] };
  const props = {
    info, environment: "", at: { suite: "Suite", caseName: "Case", index: 0 }, draft: makeDraft(sample),
    findings, onChange: () => {}, onTab: () => {}, onSave: () => {}, onSend: () => {}, onStoreSecret: () => {},
    dirty: true, busy: false,
  };
  const headers = renderToStaticMarkup(createElement(RequestEditor, { ...props, tab: "headers" }));
  assert.equal((headers.match(/count-warn/g) ?? []).length, 2);
  assert.match(headers, /value="literal-secret"/);
  const headerWarn = headers.slice(headers.indexOf('class="secret-warn"'), headers.indexOf('class="hint"'));
  assert.match(headerWarn, /Looks like a credential\. Project files are committed to git, so keep it out of them\./);
  assert.match(headerWarn, /Store as \$\{env:X_API_KEY\}/);
  assert.match(headerWarn, /Store in OS keychain/);
  assert.match(headerWarn, /Keychain name/);
  assert.match(headerWarn, /placeholder="project\/NAME"/);
  assert.doesNotMatch(headerWarn, /literal-secret/);
  assert.match(headers, /Set X_API_KEY on this machine or in an untracked \.env file \(verity run --env-file \.env\)/);
  assert.match(headers, /2 values look like credentials — store them in the OS keychain or as environment variables to save\./);
  assert.match(headers, /<button class="btn btn-sm" disabled="" title="2 values look like credentials — store them in the OS keychain or as environment variables to save\.">/);
  const body = renderToStaticMarkup(createElement(RequestEditor, { ...props, tab: "body" }));
  const bodyWarn = body.slice(body.indexOf('class="secret-warn"'), body.indexOf('class="hint"'));
  assert.match(bodyWarn, /\$\.password/);
  assert.match(bodyWarn, /Store as \$\{env:PASSWORD\}/);
  assert.doesNotMatch(bodyWarn, /literal-secret/);
  assert.match(body, /has-secret/);
});

test("mock CheckStep flags a typed literal and SaveStep refuses it without echoing the value", async () => {
  const { mockBindings } = await loadComponents();
  const literal = "literal-secret";
  const stepWithSecret = { name: "Login", protocol: "rest", request: { method: "POST", headers: { "X-Api-Key": literal } } };
  const found = await mockBindings.CheckStep(stepWithSecret);
  assert.equal(found.length, 1);
  assert.deepEqual(found[0].path, ["request", "headers", "X-Api-Key"]);
  assert.equal(found[0].envVar, "X_API_KEY");
  assert.equal(found[0].reason, "name");
  assert.equal(Object.hasOwn(found[0], "value"), false);
  await assert.rejects(
    () => mockBindings.SaveStep({ path: "project.yaml", suite: "Auth", case: "Auth", index: 0, step: stepWithSecret }),
    (error) => {
      assert.match(String(error), /request\.headers\.X-Api-Key looks like a credential/);
      assert.match(String(error), /\$\{env:X_API_KEY\}/);
      assert.equal(String(error).includes(literal), false);
      return true;
    },
  );
  const clean = await mockBindings.CheckStep({ name: "Login", protocol: "rest", request: { headers: { "X-Api-Key": "${env:X_API_KEY}" } } });
  assert.deepEqual(clean, []);
});

test("mock Auth/Me Send includes Login and renders the prerequisite note beside response status", async () => {
  const { mockBindings, ResponsePane } = await loadComponents();
  const info = await mockBindings.InspectProject("project.yaml");
  const suite = info.project.suites.find(s => s.name === "Auth");
  const testCase = suite.cases.find(c => c.steps.some(s => s.name === "Me"));
  const at = { suite: suite.name, caseName: testCase.name, index: testCase.steps.findIndex(s => s.name === "Me") };
  assert.equal(at.index, 1);
  const run = await mockBindings.RunProject({ path: info.path, environment: "", parallel: 1, vars: {}, suites: [at.suite], cases: [at.caseName], untilStep: at.index + 1 });
  const sent = sentResponse(run, at, "Me");
  assert.deepEqual(run.suites[0].cases[0].steps.map(s => s.name), ["Login", "Me"]);
  assert.equal(sent.result.name, "Me");
  assert.equal(sent.note, "Ran Login first.");
  const html = renderToStaticMarkup(createElement(ResponsePane, { ...sent, busy: false, onAssert: () => {}, onCapture: () => {} }));
  const responseHeader = html.slice(html.indexOf('class="response-top"'), html.indexOf('aria-label="Response sections"'));
  assert.match(responseHeader, /200 OK/);
  assert.match(responseHeader, /class="run-note" role="status">Ran Login first\./);
});

test("authorization-code auth offers sign in and mock sessions remain scoped", async () => {
  const { RequestEditor, mockBindings } = await loadComponents();
  const info = await mockBindings.InspectProject("project.yaml");
  const suite = info.project.suites[0];
  const testCase = suite.cases[0];
  const at = { suite: suite.name, caseName: testCase.name, index: 0 };
  const definition = { name: "browser-login", type: "oauth2", config: { grant: "authorization_code", clientId: "client" } };
  info.project.auth = [definition];
  testCase.steps[0].auth = { use: [definition.name], inherit: false };
  const noop = () => {};
  const props = { info, environment: "", at, draft: makeDraft(testCase.steps[0]), onChange: noop, tab: "auth", onTab: noop, dirty: false, busy: false, onSave: noop, onSend: noop };
  const html = renderToStaticMarkup(createElement(RequestEditor, props));
  assert.match(html, />Sign in<\/button>/);
  definition.config.grant = "client_credentials";
  assert.doesNotMatch(renderToStaticMarkup(createElement(RequestEditor, props)), />Sign in<\/button>/);
  const req = { path: info.path, environment: "first", authName: definition.name };
  assert.equal(await mockBindings.OAuthStatus(req), false);
  assert.equal(await mockBindings.OAuthLogin(req), undefined);
  assert.equal(await mockBindings.OAuthStatus(req), true);
  assert.equal(await mockBindings.OAuthStatus({ ...req, environment: "other" }), false);
  assert.equal(await mockBindings.OAuthStatus({ ...req, authName: "other" }), false);
  assert.equal(await mockBindings.OAuthLogout(req), undefined);
  assert.equal(await mockBindings.OAuthStatus(req), false);
});

test("mock keychain binding returns only a reference and rejects invalid names safely", async () => {
  const { mockBindings } = await loadComponents();
  assert.equal(await mockBindings.StoreKeychainSecret("project/TOKEN", "test-sensitive-value"), "${secret:keychain:project/TOKEN}");
  const step = { name: "Call", protocol: "rest", request: { headers: { Authorization: "Bearer ${secret:keychain:project/TOKEN}" } } };
  assert.deepEqual(await mockBindings.CheckStep(step), []);
  await assert.rejects(() => mockBindings.StoreKeychainSecret("bad:name", "test-sensitive-value"), error => {
    assert.match(error.message, /config_error/);
    assert.doesNotMatch(error.message, /test-sensitive-value/);
    return true;
  });
});

test("mock keychain existence is boolean, validates names, and rejects whitespace-only stores", async () => {
  const { mockBindings } = await loadComponents();
  const name = "project/MOCK_EXISTENCE_TOKEN";
  assert.equal(await mockBindings.KeychainSecretExists(name), false);
  await assert.rejects(() => mockBindings.StoreKeychainSecret(name, " \t\r\n\u0085\u2003"), /empty/);
  assert.equal(await mockBindings.KeychainSecretExists(name), false);
  assert.equal(await mockBindings.StoreKeychainSecret(name, "  mock-value \r\n"), "${secret:keychain:" + name + "}");
  assert.equal(await mockBindings.KeychainSecretExists(name), true);
  await assert.rejects(() => mockBindings.KeychainSecretExists("bad:name"), /config_error/);
  for (const value of ["token123", "BearerABC", "BasicABC"]) {
    assert.equal((await mockBindings.CheckStep(step({ headers: { Authorization: value } }))).length, 1);
  }
  for (const scheme of ["Bearer", "Token", "ApiKey", "AWS4-HMAC-SHA256"]) {
    assert.deepEqual(await mockBindings.CheckStep(step({ headers: { Authorization: scheme + " \t${secret:keychain:project/TOKEN}" } })), []);
  }
});
