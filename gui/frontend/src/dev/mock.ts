// Dev-only mock bindings, used when the page URL has ?mock (see api.ts).
// They return realistic data for the Widget Shop sample — the project is the
// real output of `verity import postman` on examples/specs — so views can be
// built and screenshotted without the Go backend. Never used in builds that
// run inside the desktop app.

import { emitGitProgress } from "../gitBus";
import { emitLocalLoadLive, emitLocalRunEvent } from "../runBus";
import projectJSON from "./widget-shop.project.json";
import { mockGraphQLTemplates } from "../lib/graphqlInsert";
import {
  collectProjectTargets,
  parseErrorRate,
  parseThreshold,
  type LoadMetric,
} from "../views/parts/load";
import type {
  GitFile,
  GitStashInfo,
  GitStashRequest,
  GitStashActionRequest,
  ProjectChanges,
  AssertionResult,
  OAuthRequest,
  Catalog,
  CaseResult,
  ImportPreview,
  ImportSaveRequest,
  LoadLatency,
  LoadMetrics,
  LoadOptions,
  LoadReport,
  LoadThresholdResult,
  Project,
  ProjectInfo,
  RunEvent,
  RunOptions,
  RunResult,
  SaveEnvironmentRequest,
  SaveLoadReportRequest,
  SaveStepRequest,
  SecretFinding,
  Status,
  Step,
  StepResult,
  SuiteResult,
} from "../types";

const project: Project = structuredClone(projectJSON) as Project;
for (const suite of project.suites ?? []) {
  for (const testCase of suite.cases) {
    for (const step of testCase.steps) {
      if (step.protocol === "graphql") step.schema = { file: "schema.graphql" };
    }
  }
}
const PATH = "/home/user/widget-shop/project.yaml";

let mockConflict = false;
let mockMerging = false;
/** Bumped by Simulate disk change so the next inspect returns new step and file hashes. */
let mockDiskEpoch = 0;

/** Marks the sample project file as rewritten. InspectProject then hashes every step again. */
export function noteMockDiskChange(): void {
  mockDiskEpoch += 1;
}

async function sha256Hex(text: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function diskMarked(body: string): string {
  return mockDiskEpoch === 0 ? body : `${body}\n${mockDiskEpoch}`;
}

let mockHasUpstream = false;
let mockBranch = "main";
let mockExtraBranches: string[] = [];
let mockGitCancel = false;
let mockFiles: GitFile[] = [
  { path: "project.yaml", state: "modified", projectFile: true },
  { path: "notes.txt", state: "untracked", projectFile: false },
];
let mockStashSerial = 0;
let mockStashes: (GitStashInfo & { files: GitFile[]; changes: ProjectChanges })[] = [];

function findMockStash(id: string) {
  if (!/^[a-f0-9]{40}$/.test(id)) throw new Error("a full stash object id is required");
  const entry = mockStashes.find((stash) => stash.id === id);
  if (!entry) throw new Error("this stash is no longer in the list; refresh stashes and try again");
  return entry;
}

function mockProgress(op: string) {
  mockGitCancel = false;
  emitGitProgress({ op, line: "started" });
  emitGitProgress({ op, line: `${op} (mock)` });
  if (mockGitCancel) throw new Error("Git operation cancelled.");
  emitGitProgress({ op, line: "finished" });
}

export function cancelMockGit() {
  mockGitCancel = true;
  emitGitProgress({ op: "fetch", line: "cancelled" });
}

const delay = (ms: number) => new Promise((r) => setTimeout(r, ms));

function matches(patterns: string[] | undefined, name: string) {
  return !patterns || patterns.length === 0 || patterns.includes(name);
}

// Scripted outcome per step name, so runs look like a real mixed result.
const outcomes: Record<string, { status: Status; ms: number; code?: number; body?: string; fail?: AssertionResult; error?: string }> = {
  Health: { status: "passed", ms: 12, code: 200, body: '{"ok":true}' },
  Login: { status: "passed", ms: 84, code: 200, body: '{"access_token":"••••••••","token_type":"Bearer","expires_in":3600,"user":{"id":7,"name":"ada"}}' },
  Me: { status: "passed", ms: 41, code: 200, body: '{"user":{"name":"ada","roles":["admin"]}}' },
  "Get widget": {
    status: "failed", ms: 58, code: 200, body: '{"id":42,"name":"widget","parts":["bolt","nut"]}',
    fail: { passed: false, type: "jsonpath", description: "$.id equals 43", message: "expected 42 == 43", expected: 43, actual: 42 },
  },
  Search: { status: "passed", ms: 63, code: 200, body: '{"data":{"widgets":[{"id":42}]}}' },
  "Delete widget": { status: "passed", ms: 22, code: 204, body: "" },
  "Partner token": {
    status: "error", ms: 3,
    error: "apply auth: oauth2 token request: Post \"https://staging.widgets.example/oauth/token\": dial tcp: connection refused",
  },
};

function stepResult(step: Step, env: Record<string, string>): StepResult {
  const o = outcomes[step.name] ?? { status: "passed" as Status, ms: 30, code: 200, body: "{}" };
  if (step.skip) return { name: step.name, protocol: step.protocol, status: "skipped", durationMs: 0 };
  const req = step.request ?? {};
  const url = String(req.url ?? "").replace(/\$\{([^}:]+)\}/g, (m, k) => env[k] ?? m);
  const res: StepResult = {
    name: step.name,
    protocol: step.protocol,
    status: o.status,
    durationMs: o.ms,
    request: {
      method: String(req.method ?? (step.protocol === "rest" ? "GET" : "POST")),
      url,
      headers: { "User-Agent": "Verity", ...((req.headers as Record<string, string>) ?? {}) },
      body: typeof req.body === "string" ? req.body.replace("${password}", "••••••••") : undefined,
    },
  };
  if (o.error) {
    res.error = { code: "auth_error", message: o.error };
    return res;
  }
  res.response = {
    statusCode: o.code ?? 200,
    status: `${o.code} ${o.code === 204 ? "No Content" : "OK"}`,
    headers: { "Content-Type": "application/json", "Content-Length": String(o.body?.length ?? 0), Date: new Date().toUTCString() },
    body: o.body,
    durationMs: o.ms,
  };
  // The scripted failure replaces the step's matching assertion (same
  // description), as a real run reports it in the assertion's own position.
  res.assertions = (step.assertions ?? []).map((a) =>
    o.fail && a.description === o.fail.description
      ? o.fail
      : { passed: true, type: a.type, description: a.description, expected: a.value }
  );
  res.captures = (step.transfers ?? []).map((t) => ({ name: t.name, value: "••••••••" }));
  return res;
}

