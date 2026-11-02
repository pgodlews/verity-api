// Run with: node --test src/lib/stepSecrets.test.mjs
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { build } from "esbuild";

const directory = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

let api;
async function load() {
  if (api) return api;
  const output = await build({
    stdin: {
      contents: `
        export {
          applySecretReplacement, findingTab, secretPlace, credentialSaveLine, credentialBadgeLabel, inTextHint, jsonValueSpans,
          secretHint, storeAsLabel, SECRET_FIELD_COPY, SECRET_CHECK_DELAY_MS, jsonPointerLabel,
          findingPathString, secretReplacement, keychainValue, storeDraftInKeychain, KeychainReplacementRequired,
        } from "./stepSecrets.ts";
        export { makeDraft } from "./workspace.ts";
      `,
      resolveDir: directory,
      loader: "ts",
    },
    bundle: true,
    platform: "node",
    format: "cjs",
    write: false,
  });
  const module = { exports: {} };
  new Function("module", "exports", "require", output.outputFiles[0].text)(module, module.exports, require);
  api = module.exports;
  return api;
}

const step = request => ({ name: "Request", protocol: "rest", request });
const finding = (path, envVar, key = path[path.length - 1]) => ({ path, key, envVar });

test("finding paths map onto request-editor tabs", async () => {
  const { findingTab, secretPlace } = await load();
  const cases = [
    [["request", "headers", "X-Api-Key"], "headers", "header"],
    [["request", "headers", "Authorization"], "headers", "header"],
    [["request", "query", "api_key"], "params", "query"],
    [["request", "query"], "body", "graphql-query"],
    [["request", "form", "password"], "body", "form"],
    [["request", "form", "user", "password"], "body", "form-nested"],
    [["request", "json", "password"], "body", "json"],
    [["request", "json", "user", "password"], "body", "json"],
    [["request", "json", "items", "0", "token"], "body", "json"],
    [["request", "variables", "token"], "body", "variables"],
    [["request", "body"], "body", "body"],
    [["vars", "token"], "vars", "var"],
    [["assertions", "0", "value"], undefined, "other"],
    [["name"], undefined, "other"],
    [["request", "headers"], undefined, "other"],
    [["vars", "nested", "token"], undefined, "other"],
  ];
  for (const [path, tab, place] of cases) {
    assert.equal(findingTab(path), tab, path.join("."));
    assert.equal(secretPlace(path), place, path.join("."));
  }
});

test("JSON labels name the field and never a value", async () => {
  const { jsonPointerLabel, findingPathString } = await load();
  assert.equal(jsonPointerLabel(["request", "json", "password"]), "$.password");
  assert.equal(jsonPointerLabel(["request", "json", "user", "password"]), "$.user.password");
  assert.equal(jsonPointerLabel(["request", "json", "items", "0", "token"]), "$.items[0].token");
  assert.equal(jsonPointerLabel(["request", "variables", "token"]), "$.token");
  assert.equal(jsonPointerLabel(["request", "json", "a.b"]), '$["a.b"]');
  assert.equal(findingPathString(["request", "headers", "X-Api-Key"]), "request.headers.X-Api-Key");
  assert.equal(findingPathString(["request", "json", "items", "0", "token"]), "request.json.items[0].token");
});

test("save line and store label use the suggested name, not a value", async () => {
  const { credentialSaveLine, credentialBadgeLabel, secretHint, storeAsLabel, SECRET_FIELD_COPY, SECRET_CHECK_DELAY_MS } = await load();
  assert.equal(credentialSaveLine(1), "1 value looks like a credential — store it in the OS keychain or as an environment variable to save.");
  assert.equal(credentialSaveLine(2), "2 values look like credentials — store them in the OS keychain or as environment variables to save.");
  assert.equal(credentialBadgeLabel(1), "1 value looks like a credential");
  assert.equal(credentialBadgeLabel(3), "3 values look like credentials");
  assert.equal(secretHint("API_TOKEN"), "Set API_TOKEN on this machine or in an untracked .env file (verity run --env-file .env)");
  assert.equal(storeAsLabel("API_TOKEN"), "Store as ${env:API_TOKEN}");
  assert.match(SECRET_FIELD_COPY, /committed to git/);
  assert.equal(SECRET_FIELD_COPY.includes("literal"), false);
  assert.equal(SECRET_CHECK_DELAY_MS, 300);
});

