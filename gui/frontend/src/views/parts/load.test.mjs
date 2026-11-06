// Run with: node --test src/views/parts/load.test.mjs
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
  parseThreshold,
  formatThreshold,
  parseErrorRate,
  validateLoadProfile,
  MAX_LOAD_TIME_MS,
  vuWarning,
  refusalMessage,
  isPrivateHost,
  collectSelectedSteps,
  collectProjectTargets,
  loadCliCommand,
  formatReportJson,
} = loadTypeScript("load.ts");

test("parseThreshold and formatThreshold round-trip overall and step thresholds", () => {
  const overall = formatThreshold({ step: "", metric: "p95", op: "<", value: "500", unit: "ms" });
  assert.equal(overall, "p95<500ms");
  assert.deepEqual(parseThreshold(overall), {
    expr: "p95<500ms",
    step: "",
    metric: "p95",
    op: "<",
    value: 500,
    unit: "ms",
    limitMs: 500,
  });

  const stepBound = formatThreshold({ step: "GET widget", metric: "p99", op: "<=", value: "1.5", unit: "s" });
  assert.equal(stepBound, "GET widget:p99<=1.5s");
  assert.deepEqual(parseThreshold("  GET widget:P99 <= 1.5s "), {
    expr: "GET widget:P99 <= 1.5s",
    step: "GET widget",
    metric: "p99",
    op: "<=",
    value: 1.5,
    unit: "s",
    limitMs: 1500,
  });

  for (const m of ["p50", "p90", "p95", "p99", "max", "mean"]) {
    assert.ok(parseThreshold(`${m}<10ms`), `metric ${m} should parse`);
  }

  assert.equal(parseThreshold("p75<10ms"), null);
  assert.equal(parseThreshold("p95>10ms"), null);
  assert.equal(parseThreshold("p95<10"), null);
  assert.equal(formatThreshold({ step: "", metric: "p95", op: "<", value: "", unit: "ms" }), "");
  assert.equal(formatThreshold({ step: "", metric: "p95", op: "<", value: "abc", unit: "ms" }), "");
});

test("parseErrorRate accepts percentages and fractions and rejects out-of-range values", () => {
  assert.equal(parseErrorRate(""), undefined);
  assert.equal(parseErrorRate("   "), undefined);
  assert.equal(parseErrorRate("1%"), 0.01);
  assert.equal(parseErrorRate("0%"), 0);
  assert.equal(parseErrorRate("100%"), 1);
  assert.equal(parseErrorRate("0.05"), 0.05);
  assert.equal(parseErrorRate("1e-2"), 0.01);
  assert.equal(parseErrorRate("+1.E-2"), 0.01);
  assert.equal(parseErrorRate(" 1e0 % "), 0.01);
  assert.equal(parseErrorRate("1_0%"), 0.1);
  assert.equal(parseErrorRate("0x1p-2"), 0.25);
  assert.equal(parseErrorRate("0x_1.fp-2"), 0.484375);
  assert.equal(parseErrorRate("-0"), -0);
  assert.equal(parseErrorRate("101%"), null);
  assert.equal(parseErrorRate("-1%"), null);
  assert.equal(parseErrorRate("1.5"), null);
  assert.equal(parseErrorRate("nope"), null);
  for (const invalid of ["1e", "1e999", "Infinity", "NaN", "0x1", "1__0%", "1_%", "1e-2junk"]) {
    assert.equal(parseErrorRate(invalid), null, invalid);
  }
});

const validProfile = {
  vus: "10", mode: "duration", durationSec: "10", iterations: "10", rampUpSec: "0", thinkMs: "0",
};

