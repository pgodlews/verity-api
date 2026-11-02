// Run with: node --test src/lib/authFields.test.mjs
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
  new Function("module", "exports", "require", outputText)(module, module.exports, require);
  return module.exports;
}
const { authConfigRows } = loadTypeScript("authFields.ts");

test("aws_sigv4 and hmac secret fields are masked, references stay visible", () => {
  const aws = authConfigRows("aws_sigv4", {
    service: "s3",
    secretAccessKey: "literal-secret-value",
    accessKeyId: "${env:AWS_ACCESS_KEY_ID}",
    region: "us-east-1",
    sessionToken: "${env:AWS_SESSION_TOKEN}",
    unsignedPayload: true,
  });
  assert.deepEqual(aws.map((row) => row.key), ["accessKeyId", "secretAccessKey", "sessionToken", "region", "service", "unsignedPayload"]);
  const secret = aws.find((row) => row.key === "secretAccessKey");
  assert.equal(secret.masked, true);
  assert.equal(secret.text, "");
  assert.equal(aws.find((row) => row.key === "sessionToken").masked, false);
  assert.equal(aws.find((row) => row.key === "unsignedPayload").text, "true");

  const mac = authConfigRows("hmac", { algorithm: "sha256", secret: "${env:HMAC_SECRET}", header: "X-Signature" });
  assert.equal(mac[0].key, "secret");
  assert.equal(mac[0].masked, false);
  assert.equal(mac[0].text, "${env:HMAC_SECRET}");
});

test("apikey value is masked and a mixed reference is not shown", () => {
  const rows = authConfigRows("apikey", { name: "X-Key", value: "pre-${env:K}" });
  assert.equal(rows.find((row) => row.key === "value").masked, true);
  assert.equal(rows.find((row) => row.key === "name").masked, false);
});