test("replacement keeps an Authorization scheme and drops the literal", async () => {
  const { secretReplacement } = await load();
  const cases = [
    ["X-Api-Key", "literal-secret", "X_API_KEY", "${env:X_API_KEY}"],
    ["X-Api-Key", "Bearer literal-secret", "X_API_KEY", "${env:X_API_KEY}"],
    ["Authorization", "Bearer literal-secret", "AUTHORIZATION", "Bearer ${env:AUTHORIZATION}"],
    ["Authorization", "bearer literal-secret", "AUTHORIZATION", "bearer ${env:AUTHORIZATION}"],
    ["Authorization", "BEARER literal-secret", "AUTHORIZATION", "BEARER ${env:AUTHORIZATION}"],
    ["Authorization", "Basic literal-secret", "AUTHORIZATION", "Basic ${env:AUTHORIZATION}"],
    ["Authorization", "Basic  literal-secret", "AUTHORIZATION", "Basic  ${env:AUTHORIZATION}"],
    ["Proxy-Authorization", "Digest literal-secret", "PROXY_AUTHORIZATION", "Digest ${env:PROXY_AUTHORIZATION}"],
    ["Authorization", "literal-secret", "AUTHORIZATION", "${env:AUTHORIZATION}"],
    ["password", "1234", "PASSWORD", "${env:PASSWORD}"],
  ];
  for (const [key, value, envVar, want] of cases) {
    const got = secretReplacement(key, value, envVar);
    assert.equal(got, want, key);
    assert.equal(got.includes("literal-secret"), false);
    assert.equal(got.includes("1234"), false);
  }
});

test("applySecretReplacement rewrites the draft by path and leaves siblings", async () => {
  const { applySecretReplacement, makeDraft } = await load();
  const literal = "literal-secret";
  const draft = makeDraft({
    name: "Login",
    protocol: "rest",
    request: {
      method: "POST",
      headers: { Authorization: `Bearer ${literal}`, Accept: "application/json", "X-Api-Key": literal },
      query: { api_key: literal, q: "widgets" },
      json: { user: { password: literal }, note: "keep", items: [{ token: literal }], count: 1 },
    },
    vars: { token: literal, region: "eu" },
  });
  draft.step.request.form = { password: literal, name: "ada" };

  const original = structuredClone(draft);
  let next = draft;
  const steps = [
    [["request", "headers", "Authorization"], "AUTHORIZATION"],
    [["request", "headers", "X-Api-Key"], "X_API_KEY"],
    [["request", "query", "api_key"], "API_KEY"],
    [["request", "json", "user", "password"], "PASSWORD"],
    [["request", "json", "items", "0", "token"], "TOKEN"],
    [["request", "form", "password"], "PASSWORD"],
    [["vars", "token"], "TOKEN"],
  ];
  for (const [path, envVar] of steps) next = applySecretReplacement(next, finding(path, envVar));

  assert.deepEqual(draft, original);
  assert.equal(next.step.request.headers.Authorization, "Bearer ${env:AUTHORIZATION}");
  assert.equal(next.step.request.headers["X-Api-Key"], "${env:X_API_KEY}");
  assert.equal(next.step.request.headers.Accept, "application/json");
  assert.equal(next.step.request.query.api_key, "${env:API_KEY}");
  assert.equal(next.step.request.query.q, "widgets");
  const body = JSON.parse(next.bodyText);
  assert.equal(body.user.password, "${env:PASSWORD}");
  assert.equal(body.items[0].token, "${env:TOKEN}");
  assert.equal(body.note, "keep");
  assert.equal(body.count, 1);
  assert.equal(JSON.parse(next.step.request.jsonText).user.password, "${env:PASSWORD}");
  assert.equal(next.step.request.form.password, "${env:PASSWORD}");
  assert.equal(next.step.request.form.name, "ada");
  assert.equal(next.step.vars.token, "${env:TOKEN}");
  assert.equal(next.step.vars.region, "eu");
  assert.equal(JSON.stringify(next).includes(literal), false);
});

test("applySecretReplacement updates GraphQL variables, a string query, and a numeric JSON field", async () => {
  const { applySecretReplacement, makeDraft } = await load();
  const draft = makeDraft({
    name: "Query",
    protocol: "graphql",
    request: { query: "literal-secret", variables: { token: "literal-secret", id: 7 } },
  });
  const query = applySecretReplacement(draft, finding(["request", "query"], "QUERY", "query"));
  assert.equal(query.step.request.query, "${env:QUERY}");
  assert.equal(query.variablesText, draft.variablesText);
  const variables = applySecretReplacement(query, finding(["request", "variables", "token"], "TOKEN"));
  assert.deepEqual(JSON.parse(variables.variablesText), { token: "${env:TOKEN}", id: 7 });
  assert.equal(JSON.parse(variables.step.request.variablesText).token, "${env:TOKEN}");
  assert.equal(JSON.stringify(variables).includes("literal-secret"), false);

  const numeric = makeDraft({ name: "Body", protocol: "rest", request: { json: { password: 1234, keep: true } } });
  const stored = applySecretReplacement(numeric, finding(["request", "json", "password"], "PASSWORD"));
  assert.equal(JSON.parse(stored.bodyText).password, "${env:PASSWORD}");
  assert.equal(JSON.parse(stored.bodyText).keep, true);
  assert.equal(JSON.parse(numeric.bodyText).password, 1234);
});