function run(opts: RunOptions): RunResult {
  const envName = opts.environment || project.environments?.find((e) => e.default)?.name || "";
  const env = { ...(project.environments?.find((e) => e.name === envName)?.variables ?? {}), ...opts.vars };
  const suites = (project.suites ?? [])
    .filter((s) => matches(opts.suites, s.name))
    .map((s) => {
      const cases = s.cases
        .filter((c) => matches(opts.cases, c.name))
        .map((c) => {
          const steps = (opts.untilStep ? c.steps.slice(0, opts.untilStep) : c.steps).map((st) => stepResult(st, env));
          return { name: c.name, status: rollup(steps.map((x) => x.status)), durationMs: sum(steps), iteration: -1, steps };
        });
      return { name: s.name, status: rollup(cases.map((c) => c.status)), durationMs: sum(cases), cases };
    })
    .filter((s) => s.cases.length > 0);
  const all = suites.flatMap((s) => s.cases.flatMap((c) => c.steps));
  const asserts = all.flatMap((s) => s.assertions ?? []);
  return {
    project: project.name,
    environment: envName,
    startedAt: new Date().toISOString(),
    durationMs: sum(suites),
    suites,
    summary: {
      suites: suites.length,
      cases: suites.reduce((n, s) => n + s.cases.length, 0),
      steps: all.length,
      assertions: asserts.length,
      passed: asserts.filter((a) => a.passed).length,
      failed: asserts.filter((a) => !a.passed).length,
      errors: all.filter((s) => s.status === "error").length,
      skipped: all.filter((s) => s.status === "skipped").length,
      stepsFailed: all.filter((s) => s.status === "failed").length,
      stepsErrored: all.filter((s) => s.status === "error").length,
      stepsSkipped: all.filter((s) => s.status === "skipped").length,
      status: rollup(all.map((s) => s.status)),
    },
  };
}

function rollup(st: Status[]): Status {
  if (st.includes("error")) return "error";
  if (st.includes("failed")) return "failed";
  if (st.includes("aborted")) return "aborted";
  if (st.length > 0 && st.every((s) => s === "skipped")) return "skipped";
  return "passed";
}

function eventsOf(result: RunResult): RunEvent[] {
  const events: RunEvent[] = [];
  for (const suite of result.suites) {
    for (const testCase of suite.cases) {
      events.push({ kind: "case_started", suite: suite.name, case: testCase.name, iteration: testCase.iteration });
      testCase.steps.forEach((step, index) => {
        events.push({
          kind: "step_done",
          suite: suite.name,
          case: testCase.name,
          iteration: testCase.iteration,
          stepIndex: index,
          step,
        });
      });
      events.push({
        kind: "case_done",
        suite: suite.name,
        case: testCase.name,
        iteration: testCase.iteration,
        status: testCase.status,
        durationMs: testCase.durationMs,
      });
    }
  }
  return events;
}

function stepKey(suite: string, testCase: string, iteration: number, index: number) {
  return `${suite}\0${testCase}\0${iteration}\0${index}`;
}

function abortedCopy(step: StepResult): StepResult {
  if (step.status === "skipped") return step;
  return { name: step.name, protocol: step.protocol, status: "aborted", durationMs: 0 };
}