test("validateLoadProfile preserves requested values and accepts backend boundaries", () => {
  assert.deepEqual(validateLoadProfile(validProfile), {
    options: { vus: 10, durationMs: 10000, rampUpMs: 0, thinkMs: 0 }, errors: {},
  });
  const maximum = validateLoadProfile({
    ...validProfile, vus: "1000", durationSec: "86400", rampUpSec: "86400", thinkMs: "86400000",
  });
  assert.deepEqual(maximum.options, {
    vus: 1000, durationMs: MAX_LOAD_TIME_MS, rampUpMs: MAX_LOAD_TIME_MS, thinkMs: MAX_LOAD_TIME_MS,
  });
  assert.deepEqual(validateLoadProfile({
    ...validProfile, vus: "1", mode: "iterations", iterations: String(Number.MAX_SAFE_INTEGER), durationSec: "invalid",
  }).options, { vus: 1, iterations: Number.MAX_SAFE_INTEGER, rampUpMs: 0, thinkMs: 0 });
  assert.equal(validateLoadProfile({ ...validProfile, durationSec: "0.001" }).options.durationMs, 1);
  assert.equal(validateLoadProfile({ ...validProfile, durationSec: "1.001" }).options.durationMs, 1001);
  assert.equal(validateLoadProfile({ ...validProfile, rampUpSec: "0.001", thinkMs: "1" }).options.rampUpMs, 1);
});

test("validateLoadProfile rejects invalid raw fields without clamping, flooring, or dropping them", () => {
  const cases = {
    vus: ["", " ", "0", "-1", "1001", "1.9", "NaN", "Infinity", "1e999", "9007199254740992"],
    iterations: ["", "0", "-1", "1.9", "NaN", "Infinity", "1e999", "9007199254740992"],
    durationSec: ["", "0", "-1", "NaN", "Infinity", "1e999", "86400.001", "0.0001", "1e-20"],
    rampUpSec: ["", "-1", "NaN", "Infinity", "1e999", "86400.001", "0.0001", "1e-20"],
    thinkMs: ["", "-1", "NaN", "Infinity", "1e999", "86400001", "0.9", "1e-20"],
  };
  for (const [field, values] of Object.entries(cases)) {
    for (const value of values) {
      const draft = { ...validProfile, ...(field === "iterations" ? { mode: "iterations" } : {}), [field]: value };
      const result = validateLoadProfile(draft);
      assert.equal(result.options, null, `${field}=${value} must not produce runnable options`);
      assert.ok(result.errors[field], `${field}=${value} must have an inline error`);
      assert.equal(draft[field], value, "raw input must be preserved");
    }
  }
  const invalid = validateLoadProfile({ ...validProfile, vus: "1001", rampUpSec: "-1", thinkMs: "-1" });
  assert.deepEqual(Object.keys(invalid.errors), ["vus", "rampUpSec", "thinkMs"]);
  assert.match(validateLoadProfile({ ...validProfile, thinkMs: "86400001" }).errors.thinkMs, /24 hours/);
});

test("isPrivateHost and refusalMessage match the CLI private-target rules", () => {
  assert.equal(isPrivateHost("127.0.0.1"), true);
  assert.equal(isPrivateHost("::1"), true);
  assert.equal(isPrivateHost("localhost"), true);
  assert.equal(isPrivateHost("10.0.0.1"), true);
  assert.equal(isPrivateHost("172.16.5.4"), true);
  assert.equal(isPrivateHost("172.31.255.1"), true);
  assert.equal(isPrivateHost("192.168.1.10"), true);
  assert.equal(isPrivateHost("169.254.1.1"), true);
  assert.equal(isPrivateHost("fe80::1"), true);

  assert.equal(isPrivateHost("172.32.0.1"), false);
  assert.equal(isPrivateHost("203.0.113.10"), false);
  assert.equal(isPrivateHost("staging.widgets.example"), false);
  assert.equal(isPrivateHost("${baseUrl}"), false);

  assert.equal(
    refusalMessage(["203.0.113.10"]),
    "refusing non-private target 203.0.113.10; pass --allow-remote to run it",
  );
  assert.equal(refusalMessage([]), "");
  assert.match(vuWarning(201), /warning: --vus=201 is above 200/);
  assert.equal(vuWarning(200), "");
});

