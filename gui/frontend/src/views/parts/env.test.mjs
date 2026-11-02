// Run with: node --test src/views/parts/env.test.mjs
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
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 },
  });
  const module = { exports: {} };
  new Function("module", "exports", "require", outputText)(module, module.exports, require);
  return module.exports;
}

const {
  suggestEnvVar,
  envRefName,
  toEnvRef,
  storedEnvRef,
  refusesLiteral,
  looksLikeCredential,
  isSecretName,
  credentialMessage,
  withVariable,
  withoutVariable,
} = loadTypeScript("env.ts");

test("suggestEnvVar turns a variable key into upper snake case", () => {
  const cases = [
    ["password", "PASSWORD"],
    ["apiKey", "API_KEY"],
    ["api-key", "API_KEY"],
    ["api_key", "API_KEY"],
    ["BASE_URL", "BASE_URL"],
    ["base url", "BASE_URL"],
    ["  api key  ", "API_KEY"],
    ["URLValue", "URL_VALUE"],
    ["HTTPResponse", "HTTP_RESPONSE"],
    ["x-api-key", "X_API_KEY"],
    ["foo2Bar", "FOO2_BAR"],
    ["404", "404"],
    ["", "VAR"],
    ["***", "VAR"],
  ];
  for (const [key, want] of cases) assert.equal(suggestEnvVar(key), want, JSON.stringify(key));
});

test("env refs parse and round-trip without touching a typed secret", () => {
  assert.equal(toEnvRef("API_TOKEN"), "${env:API_TOKEN}");
  assert.equal(envRefName("  ${env:API_TOKEN}  "), "API_TOKEN");
  assert.equal(envRefName("${env:API_TOKEN}/x"), undefined);
  assert.equal(envRefName("literal"), undefined);
  assert.equal(storedEnvRef("password", "literal"), "${env:PASSWORD}");
  assert.equal(storedEnvRef("token", "${env:CUSTOM}"), "${env:CUSTOM}");
  assert.equal(storedEnvRef("api-key", ""), "${env:API_KEY}");
});

test("refusesLiteral matches values SaveEnvironment will not write", () => {
  const pem = "-----BEGIN PRIVATE KEY-----\nabc";
  const jwt = "eyJaaaa.bbbb.cccc";
  const token = "a".repeat(24);
  const cases = [
    ["BASE_URL", "/dev", false],
    ["BASE_URL", "", false],
    ["BASE_URL", "literal", false],
    ["BASE_URL", token, true],
    ["BASE_URL", `prefix ${pem}`, true],
    ["note", jwt, true],
    ["note", "hello ${env:TOKEN}", false],
    ["API_TOKEN", "literal-value", true],
    ["password", "x", true],
    ["password", " ", false],
    ["password", "hunter2${x}", true],
    ["credential", "hunter2", true],
    ["password", "", false],
    ["password", "${env:PASSWORD}", false],
    ["my_api-key", "short", true],
    ["tokenizer", "short", true],
    ["BASE_URL", "${env:X}" + token, true], // literal text around a reference is still stored
  ];
  for (const [key, value, want] of cases) {
    assert.equal(refusesLiteral(key, value), want, `${key}=${JSON.stringify(value)}`);
  }
  assert.equal(looksLikeCredential("  " + token + "  "), true);
  assert.equal(looksLikeCredential(""), false);
  assert.equal(isSecretName("BASE_URL"), false);
  assert.equal(isSecretName("private_key_path"), true);
  assert.equal(
    credentialMessage("password"),
    "password looks like a credential; store it as ${env:NAME} and set NAME on this machine",
  );
  assert.equal(
    credentialMessage("password", ["staging", "prod"]),
    "password looks like a credential; store it as ${env:NAME} and set NAME on this machine (staging, prod)",
  );
});

test("variable maps are copied when a key is set or removed", () => {
  const original = { BASE_URL: "/dev", TOKEN: "${env:TOKEN}" };
  const added = withVariable(original, "REGION", "eu");
  assert.deepEqual(added, { BASE_URL: "/dev", TOKEN: "${env:TOKEN}", REGION: "eu" });
  assert.deepEqual(original, { BASE_URL: "/dev", TOKEN: "${env:TOKEN}" });
  assert.deepEqual(withVariable(undefined, "A", ""), { A: "" });
  const removed = withoutVariable(original, "TOKEN");
  assert.deepEqual(removed, { BASE_URL: "/dev" });
  assert.equal(original.TOKEN, "${env:TOKEN}");
  assert.deepEqual(withoutVariable(undefined, "MISSING"), {});
});

test("environments view offers editing controls and drops the read-only note", async () => {
  const output = await build({
    stdin: {
      contents: `export { EnvironmentsView } from "../EnvironmentsView";`,
      resolveDir: directory,
      loader: "tsx",
    },
    bundle: true,
    platform: "node",
    format: "cjs",
    write: false,
    external: ["react", "react-dom"],
    loader: { ".css": "empty" },
    define: { "import.meta.env.DEV": "false" },
  });
  const module = { exports: {} };
  new Function("module", "exports", "require", output.outputFiles[0].text)(module, module.exports, require);
  const info = {
    path: "project.yaml",
    project: {
      schemaVersion: "1.0",
      name: "Collection",
      environments: [
        { name: "dev", default: true, variables: { BASE_URL: "/dev", TOKEN: "${env:API_TOKEN}" } },
        { name: "prod", variables: { BASE_URL: "/prod" } },
      ],
    },
    envRefs: [{ name: "API_TOKEN", set: false, usedIn: ["environment dev"] }],
    undefinedVars: [],
    captures: [],
  };
  const html = renderToStaticMarkup(createElement(module.exports.EnvironmentsView, {
    info,
    environment: "dev",
    onSelectEnvironment: () => {},
    onChanged: () => {},
  }));
  assert.doesNotMatch(html, /Read-only/);
  assert.match(html, /\+ Environment/);
  assert.match(html, /\+ Variable/);
  assert.equal((html.match(/Make default/g) ?? []).length, 1);
  assert.match(html, /· default/);
  assert.match(html, /· active/);
  const locks = [...html.matchAll(/<button\b[^>]*aria-label="Store as environment variable"[^>]*>/g)].map((match) => match[0]);
  assert.ok(locks.some((button) => button.includes('aria-pressed="true"')));
  assert.ok(locks.some((button) => button.includes('aria-pressed="false"')));
  assert.match(html, /class="secret-unset">not set/);
  assert.match(html, /\$\{env:API_TOKEN\}/);
  assert.match(html, />\/dev</);
});