function withUnsentAborted(full: RunResult, sent: Set<string>): RunResult {
  const suites: SuiteResult[] = full.suites.map((suite) => {
    const cases: CaseResult[] = suite.cases.map((testCase) => {
      const steps = testCase.steps.map((step, index) =>
        sent.has(stepKey(suite.name, testCase.name, testCase.iteration, index)) ? step : abortedCopy(step),
      );
      return { ...testCase, steps, status: rollup(steps.map((step) => step.status)), durationMs: sum(steps) };
    });
    return { ...suite, cases, status: rollup(cases.map((testCase) => testCase.status)), durationMs: sum(cases) };
  });
  const steps = suites.flatMap((suite) => suite.cases.flatMap((testCase) => testCase.steps));
  const assertions = steps.flatMap((step) => step.assertions ?? []);
  return {
    ...full,
    durationMs: sum(suites),
    suites,
    summary: {
      suites: suites.length,
      cases: suites.reduce((n, suite) => n + suite.cases.length, 0),
      steps: steps.length,
      assertions: assertions.length,
      passed: assertions.filter((assertion) => assertion.passed).length,
      failed: assertions.filter((assertion) => !assertion.passed).length,
      errors: steps.filter((step) => step.status === "error").length,
      skipped: steps.filter((step) => step.status === "skipped").length,
      stepsFailed: steps.filter((step) => step.status === "failed").length,
      stepsErrored: steps.filter((step) => step.status === "error").length,
      stepsSkipped: steps.filter((step) => step.status === "skipped").length,
      stepsAborted: steps.filter((step) => step.status === "aborted").length,
      aborted: true,
      status: rollup(steps.map((step) => step.status)),
    },
  };
}

let mockRun: { cancelled: boolean } | null = null;
let mockLoad: { cancelled: boolean } | null = null;

/** Stops the in-flight ?mock run. The remaining steps are reported as aborted. */
export function cancelMockRun() {
  if (mockRun) mockRun.cancelled = true;
}

/** Stops the in-flight ?mock load test and returns a partial report. */
export function cancelMockLoad() {
  if (mockLoad) mockLoad.cancelled = true;
}

function metricOfLatency(lat: LoadLatency, metric: LoadMetric): number {
  switch (metric) {
    case "p50":
      return lat.p50Ms;
    case "p90":
      return lat.p90Ms;
    case "p95":
      return lat.p95Ms;
    case "p99":
      return lat.p99Ms;
    case "max":
      return lat.maxMs;
    case "mean":
      return lat.meanMs;
  }
}