test("collectSelectedSteps and collectProjectTargets filter by suite, case, and environment", () => {
  const project = {
    schemaVersion: "1.0",
    name: "Demo",
    environments: [
      { name: "Local", variables: { baseUrl: "http://127.0.0.1:8099" } },
      { name: "Staging", default: true, variables: { baseUrl: "https://staging.widgets.example" } },
    ],
    suites: [
      {
        name: "Smoke",
        cases: [
          {
            name: "health",
            steps: [
              { name: "Health", protocol: "rest", request: { url: "${baseUrl}/health" } },
              { name: "Skipped", protocol: "rest", skip: true, request: { url: "http://203.0.113.99/skip" } },
            ],
          },
        ],
      },
      {
        name: "Widgets",
        cases: [
          {
            name: "get",
            steps: [{ name: "Get widget", protocol: "rest", request: { url: "${baseUrl}/widgets/1" } }],
          },
        ],
      },
    ],
  };

  assert.deepEqual(collectSelectedSteps(project), ["Health", "Get widget"]);
  assert.deepEqual(collectSelectedSteps(project, ["Smoke"]), ["Health"]);

  const localTargets = collectProjectTargets(project, "Local");
  assert.deepEqual(localTargets, {
    hosts: ["127.0.0.1"],
    refused: [],
    message: undefined,
  });

  const stagingTargets = collectProjectTargets(project, "Staging");
  assert.deepEqual(stagingTargets, {
    hosts: ["staging.widgets.example"],
    refused: ["staging.widgets.example"],
    message: "refusing non-private target staging.widgets.example; pass --allow-remote to run it",
  });
});

test("loadCliCommand and formatReportJson format CLI flags and 3-decimal millisecond fields", () => {
  const cmd = loadCliCommand("/tmp/project.yaml", {
    path: "/tmp/project.yaml",
    environment: "local",
    suites: ["Smoke"],
    vus: 20,
    durationMs: 30000,
    rampUpMs: 5000,
    thinkMs: 50,
    thresholds: ["p95<500ms", "GET widget:p95<300ms"],
    maxErrorRate: "1%",
  });
  assert.equal(
    cmd,
    "verity load project.yaml --env local --suite Smoke --vus 20 --duration 30s --ramp-up 5s --think 50ms --threshold 'p95<500ms' --threshold 'GET widget:p95<300ms' --max-error-rate '1%'",
  );

  const json = formatReportJson({
    schemaVersion: "1",
    project: "widgets",
    environment: "local",
    startedAt: "2026-01-01T00:00:00Z",
    finishedAt: "2026-01-01T00:00:10Z",
    durationMs: 10000,
    interrupted: false,
    vus: 5,
    iterations: 50,
    targets: ["127.0.0.1"],
    summary: {
      requests: 50,
      assertionFailures: 0,
      transportErrors: 0,
      errorRate: 0,
      rps: 5,
      latency: { minMs: 4.2, meanMs: 12.4, p50Ms: 11.2, p90Ms: 20.1, p95Ms: 28.5, p99Ms: 40.8, maxMs: 55 },
      statusCodes: [{ code: 200, count: 50 }],
      errors: [],
    },
    steps: [],
    thresholds: [{ expr: "p95<500ms", passed: true, metric: "p95", actualMs: 28.5, limitMs: 500 }],
    passed: true,
  });
  assert.match(json, /"minMs": 4\.200/);
  assert.match(json, /"actualMs": 28\.500/);
  assert.match(json, /"limitMs": 500\.000/);
});

