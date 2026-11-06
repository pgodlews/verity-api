import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { build } from "esbuild";
import { createElement } from "react";
import { act, create } from "react-test-renderer";

const require = createRequire(import.meta.url);
const result = await build({
  stdin: { contents: `export { App } from '../../App'; export { LoadView } from '../LoadView'; export { api } from '../../api';`, resolveDir: dirname(fileURLToPath(import.meta.url)), loader: "tsx" },
  bundle: true, platform: "node", format: "cjs", write: false,
  external: ["react", "react-dom", "react/jsx-runtime"],
  loader: { ".css": "empty", ".json": "json" }, define: { "import.meta.env.DEV": "false" },
});
const mod = { exports: {} };
new Function("module", "exports", "require", result.outputFiles[0].text)(mod, mod.exports, require);
const { App, LoadView, api } = mod.exports;
const previous = {
  project: "demo", vus: 1, iterations: 1, durationMs: 1, passed: true,
  startedAt: "2026-01-01T00:00:00Z", finishedAt: "2026-01-01T00:00:01Z",
  summary: { requests: 1, assertionFailures: 0, transportErrors: 0, otherErrors: 0, rps: 1, errorRate: 0, latency: { minMs: 1, meanMs: 1, p50Ms: 1, p90Ms: 1, p95Ms: 1, p99Ms: 1, maxMs: 1 }, statusCodes: [], errors: [] },
  steps: [], thresholds: [], targets: [],
};
const textOf = (node) => node.children.map((child) => typeof child === "string" ? child : textOf(child)).join("");
const button = (root, text) => root.findAllByType("button").find((node) => textOf(node) === text);

for (const scenario of ["virtual users", "threshold", "target", "backend", "replacement", "interrupted"]) {
  test(`load start preserves the previous report: ${scenario}`, async () => {
    const originalWindow = globalThis.window;
    const originalDocument = globalThis.document;
    globalThis.window = { addEventListener() {}, removeEventListener() {}, setTimeout, clearTimeout };
    globalThis.document = { documentElement: { dataset: {} } };
    let renderer;
    let calls = 0;
    let resolveRun;
    const info = { path: "project.yaml", project: { name: "demo", suites: [] }, envRefs: [], undefinedVars: [], captures: [] };
    Object.assign(api, {
      inspect: async () => info,
      gitStatus: async () => ({ available: true, repo: false, files: [] }),
      gitChanges: async () => ({ steps: [], environments: [], auth: [], suites: [], cases: [] }),
      gitBranches: async () => [], gitStashes: async () => [], cancelGit: async () => {},
      loadTargets: async () => ({ hosts: [], refused: scenario === "target" ? ["remote.example"] : [] }),
      startLoad: async () => { calls++; return previous; },
    });
    try {
      await act(async () => { renderer = create(createElement(App)); });
      await act(async () => renderer.root.findByProps({ placeholder: "project.yaml" }).props.onChange({ target: { value: info.path } }));
      await act(async () => renderer.root.findByType("form").props.onSubmit({ preventDefault() {} }));
      await act(async () => renderer.root.findByProps({ "aria-label": "Load test" }).props.onClick());
      // Establish a real report in App's state before exercising another start.
      await act(async () => renderer.root.findByType(LoadView).props.onStart({ path: info.path, vus: 1, iterations: 1 }));
      assert.equal(renderer.root.findByType(LoadView).props.report, previous);
      calls = 0;
      api.startLoad = async () => {
        calls++;
        if (scenario === "backend") throw new Error("Start rejected");
        return new Promise((resolve) => { resolveRun = resolve; });
      };
      if (scenario === "virtual users") {
        await act(async () => renderer.root.findByProps({ "aria-label": "Virtual users" }).props.onChange({ target: { value: "0" } }));
      } else if (scenario === "threshold") {
        const input = renderer.root.findAllByType("input").find((node) => node.props.value === "500");
        await act(async () => input.props.onChange({ target: { value: "invalid" } }));
      }
      await act(async () => button(renderer.root, "Start load test").props.onClick());
      assert.equal(renderer.root.findByType(LoadView).props.report, previous);
      if (["replacement", "interrupted"].includes(scenario)) {
        assert.equal(calls, 1);
        assert.equal(button(renderer.root, "Save JSON report"), undefined);
        const next = { ...previous, iterations: 2, interrupted: scenario === "interrupted" };
        await act(async () => resolveRun(next));
        assert.equal(renderer.root.findByType(LoadView).props.report, next);
      } else {
        assert.equal(calls, scenario === "backend" ? 1 : 0);
        assert.ok(button(renderer.root, "Save JSON report"));
        if (scenario === "target") assert.equal(button(renderer.root, "Start load test").props.disabled, true);
      }
    } finally {
      if (renderer) await act(async () => renderer.unmount());
      globalThis.window = originalWindow;
      globalThis.document = originalDocument;
    }
  });
}