async function streamLoad(opts: LoadOptions): Promise<LoadReport> {
  if (opts.vus < 1 || opts.vus > 1000) {
    throw new Error(`config_error: vus must be from 1 to 1000, got ${opts.vus}`);
  }
  const hasDur = (opts.durationMs ?? 0) > 0;
  const hasIter = (opts.iterations ?? 0) > 0;
  if (hasDur === hasIter) {
    throw new Error("config_error: set exactly one of iterations or duration");
  }
  const parsedThresholds = (opts.thresholds ?? []).map((expr) => {
    const p = parseThreshold(expr);
    if (!p) throw new Error(`config_error: invalid threshold "${expr}" (want [step:]p95<500ms)`);
    return p;
  });
  const maxRate = opts.maxErrorRate ? parseErrorRate(opts.maxErrorRate) : undefined;
  if (maxRate === null) {
    throw new Error(`config_error: invalid max error rate "${opts.maxErrorRate}"`);
  }

  const targetCheck = collectProjectTargets(
    project,
    opts.environment,
    opts.suites,
    opts.cases,
    opts.vars,
    !!opts.allowRemote,
  );
  if (targetCheck.refused.length > 0 && !opts.allowRemote) {
    throw new Error(`config_error: ${targetCheck.message}`);
  }

  const selectedCases = (project.suites ?? [])
    .filter((s) => matches(opts.suites, s.name))
    .flatMap((s) => s.cases.filter((c) => matches(opts.cases, c.name)));
  if (selectedCases.length === 0) {
    throw new Error("not_found: no suite/case matches the --suite/--case filters");
  }

  const ctrl = { cancelled: false };
  mockLoad = ctrl;
  const startedAt = new Date();
  const totalTicks = 3;
  const tickMs = opts.liveEveryMs && opts.liveEveryMs > 0 ? opts.liveEveryMs : 250;
  const totalIters = hasIter
    ? opts.vus * (opts.iterations ?? 1)
    : Math.max(opts.vus * 6, Math.round(((opts.durationMs ?? 5000) / 1000) * opts.vus * 4));
  let completedTicks = 0;

  try {
    for (let tick = 1; tick <= totalTicks; tick++) {
      await delay(tickMs);
      if (ctrl.cancelled) break;
      completedTicks = tick;
      const frac = tick / totalTicks;
      const reqs = Math.max(1, Math.round(totalIters * frac));
      emitLocalLoadLive({
        vus: opts.vus,
        rps: Number(((reqs / Math.max(1, tick)) * 2.5).toFixed(1)),
        p95Ms: 58.0,
        errors: Math.floor(reqs * 0.08),
        requests: reqs,
        elapsedMs: tick * (opts.liveEveryMs && opts.liveEveryMs > 0 ? opts.liveEveryMs : 1000),
      });
    }
    const interrupted = ctrl.cancelled;
    const scale = interrupted ? Math.max(0.25, completedTicks / totalTicks) : 1;
    const finalIters = Math.max(1, Math.round(totalIters * scale));
    const durationMs = interrupted
      ? Math.max(100, completedTicks * tickMs)
      : opts.durationMs && opts.durationMs > 0
        ? opts.durationMs
        : Math.max(250, finalIters * 35);
    const windowSec = Math.max(0.1, durationMs / 1000);

    const stepMap = new Map<string, { step: Step; count: number }>();
    for (let i = 0; i < finalIters; i++) {
      const c = selectedCases[i % selectedCases.length];
      for (const st of c.steps) {
        if (st.skip) continue;
        const cur = stepMap.get(st.name);
        if (cur) cur.count += 1;
        else stepMap.set(st.name, { step: st, count: 1 });
      }
    }

    const steps: LoadMetrics[] = [];
    let totalReqs = 0;
    let totalAssertFail = 0;
    let totalTransportErr = 0;
    let totalOtherErr = 0;
    const statusTotals = new Map<number, number>();
    const overallErrors: string[] = [];

    for (const [name, { count }] of stepMap.entries()) {
      const o = outcomes[name] ?? { status: "passed" as Status, ms: 30, code: 200 };
      const baseMs = Math.max(1, o.ms);
      const assertF = o.status === "failed" ? count : 0;
      const otherE = o.status === "error" ? count : 0;
      const transE = 0;
      const errs: string[] = [];
      if (o.fail?.message) errs.push(`${name}: ${o.fail.message}`);
      if (o.error) errs.push(`${name}: ${o.error}`);
      for (const e of errs) {
        if (!overallErrors.includes(e) && overallErrors.length < 10) overallErrors.push(e);
      }
      const code = o.code ?? (o.status === "error" ? 0 : 200);
      if (code > 0) {
        statusTotals.set(code, (statusTotals.get(code) ?? 0) + count);
      }
      totalReqs += count;
      totalAssertFail += assertF;
      totalTransportErr += transE;
      totalOtherErr += otherE;

      steps.push({
        name,
        requests: count,
        assertionFailures: assertF,
        transportErrors: transE,
        ...(otherE > 0 ? { otherErrors: otherE } : {}),
        errorRate: count > 0 ? (assertF + transE + otherE) / count : 0,
        rps: Number((count / windowSec).toFixed(2)),
        latency: {
          minMs: Number((baseMs * 0.7).toFixed(3)),
          meanMs: Number(baseMs.toFixed(3)),
          p50Ms: Number((baseMs * 0.95).toFixed(3)),
          p90Ms: Number((baseMs * 1.15).toFixed(3)),
          p95Ms: Number((baseMs * 1.25).toFixed(3)),
          p99Ms: Number((baseMs * 1.4).toFixed(3)),
          maxMs: Number((baseMs * 1.6).toFixed(3)),
        },
        statusCodes: code > 0 ? [{ code, count }] : [],
        errors: errs,
      });
    }

    const allLatencies = steps.map((s) => s.latency);
    const summaryLatency: LoadLatency =
      allLatencies.length > 0
        ? {
            minMs: Math.min(...allLatencies.map((l) => l.minMs)),
            meanMs: Number((allLatencies.reduce((a, l) => a + l.meanMs, 0) / allLatencies.length).toFixed(3)),
            p50Ms: Number((allLatencies.reduce((a, l) => a + l.p50Ms, 0) / allLatencies.length).toFixed(3)),
            p90Ms: Math.max(...allLatencies.map((l) => l.p90Ms)),
            p95Ms: Math.max(...allLatencies.map((l) => l.p95Ms)),
            p99Ms: Math.max(...allLatencies.map((l) => l.p99Ms)),
            maxMs: Math.max(...allLatencies.map((l) => l.maxMs)),
          }
        : { minMs: 0, meanMs: 0, p50Ms: 0, p90Ms: 0, p95Ms: 0, p99Ms: 0, maxMs: 0 };

    const totalFailures = totalAssertFail + totalTransportErr + totalOtherErr;
    const summary: LoadMetrics = {
      requests: totalReqs,
      assertionFailures: totalAssertFail,
      transportErrors: totalTransportErr,
      ...(totalOtherErr > 0 ? { otherErrors: totalOtherErr } : {}),
      errorRate: totalReqs > 0 ? totalFailures / totalReqs : 0,
      rps: Number((totalReqs / windowSec).toFixed(2)),
      latency: summaryLatency,
      statusCodes: [...statusTotals.entries()]
        .sort((a, b) => a[0] - b[0])
        .map(([code, count]) => ({ code, count })),
      errors: overallErrors,
    };

    const thresholds: LoadThresholdResult[] = parsedThresholds.map((th) => {
      const targetMetrics = th.step ? steps.find((s) => s.name === th.step) : summary;
      if (!targetMetrics || targetMetrics.requests === 0) {
        return {
          expr: th.expr,
          passed: false,
          ...(th.step ? { step: th.step } : {}),
          metric: th.metric,
          actualMs: 0,
          limitMs: th.limitMs,
          message: th.step ? `step "${th.step}" did not run` : "no requests ran",
        };
      }
      const actual = metricOfLatency(targetMetrics.latency, th.metric);
      const passed = th.op === "<=" ? actual <= th.limitMs : actual < th.limitMs;
      return {
        expr: th.expr,
        passed,
        ...(th.step ? { step: th.step } : {}),
        metric: th.metric,
        actualMs: actual,
        limitMs: th.limitMs,
        ...(!passed
          ? { message: `${th.expr} failed: ${th.metric}=${actual.toFixed(2)}ms limit ${th.op} ${th.limitMs.toFixed(2)}ms` }
          : {}),
      };
    });

    const errorRateThreshold =
      maxRate !== undefined
        ? {
            max: maxRate,
            actual: summary.errorRate,
            passed: summary.requests > 0 && summary.errorRate <= maxRate,
            ...(summary.requests === 0 ? { message: "no requests ran" } : {}),
          }
        : undefined;

    const envName = opts.environment || project.environments?.find((e) => e.default)?.name || "";
    const passed =
      !interrupted &&
      thresholds.every((t) => t.passed) &&
      (!errorRateThreshold || errorRateThreshold.passed);

    return {
      schemaVersion: "1",
      project: project.name,
      environment: envName,
      startedAt: startedAt.toISOString(),
      finishedAt: new Date(startedAt.getTime() + durationMs).toISOString(),
      durationMs,
      interrupted,
      vus: opts.vus,
      iterations: finalIters,
      ...(opts.rampUpMs ? { rampUpMs: opts.rampUpMs } : {}),
      ...(opts.thinkMs ? { thinkMs: opts.thinkMs } : {}),
      ...(hasDur ? { configuredDurationMs: opts.durationMs } : { configuredIterations: opts.iterations }),
      targets: targetCheck.hosts,
      summary,
      steps,
      thresholds,
      ...(errorRateThreshold ? { errorRateThreshold } : {}),
      passed,
    };
  } finally {
    if (mockLoad === ctrl) mockLoad = null;
  }
}