let bundleCache;
async function loadBundle() {
  if (bundleCache) return bundleCache;
  const result = await build({
    stdin: {
      contents: `
        export { LoadView } from '../LoadView';
        export { mockBindings } from '../../dev/mock';
        export { subscribeLoadLive } from '../../runBus';
      `,
      resolveDir: directory,
      loader: "tsx",
    },
    bundle: true,
    platform: "node",
    format: "cjs",
    write: false,
    external: ["react", "react-dom", "react-dom/server", "react/jsx-runtime"],
    loader: { ".css": "empty", ".json": "json" },
    define: { "import.meta.env.DEV": "false" },
  });
  const code = result.outputFiles[0].text;
  const mod = { exports: {} };
  new Function("module", "exports", "require", code)(mod, mod.exports, require);
  bundleCache = mod.exports;
  return bundleCache;
}

test("LoadView renders target refusal, live stats with aria-live, and final per-step report", async () => {
  const { LoadView } = await loadBundle();
  const info = {
    path: "/tmp/project.yaml",
    project: {
      schemaVersion: "1.0",
      name: "Widget Shop",
      environments: [
        { name: "Staging", default: true, variables: { baseUrl: "https://staging.widgets.example" } },
      ],
      suites: [
        {
          name: "Smoke",
          cases: [
            {
              name: "list",
              steps: [{ name: "GET widget", protocol: "rest", request: { url: "${baseUrl}/widgets" } }],
            },
          ],
        },
      ],
    },
    envRefs: [],
    undefinedVars: [],
    captures: [],
  };

  // 1. Pre-run view with non-private target refusal and explicit Allow remote targets warning
  const initialHtml = renderToStaticMarkup(
    createElement(LoadView, {
      info,
      environment: "Staging",
      report: null,
      live: null,
      running: false,
      onStart: async () => ({}),
      onCancel: () => {},
    }),
  );
  assert.match(initialHtml, /refusing non-private target staging\.widgets\.example; pass --allow-remote to run it/);
  assert.match(initialHtml, /Allow remote targets \(--allow-remote\)/);
  assert.match(initialHtml, /never remembered between runs/);
  assert.match(initialHtml, /GET widget/);

  // 2. Running view with live VUs, RPS, p95, errors and aria-live="polite"
  const liveHtml = renderToStaticMarkup(
    createElement(LoadView, {
      info,
      environment: "Staging",
      report: null,
      live: { vus: 12, rps: 48.5, p95Ms: 31.2, errors: 2, requests: 145, elapsedMs: 3000 },
      running: true,
      onStart: async () => ({}),
      onCancel: () => {},
    }),
  );
  assert.match(liveHtml, /aria-live="polite"/);
  assert.match(liveHtml, /vus=12 rps=48\.5 p95=31\.2ms errors=2/);
  assert.match(liveHtml, /Cancel/);

  // 3. Completed report with per-step table, threshold pass/fail, and Save JSON report button
  const doneProps = {
    info,
    environment: "Staging",
    running: false,
    live: null,
    onStart: async () => ({}),
    onCancel: () => {},
    report: {
      schemaVersion: "1",
      project: "Widget Shop",
      environment: "Staging",
      startedAt: "2026-01-01T00:00:00Z",
      finishedAt: "2026-01-01T00:00:10Z",
      durationMs: 10000,
      interrupted: false,
      vus: 10,
      iterations: 100,
      targets: ["127.0.0.1"],
      summary: {
        requests: 100,
        assertionFailures: 1,
        transportErrors: 0,
        errorRate: 0.01,
        rps: 10.0,
        latency: { minMs: 2.5, meanMs: 11.2, p50Ms: 10.0, p90Ms: 18.4, p95Ms: 22.1, p99Ms: 30.0, maxMs: 41.5 },
        statusCodes: [{ code: 200, count: 100 }],
        errors: ["GET widget: expected 200 == 500"],
      },
      steps: [
        {
          name: "GET widget",
          requests: 100,
          assertionFailures: 1,
          transportErrors: 0,
          errorRate: 0.01,
          rps: 10.0,
          latency: { minMs: 2.5, meanMs: 11.2, p50Ms: 10.0, p90Ms: 18.4, p95Ms: 22.1, p99Ms: 30.0, maxMs: 41.5 },
          statusCodes: [{ code: 200, count: 100 }],
          errors: ["GET widget: expected 200 == 500"],
        },
      ],
      thresholds: [
        { expr: "GET widget:p95<300ms", passed: true, step: "GET widget", metric: "p95", actualMs: 22.1, limitMs: 300 },
      ],
      errorRateThreshold: { max: 0.05, actual: 0.01, passed: true },
      passed: true,
    },
  };
  const doneHtml = renderToStaticMarkup(createElement(LoadView, doneProps));
  assert.match(doneHtml, /Save JSON report/);
  assert.match(doneHtml, /GET widget:p95&lt;300ms/);
  assert.match(doneHtml, /22\.10 ms/);
  assert.match(doneHtml, /PASSED/);
  assert.doesNotMatch(doneHtml, /No max error rate was set/);
  assert.match(doneHtml, /class="load-table-scroll"/);

  const latencyOnlyReport = {
    ...doneProps.report,
    errorRateThreshold: undefined,
    summary: { ...doneProps.report.summary, assertionFailures: 20, transportErrors: 3, otherErrors: 2, errorRate: 0.25 },
  };
  const warningHtml = renderToStaticMarkup(createElement(LoadView, { ...doneProps, report: latencyOnlyReport }));
  assert.match(warningHtml, /Thresholds passed/);
  assert.match(warningHtml, /25 request failures\/errors/);
  assert.match(warningHtml, /20 assertion failures, 5 errors/);
  assert.match(warningHtml, /No max error rate was set/);
  assert.doesNotMatch(warningHtml, />PASSED</);

  // A retained report prop must never expose the previous table during a new run.
  const rerunningHtml = renderToStaticMarkup(createElement(LoadView, { ...doneProps, running: true }));
  assert.match(rerunningHtml, /Load test in progress/);
  assert.doesNotMatch(rerunningHtml, /Per-step latency &amp; throughput/);
  assert.doesNotMatch(rerunningHtml, /Save JSON report/);
});