test("applySecretReplacement no-ops when the path is missing and does not invent a value", async () => {
  const { applySecretReplacement, makeDraft } = await load();
  const draft = makeDraft({ name: "Get", protocol: "rest", request: { headers: { Accept: "application/json" }, json: { ok: true } } });
  assert.equal(applySecretReplacement(draft, finding(["request", "headers", "X-Api-Key"], "X_API_KEY")), draft);
  assert.equal(applySecretReplacement(draft, finding(["request", "json", "password"], "PASSWORD")), draft);
  assert.equal(applySecretReplacement(draft, finding(["request", "json", "items", "3", "token"], "TOKEN")), draft);
  assert.equal(applySecretReplacement(draft, finding(["missing"], "SECRET")), draft);
  const broken = { ...draft, bodyText: "{not json" };
  assert.equal(applySecretReplacement(broken, finding(["request", "json", "password"], "PASSWORD")), broken);
});

test("in-text findings (URL, XML/form body) have no one-click replacement", async () => {
  const { applySecretReplacement, secretPlace, inTextHint } = await load();
  assert.equal(secretPlace(["request", "url"]), "url");
  const draft = { step: { name: "s", protocol: "rest", request: { url: "https://u:hunter2@x/p" } }, bodyText: "", variablesText: "{}" };
  const same = applySecretReplacement(draft, { path: ["request", "url"], key: "password", envVar: "URL_PASSWORD", inText: true });
  assert.equal(same, draft);
  const hint = inTextHint("password", "URL_PASSWORD");
  assert.match(hint, /\$\{env:URL_PASSWORD\}/);
  assert.doesNotMatch(hint, /hunter2/);
});