async function streamRun(opts: RunOptions): Promise<RunResult> {
  const ctrl = { cancelled: false };
  mockRun = ctrl;
  const full = run(opts);
  const sent = new Set<string>();
  try {
    for (const event of eventsOf(full)) {
      if (ctrl.cancelled) break;
      if (event.kind === "step_done") {
        sent.add(stepKey(event.suite, event.case, event.iteration, event.stepIndex ?? 0));
      }
      emitLocalRunEvent(event);
      await delay(80);
    }
    if (!ctrl.cancelled) return full;
    const partial = withUnsentAborted(full, sent);
    for (const suite of partial.suites) {
      for (const testCase of suite.cases) {
        testCase.steps.forEach((step, index) => {
          if (sent.has(stepKey(suite.name, testCase.name, testCase.iteration, index))) return;
          emitLocalRunEvent({
            kind: "step_done",
            suite: suite.name,
            case: testCase.name,
            iteration: testCase.iteration,
            stepIndex: index,
            step,
          });
        });
        emitLocalRunEvent({
          kind: "case_done",
          suite: suite.name,
          case: testCase.name,
          iteration: testCase.iteration,
          status: testCase.status,
          durationMs: testCase.durationMs,
        });
      }
    }
    return partial;
  } finally {
    if (mockRun === ctrl) mockRun = null;
  }
}

function sum(xs: { durationMs: number }[]) {
  return xs.reduce((n, x) => n + x.durationMs, 0);
}

async function info(): Promise<ProjectInfo> {
  return {
    path: PATH,
    project: structuredClone(project),
    envRefs: [
      { name: "APIKEY", set: true, usedIn: ["environment Local", "environment Staging"] },
      { name: "PASSWORD", set: true, usedIn: ["environment Local", "environment Staging"] },
      { name: "BASIC_PASSWORD", set: true, usedIn: ["auth basic"] },
      { name: "SIGNING_SECRET", set: false, usedIn: ["Widgets/Widgets / Admin/Partner token"] },
    ],
    undefinedVars: [
      { name: "partnerSecret", missingIn: ["Local", "Staging"], usedIn: ["auth oauth2-client-credentials"] },
    ],
    captures: [{ name: "token", from: "jsonpath", expr: "$.access_token", scope: "run", step: "Auth/Auth/Login" }],
    stepFiles: await stepOrigins(),
    fileHashes: [{ path: PATH, hash: await sha256Hex(diskMarked(JSON.stringify(project))) }],
  };
}

// Per-step sha256 of the step JSON, the same idea as workspace.HashStep, plus
// the file hash inspect reports. A simulated disk change moves both.
async function stepOrigins() {
  const out: { suite: string; case: string; index: number; file: string; occurrence: number; hash: string }[] = [];
  for (const suite of project.suites ?? []) {
    for (const testCase of suite.cases) {
      const seen = new Map<string, number>();
      for (let index = 0; index < testCase.steps.length; index++) {
        const step = testCase.steps[index];
        const occurrence = seen.get(step.name) ?? 0;
        seen.set(step.name, occurrence + 1);
        out.push({
          suite: suite.name,
          case: testCase.name,
          index,
          file: PATH,
          occurrence,
          hash: await sha256Hex(diskMarked(JSON.stringify(step))),
        });
      }
    }
  }
  return out;
}