test("mockBindings.StartLoad streams live stats, enforces private targets, and supports CancelLoad", async () => {
  const { mockBindings, subscribeLoadLive } = await loadBundle();

  // Non-private Staging environment is refused without allowRemote
  await assert.rejects(
    () =>
      mockBindings.StartLoad({
        path: "/home/user/widget-shop/project.yaml",
        environment: "Staging",
        vus: 2,
        iterations: 2,
      }),
    /refusing non-private target staging\.widgets\.example; pass --allow-remote to run it/,
  );

  // Local environment runs and emits live stats
  const liveSamples = [];
  const off = subscribeLoadLive((st) => liveSamples.push(st));
  try {
    const rep = await mockBindings.StartLoad({
      path: "/home/user/widget-shop/project.yaml",
      environment: "Local",
      suites: ["Widget Shop"],
      vus: 2,
      iterations: 2,
      thresholds: ["p95<500ms", "Health:p95<500ms"],
      maxErrorRate: "10%",
      liveEveryMs: 10,
    });
    assert.equal(rep.passed, true);
    assert.equal(rep.interrupted, false);
    assert.ok(rep.steps.some((s) => s.name === "Health"));
    assert.ok(liveSamples.length > 0);
  } finally {
    off();
  }

  // CancelLoad interrupts an in-flight run and returns a partial report
  let cancelledOnce = false;
  const offCancel = subscribeLoadLive(() => {
    if (!cancelledOnce) {
      cancelledOnce = true;
      mockBindings.CancelLoad();
    }
  });
  try {
    const partial = await mockBindings.StartLoad({
      path: "/home/user/widget-shop/project.yaml",
      environment: "Local",
      suites: ["Widget Shop"],
      vus: 4,
      durationMs: 5000,
      liveEveryMs: 10,
    });
    assert.equal(partial.interrupted, true);
  } finally {
    offCancel();
  }
});