test("JSON replacement edits only the target value (duplicate keys, big numbers kept)", async () => {
  const { applySecretReplacement, jsonValueSpans } = await load();
  const text = '{\n  "password": "hunter2",\n  "id": 9007199254740993,\n  "password": "again"\n}';
  assert.equal(jsonValueSpans(text, ["password"]).length, 2);
  const draft = { step: { name: "s", protocol: "rest", request: { body: text } }, bodyText: text, variablesText: "{}", bodyMode: "text", jsonInBody: true };
  const next = applySecretReplacement(draft, { path: ["request", "body", "password"], key: "password", envVar: "PASSWORD" });
  assert.match(next.bodyText, /9007199254740993/);
  assert.doesNotMatch(next.bodyText, /hunter2|again/);
  assert.equal((next.bodyText.match(/\$\{env:PASSWORD\}/g) || []).length, 2);
  assert.match(next.bodyText, /^\{\n  "password"/); // formatting untouched
});

test("keychain store sends the credential without its scheme, then replaces the draft", async () => {
  const { storeDraftInKeychain, makeDraft } = await load();
  const draft = makeDraft({ name: "Call", protocol: "rest", request: { headers: { Authorization: "Bearer test-sensitive-value", Accept: "application/json" } } });
  const detected = finding(["request", "headers", "Authorization"], "TOKEN");
  const before = structuredClone(draft);
  let writes = 0;
  const next = await storeDraftInKeychain(draft, detected, "project/TOKEN", async (name, value) => {
    assert.equal(name, "project/TOKEN");
    assert.equal(value, "test-sensitive-value");
    assert.deepEqual(draft, before);
    writes++;
    return "${secret:keychain:project/TOKEN}";
  });
  assert.equal(writes, 1);
  assert.equal(next.step.request.headers.Authorization, "Bearer ${secret:keychain:project/TOKEN}");
  assert.equal(next.step.request.headers.Accept, "application/json");
  assert.doesNotMatch(JSON.stringify(next), /test-sensitive-value/);
  assert.deepEqual(draft, before);
  await assert.rejects(() => storeDraftInKeychain(draft, detected, "TOKEN", async () => { throw new Error("config_error: store locked"); }), /store locked/);
  assert.deepEqual(draft, before);
});

test("keychain extraction and replacement handle JSON, body, form, vars and GraphQL", async () => {
  const { storeDraftInKeychain, makeDraft } = await load();
  const scenarios = [
    [{ json: { password: "test-sensitive-value", keep: true } }, ["request", "json", "password"]],
    [{ body: '{"password":"test-sensitive-value","id":9007199254740993}' }, ["request", "body", "password"]],
    [{ form: { password: "test-sensitive-value" } }, ["request", "form", "password"]],
    [{ variables: { token: "test-sensitive-value" } }, ["request", "variables", "token"]],
    [{ query: "test-sensitive-value" }, ["request", "query"]],
  ];
  for (const [request, path] of scenarios) {
    const protocol = ["variables", "query"].includes(path[1]) ? "graphql" : "rest";
    const draft = makeDraft({ name: "Call", protocol, request });
    const next = await storeDraftInKeychain(draft, finding(path, "TOKEN"), "TOKEN", async (_, value) => {
      assert.equal(value, "test-sensitive-value");
      return "${secret:keychain:TOKEN}";
    });
    assert.doesNotMatch(JSON.stringify(next), /test-sensitive-value/);
    assert.match(JSON.stringify(next), /secret:keychain:TOKEN/);
  }
  const draft = makeDraft({ name: "Call", protocol: "rest", request: {}, vars: { token: "test-sensitive-value" } });
  const next = await storeDraftInKeychain(draft, finding(["vars", "token"], "TOKEN"), "TOKEN", async () => "${secret:keychain:TOKEN}");
  assert.equal(next.step.vars.token, "${secret:keychain:TOKEN}");
});

test("keychain extraction refuses ambiguous or stale fields and preserves exact numeric credentials", async () => {
  const { keychainValue, storeDraftInKeychain, makeDraft } = await load();
  const detected = finding(["request", "body", "password"], "PASSWORD");
  const body = text => ({ ...makeDraft({ name: "Call", protocol: "rest", request: { body: text } }), bodyText: text });
  assert.equal(keychainValue(body('{"password":9007199254740993}'), detected), "9007199254740993");
  assert.equal(keychainValue(body('{"password":"same","password":"same"}'), detected), "same");
  for (const text of ['{"password":"one","password":"two"}', '{', '{}', '{"password":{}}', '{"password":"${env:TOKEN}suffix"}']) {
    let writes = 0;
    await assert.rejects(() => storeDraftInKeychain(body(text), detected, "PASSWORD", async () => { writes++; return ""; }));
    assert.equal(writes, 0);
  }
  assert.throws(() => keychainValue(body(""), { ...detected, inText: true }), /by hand/);
});

test("Authorization credentials beginning with a scheme need a separator to split", async () => {
  const { makeDraft, keychainValue, secretReplacement } = await load();
  for (const value of ["token123", "BearerABC", "BasicABC", "api-keyABC"]) {
    const draft = makeDraft(step({ headers: { Authorization: value } }));
    const detected = { ...finding(["request", "headers", "Authorization"], "TOKEN"), key: "Authorization" };
    assert.equal(keychainValue(draft, detected), value);
    assert.equal(secretReplacement("Authorization", value, "TOKEN", "keychain"), "${secret:keychain:TOKEN}");
  }
  assert.equal(secretReplacement("Authorization", "Bearer\t credential", "TOKEN"), "Bearer\t ${env:TOKEN}");
});

test("existing keychain items require confirmation before any store", async () => {
  const { makeDraft, storeDraftInKeychain, KeychainReplacementRequired } = await load();
  const draft = makeDraft(step({ headers: { Authorization: "Bearer original-secret" } }));
  const detected = { ...finding(["request", "headers", "Authorization"], "TOKEN"), key: "Authorization" };
  let writes = 0;
  const store = async (name, value) => {
    writes++;
    assert.equal(name, "project/TOKEN");
    assert.equal(value, "original-secret");
    return "${secret:keychain:project/TOKEN}";
  };
  const exists = async name => { assert.equal(name, "project/TOKEN"); return true; };
  await assert.rejects(() => storeDraftInKeychain(draft, detected, "project/TOKEN", store, { exists }), error => {
    assert.ok(error instanceof KeychainReplacementRequired);
    assert.equal(error.message, 'Replace keychain item "project/TOKEN"?');
    return true;
  });
  assert.equal(writes, 0); // Cancel simply leaves this draft and store untouched.
  assert.equal(draft.step.request.headers.Authorization, "Bearer original-secret");
  const next = await storeDraftInKeychain(draft, detected, "project/TOKEN", store, { exists, confirmed: true });
  assert.equal(writes, 1);
  assert.equal(next.step.request.headers.Authorization, "Bearer ${secret:keychain:project/TOKEN}");
  await assert.rejects(() => storeDraftInKeychain(draft, detected, "project/TOKEN", store, { exists: async () => { throw new Error("store unavailable"); } }), /store unavailable/);
  assert.equal(writes, 1);
  await storeDraftInKeychain(draft, detected, "project/TOKEN", store, { exists: async () => false });
  assert.equal(writes, 2);
});