const catalog: Catalog = {
  protocols: ["graphql", "grpc", "rest", "soap"],
  assertions: ["contains", "header", "json_schema", "jsonpath", "regex", "response_time", "status", "xpath", "xsd"],
  reporters: ["console", "html", "json", "junit"],
  auth: ["apikey", "aws_sigv4", "basic", "digest", "bearer", "hmac", "mtls", "none", "oauth2", "oauth2_client_credentials"],
  dataSources: ["csv", "json"],
};

const importPreview: ImportPreview = {
  yaml: "schemaVersion: \"1.0\"\nname: Widget Shop\n# … full project YAML …\n",
  warnings: [
    'collection variable "apiKey": secret value not imported; set environment variable APIKEY',
    'environment Staging variable "password": secret value not imported; set environment variable PASSWORD',
    "Auth/Me: 1 test-script line(s) not converted (first: console.log(body);)",
    "dynamic variable {{$guid}} has no Verity equivalent; left as-is (first seen in Widgets/Get widget)",
    "Widgets/Get widget: pre-request script not converted (1 line(s))",
    'auth "basic": literal password not imported; set environment variable BASIC_PASSWORD',
    "Widgets/Admin/Upload image: formdata body is not supported yet; step imported with skip: true",
    'Widgets/Admin/Partner token: form field "signing_secret" held a literal credential; set environment variable SIGNING_SECRET',
  ],
  stats: { suites: 3, cases: 4, steps: 8, assertions: 14, auth: 2, environments: 1 },
};

const oauthSessions = new Set<string>();
function validateKeychainName(name: string): void {
  if (name.length > 200 || !/^[A-Za-z0-9_][A-Za-z0-9_.-]*(\/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$/.test(name)) {
    throw new Error("config_error: invalid keychain name");
  }
}

// Process-memory only: ?mock never touches the real keychain or localStorage.
const keychainItems = new Map<string, string>();
const oauthKey = (req: OAuthRequest) => JSON.stringify([req.path, req.environment, req.authName]);

export const mockBindings = {
  OAuthLogin: async (req: OAuthRequest) => { await delay(100); oauthSessions.add(oauthKey(req)); },
  OAuthLogout: async (req: OAuthRequest) => { oauthSessions.delete(oauthKey(req)); },
  OAuthStatus: async (req: OAuthRequest) => oauthSessions.has(oauthKey(req)),
  Catalog: async () => catalog,
  ValidateProject: async (path: string, environment = "") => {
    void path;
    void environment;
    return { ok: true, project: project.name, errors: [] };
  },
  GraphQLTemplates: async () => mockGraphQLTemplates,
  InspectProject: async () => { await delay(120); return info(); },
  RunProject: (opts: RunOptions) => streamRun(opts),
  CancelRun: async () => { cancelMockRun(); },
  StartLoad: (opts: LoadOptions) => streamLoad(opts),
  CancelLoad: async () => { cancelMockLoad(); },
  LoadTargets: async (opts: LoadOptions) =>
    collectProjectTargets(project, opts.environment, opts.suites, opts.cases, opts.vars, !!opts.allowRemote),
  SaveLoadReport: async (req: SaveLoadReportRequest) => req.out || "/home/user/widget-shop/load-report.json",
  CheckStep: async (step: Step) => mockCheckStep(step),
  KeychainSecretExists: async (name: string) => {
    validateKeychainName(name);
    return keychainItems.has(name);
  },
  StoreKeychainSecret: async (name: string, value: string) => {
    validateKeychainName(name);
    // Match Go strings.TrimSpace (Unicode White_Space).
    value = value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, "");
    if (!value) throw new Error("config_error: keychain value must not be empty after trimming whitespace");
    keychainItems.set(name, value);
    return "${secret:keychain:" + name + "}";
  },
  SaveStep: async (req: SaveStepRequest) => {
    const found = mockCheckStep(req.step);
    if (found.length) throw new Error(found.map((f) => `${f.path.join(".")} looks like a credential; store it as \${env:${f.envVar}}`).join("; "));
    const c = project.suites?.find((s) => s.name === req.suite)?.cases.find((x) => x.name === req.case);
    if (!c || !c.steps[req.index]) throw new Error(`step ${req.suite}/${req.case}#${req.index} not found`);
    c.steps[req.index] = structuredClone(req.step);
    await delay(80);
    return { file: PATH };
  },
  SaveEnvironment: async (req: SaveEnvironmentRequest) => {
    for (const v of Object.values(req.variables)) {
      if (/^[A-Za-z0-9_-]{24,}$/.test(v)) throw new Error("value looks like a credential; store it as ${env:NAME}");
    }
    const envs = (project.environments ??= []);
    const existing = envs.find((e) => e.name === req.name);
    if (req.create) {
      if (existing) throw new Error(`environment "${req.name}" already exists`);
      envs.push({ name: req.name, variables: { ...req.variables } });
    } else if (!existing) {
      throw new Error(`environment "${req.name}" not found`);
    } else {
      existing.variables = { ...req.variables };
    }
    if (req.default !== undefined) for (const e of envs) e.default = req.default && e.name === req.name ? true : undefined;
    await delay(80);
    return { file: PATH };
  },
  ImportPreview: async () => { await delay(300); return importPreview; },
  ImportSave: async (req: ImportSaveRequest) => req.out,
  PickFile: async () => "/home/user/Downloads/Widget Shop.postman_collection.json",
  GitStatus: async () => ({
    available: true,
    repo: true,
    merging: mockMerging,
    root: "/home/user/widget-shop",
    branch: mockBranch,
    detached: false,
    ahead: mockHasUpstream ? 1 : 0,
    behind: mockHasUpstream ? 1 : 0,
    tracking: mockHasUpstream,
    upstream: mockHasUpstream ? `origin/${mockBranch}` : "",
    files: mockFiles.map((file) => ({ ...file, state: file.projectFile && mockConflict ? "conflicted" : file.state })),
    changedSteps: mockFiles.some((file) => file.projectFile) ? [
      { suite: "Widget Shop", case: "Widget Shop", name: "Health", index: 0, state: "added" },
      { suite: "Widgets", case: "Widgets", name: "Get widget", index: 0, state: "modified" },
    ] : [],
  }),
  GitChanges: async (): Promise<ProjectChanges> => {
    const data: ProjectChanges = {
      available: true,
      repo: true,
      steps: [
        { suite: "Widget Shop", case: "Widget Shop", name: "Health", index: 0, state: "added" },
        {
          suite: "Widgets",
          case: "Widgets",
          name: "Get widget",
          index: 0,
          state: "modified",
          parts: [
            { part: "headers", key: "Accept", before: "", after: "application/json" },
            { part: "body", before: "", after: "{\n  \"id\": 42\n}" },
          ],
        },
      ],
      environments: [],
      auth: [],
      suites: [],
      cases: [],
      otherFiles: mockFiles.filter((file) => !file.projectFile).length,
    };
    if (!mockFiles.some((file) => file.projectFile)) data.steps = [];
    return data;
  },
  GitStashes: async () => mockStashes.map(({ id, branch, message, date }, index) => ({ id, index, branch, message, date })),
  GitStash: async (req: GitStashRequest) => {
    await delay(80);
    if (mockConflict) throw new Error("resolve the existing conflicts before stashing");
    const files = mockFiles.filter((file) => (!req.paths?.length || req.paths.includes(file.path)) && (file.state !== "untracked" || req.includeUntracked));
    if (files.length === 0) throw new Error("nothing to stash in the chosen files");
    const changes = await mockBindings.GitChanges();
    if (!files.some((file) => file.projectFile)) changes.steps = [];
    changes.otherFiles = files.filter((file) => !file.projectFile).length;
    mockStashSerial += 1;
    mockStashes.unshift({ id: mockStashSerial.toString(16).padStart(40, "0"), index: 0, branch: mockBranch,
      message: (req.message || "Working changes").replace(/(password|token|secret|api[_-]?key)\s*[:=]\s*\S+/gi, "$1=***REDACTED***"),
      date: new Date().toISOString(), files: structuredClone(files), changes });
    mockFiles = mockFiles.filter((file) => !files.includes(file));
    noteMockDiskChange();
    mockProgress("stash");
    return { status: "stashed" };
  },
  GitStashChanges: async (req: GitStashActionRequest) => structuredClone(findMockStash(req.id).changes),
  GitStashAction: async (req: GitStashActionRequest) => {
    await delay(80);
    if (!["apply", "pop", "drop"].includes(req.action)) throw new Error("stash action must be apply, pop, or drop");
    const entry = findMockStash(req.id);
    if (req.action === "drop") {
      mockStashes = mockStashes.filter((stash) => stash.id !== req.id);
      mockProgress("stash-drop");
      return { status: "dropped" };
    }
    if (mockConflict) throw new Error("resolve the existing conflicts before applying a stash");
    const overlap = entry.files.filter((file) => mockFiles.some((work) => work.path === file.path));
    if (overlap.some((file) => file.state === "untracked")) throw new Error("could not restore untracked files from stash; the stash was kept");
    if (overlap.length > 0) {
      mockConflict = true;
      noteMockDiskChange();
      return { status: "conflict", conflicted: overlap.map((file) => file.path), output: "Stash apply left conflicts. Resolve them in your editor. The stash was kept." };
    }
    mockFiles.push(...structuredClone(entry.files));
    if (req.action === "pop") mockStashes = mockStashes.filter((stash) => stash.id !== req.id);
    noteMockDiskChange();
    mockProgress(`stash-${req.action}`);
    return { status: "applied" };
  },
  GitCommit: async (req: { message?: string }) => {
    await delay(120);
    mockProgress("commit");
    const message = (req.message ?? "").trim().toLowerCase();
    if (message.includes("hook")) {
      throw new Error("hook-says-no\npre-commit hook refused the commit");
    }
    if (message.includes("secret")) {
      throw new Error("Commit refused: plaintext secrets are staged.\nrequest.headers.X-Api-Key looks like a credential; store it as ${env:API_KEY}\nNothing was committed.");
    }
    mockConflict = false;
    mockMerging = false;
    return { output: `[${mockBranch} abc1234] ${req.message}` };
  },
  GitPull: async (req: { mode?: string }) => {
    await delay(150);
    mockProgress("pull");
    if (req.mode === "merge") {
      mockConflict = true;
      mockMerging = true;
      return { status: "conflict", conflicted: ["project.yaml"], output: "CONFLICT (content): Merge conflict in project.yaml\nAutomatic merge failed; fix conflicts and then commit the result." };
    }
    return { status: "diverged", output: "fatal: Not possible to fast-forward, aborting." };
  },
  GitFetch: async () => {
    await delay(120);
    mockProgress("fetch");
    return { output: "fetched origin" };
  },
  GitPush: async () => {
    await delay(120);
    mockProgress("push");
    if (!mockHasUpstream) {
      return { status: "no-upstream", remote: "origin", branch: mockBranch, remotes: ["origin"] };
    }
    return { status: "pushed", remote: "origin", branch: mockBranch, output: "pushed" };
  },
  GitPushUpstream: async (req: { remote?: string; branch?: string }) => {
    await delay(120);
    mockProgress("push");
    mockHasUpstream = true;
    return { status: "pushed", remote: req.remote, branch: req.branch, output: `branch '${req.branch}' set up to track '${req.remote}/${req.branch}'` };
  },
  GitBranches: async () => {
    const unique = [...new Set(["main", "other", mockBranch, ...mockExtraBranches])];
    return unique.map((name) => ({
      name,
      current: name === mockBranch,
      upstream: mockHasUpstream && name === mockBranch ? `origin/${name}` : "",
      ahead: 0,
      behind: 0,
    }));
  },
  GitSwitch: async (req: { branch: string; force?: boolean }) => {
    await delay(80);
    if (req.branch === "other" && !req.force) {
      return { status: "dirty", files: ["project.yaml"] };
    }
    mockBranch = req.branch;
    return { status: "switched", output: `Switched to branch '${req.branch}'` };
  },
  GitCreateBranch: async (req: { name: string; switchTo?: boolean; force?: boolean }) => {
    await delay(80);
    if (req.switchTo && !req.force && req.name === "blocked") {
      return { status: "dirty", files: ["project.yaml"] };
    }
    mockExtraBranches = [...mockExtraBranches, req.name];
    if (req.switchTo) mockBranch = req.name;
    return { status: req.switchTo ? "switched" : "created", output: req.name };
  },
  GitMergeAbort: async () => {
    await delay(80);
    mockProgress("abort");
    mockConflict = false;
    mockMerging = false;
    return "merge aborted";
  },
  OpenFile: async () => {
    await delay(20);
  },
  CancelGit: async () => {
    cancelMockGit();
  },
};

// mockCheckStep is a small stand-in for core/secretscan (name rule only):
// a value under a credential-named key with literal text outside ${...}.
const SECRET_WORDS = ["password", "passwd", "pwd", "secret", "token", "apikey", "privatekey", "credential", "authorization", "cookie", "sessionid", "accesskey"];
const PLACE_WORDS = ["url", "uri", "endpoint", "path", "file", "type", "name", "field", "header", "param", "location", "expires", "expiry", "ttl", "length", "style", "mode", "format", "method"];
function mockCheckStep(step: Step): SecretFinding[] {
  const out: SecretFinding[] = [];
  const norm = (k: string) => k.toLowerCase().replace(/[-_.\s]/g, "");
  const secretName = (k: string) => {
    const n = norm(k);
    if (PLACE_WORDS.some((p) => n.endsWith(p) && n.length > p.length)) return false;
    return SECRET_WORDS.some((w) => n.includes(w));
  };
  const envVar = (k: string) => k.replace(/([a-z0-9])([A-Z])/g, "$1_$2").replace(/[^A-Za-z0-9]+/g, "_").replace(/^_+|_+$/g, "").toUpperCase() || "SECRET";
  const walk = (v: unknown, path: string[]) => {
    if (Array.isArray(v)) { v.forEach((e, i) => walk(e, [...path, String(i)])); return; }
    if (!v || typeof v !== "object") return;
    for (const [k, e] of Object.entries(v as Record<string, unknown>)) {
      if (typeof e === "string" || typeof e === "number") {
        let lit = String(e).trim();
        if (norm(k).includes("authorization")) lit = lit.replace(/^(bearer|basic|digest|token|apikey|api-key|negotiate|ntlm|aws4-hmac-sha256)\s+/i, "");
        lit = lit.replace(/\$\{[^}]*\}/g, "").trim();
        if (lit && secretName(k)) out.push({ path: [...path, k], key: k, reason: "name", envVar: envVar(k) });
      } else walk(e, [...path, k]);
    }
  };
  walk(step, []);
  return out.sort((a, b) => a.path.join(".").localeCompare(b.path.join(".")));
}
