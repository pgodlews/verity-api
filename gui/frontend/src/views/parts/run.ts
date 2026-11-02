import type { AssertionResult, Project, RequestCapture, RunEvent, RunResult, Status, Step, StepResult, Summary } from "../../types";

/** Result capture, or the project step's method and URL when the run didn't record one. */
export function capturedRequest(step: StepResult, defined?: Step): RequestCapture | undefined {
  if (step.request?.url || step.request?.method) return step.request;
  const req = defined?.request;
  if (!req) return undefined;
  const url = typeof req.url === "string" ? req.url : undefined;
  // REST requests default to GET when the project leaves the method out.
  const method = typeof req.method === "string" ? req.method : defined?.protocol === "rest" && url ? "GET" : undefined;
  if (!method && !url) return undefined;
  return { method, url };
}

export interface FlatStep {
  key: string;
  suite: string;
  caseName: string;
  caseIndex: number;
  iteration: number;
  index: number;
  step: StepResult;
  /** Shown above the first row of this case. Null when the suite heading is enough. */
  caseHeading: string | null;
}

export type StepFilter = "all" | Status;

export function stepKey(suite: string, caseIndex: number, index: number): string {
  return `${suite}\0${caseIndex}\0${index}`;
}

/** iteration 0 is the first data row, labelled "row 1". */
export function rowLabel(iteration: number): string {
  return iteration >= 0 ? `row ${iteration + 1}` : "";
}

function caseHeading(suiteName: string, caseName: string, iteration: number, several: boolean): string | null {
  const row = rowLabel(iteration);
  if (!several && !row) return null;
  const same = caseName === suiteName;
  if (several && same && !row) return null;
  if (!several || same) return row;
  return row ? `${caseName} · ${row}` : caseName;
}

export function flatten(result: RunResult): FlatStep[] {
  const out: FlatStep[] = [];
  for (const suite of result.suites ?? []) {
    const several = suite.cases.length > 1;
    suite.cases.forEach((testCase, caseIndex) => {
      const heading = caseHeading(suite.name, testCase.name, testCase.iteration, several);
      testCase.steps.forEach((step, index) => {
        out.push({
          key: stepKey(suite.name, caseIndex, index),
          suite: suite.name,
          caseName: testCase.name,
          caseIndex,
          iteration: testCase.iteration,
          index,
          step,
          caseHeading: heading,
        });
      });
    });
  }
  return out;
}

export function defaultStepKey(result: RunResult | null): string | null {
  if (!result) return null;
  let first: string | null = null;
  for (const row of flatten(result)) {
    if (!first) first = row.key;
    if (row.step.status === "failed" || row.step.status === "error") return row.key;
  }
  return first;
}

export function countSteps(rows: FlatStep[]): Record<StepFilter, number> {
  const counts: Record<StepFilter, number> = { all: rows.length, passed: 0, failed: 0, error: 0, skipped: 0, aborted: 0 };
  for (const row of rows) counts[row.step.status] += 1;
  return counts;
}

/** Case names that contain a failed or errored step, in run order, once each. */
export function failedCaseNames(result: RunResult): string[] {
  const names: string[] = [];
  const seen = new Set<string>();
  for (const suite of result.suites ?? []) {
    for (const testCase of suite.cases) {
      const bad = testCase.steps.some((step) => step.status === "failed" || step.status === "error");
      if (bad && !seen.has(testCase.name)) {
        seen.add(testCase.name);
        names.push(testCase.name);
      }
    }
  }
  return names;
}

export function stepNote(step: StepResult): { text: string; tone: "fail" | "error" | "muted" | "abort" } {
  if (step.status === "failed") {
    const failed = step.assertions?.find((assertion) => !assertion.passed);
    const description = failed?.description?.trim();
    const message = failed?.message?.trim();
    const text =
      description && message && description !== message
        ? `${description} — ${message}`
        : description || message || "Assertion failed";
    return { text, tone: "fail" };
  }
  if (step.status === "error") {
    return { text: step.error?.message || step.error?.code || "Error", tone: "error" };
  }
  if (step.status === "passed") {
    const count = step.assertions?.length ?? 0;
    return { text: `${count} ${count === 1 ? "check" : "checks"} passed`, tone: "muted" };
  }
  if (step.status === "aborted") return { text: "Not started", tone: "abort" };
  return { text: "Skipped", tone: "muted" };
}

export function methodPresentation(protocol: string, request?: RequestCapture): { short: string; cls: string; verb: string } {
  const kind = protocol.toLowerCase();
  if (kind === "graphql") return { short: "GQL", cls: "method-gql", verb: (request?.method || "POST").toUpperCase() };
  if (kind === "soap") return { short: "SOAP", cls: "method-soap", verb: (request?.method || "POST").toUpperCase() };
  const verb = (request?.method || protocol || "").toUpperCase();
  const known: Record<string, string> = {
    GET: "method-get",
    POST: "method-post",
    PUT: "method-put",
    PATCH: "method-patch",
    DELETE: "method-delete",
  };
  const short = verb === "DELETE" ? "DEL" : verb.length > 6 ? verb.slice(0, 5) : verb || kind.toUpperCase();
  return { short: short || "REQ", cls: known[verb] ?? "method-other", verb: verb || short };
}

export function projectStep(project: Project, suite: string, caseName: string, index: number): Step | undefined {
  return project.suites?.find((item) => item.name === suite)?.cases.find((item) => item.name === caseName)?.steps[index];
}

export function checkSummary(assertions: AssertionResult[]): string {
  const failed = assertions.filter((assertion) => !assertion.passed).length;
  if (assertions.length === 0) return "no checks";
  if (failed === 0) return `all ${assertions.length} passed`;
  return `${failed} of ${assertions.length} failed`;
}

export function barWidth(ms: number, max: number): string {
  if (ms <= 0 || max <= 0) return "0%";
  return `${Math.max(4, Math.round((ms / max) * 100))}%`;
}

export function blankRun(project: string, environment: string): RunResult {
  return {
    project,
    environment,
    startedAt: new Date().toISOString(),
    durationMs: 0,
    suites: [],
    summary: {
      suites: 0,
      cases: 0,
      steps: 0,
      assertions: 0,
      passed: 0,
      failed: 0,
      errors: 0,
      skipped: 0,
      status: "passed",
    },
  };
}

/** Steps the run is expected to execute. Feeds can make the real total larger. */
export function countPlannedSteps(project: Project, opts: { suites?: string[]; cases?: string[]; untilStep?: number }): number {
  let total = 0;
  for (const suite of project.suites ?? []) {
    if (opts.suites?.length && !opts.suites.includes(suite.name)) continue;
    for (const testCase of suite.cases) {
      if (opts.cases?.length && !opts.cases.includes(testCase.name)) continue;
      const steps = opts.untilStep && opts.untilStep > 0 ? testCase.steps.slice(0, opts.untilStep) : testCase.steps;
      total += steps.length;
    }
  }
  return total;
}

function rollupStatus(statuses: Status[]): Status {
  if (statuses.includes("error")) return "error";
  if (statuses.includes("failed")) return "failed";
  if (statuses.includes("aborted")) return "aborted";
  if (statuses.length > 0 && statuses.every((status) => status === "skipped")) return "skipped";
  return "passed";
}

function liveSummary(result: RunResult): Summary {
  const steps = result.suites.flatMap((suite) => suite.cases.flatMap((testCase) => testCase.steps));
  const assertions = steps.flatMap((step) => step.assertions ?? []);
  const aborted = steps.some((step) => step.status === "aborted") || result.suites.some((suite) => suite.cases.some((testCase) => testCase.status === "aborted"));
  return {
    suites: result.suites.length,
    cases: result.suites.reduce((n, suite) => n + suite.cases.length, 0),
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
    aborted: aborted || undefined,
    status: rollupStatus(steps.map((step) => step.status)),
  };
}

/** Fold one progress event into a run result. Used while a GUI run is streaming. */
export function applyRunEvent(result: RunResult, event: RunEvent): RunResult {
  const suites = result.suites.map((suite) => ({
    ...suite,
    cases: suite.cases.map((testCase) => ({ ...testCase, steps: testCase.steps.slice() })),
  }));
  let suite = suites.find((item) => item.name === event.suite);
  if (!suite) {
    suite = { name: event.suite, status: "passed", durationMs: 0, cases: [] };
    suites.push(suite);
  }
  let testCase = suite.cases.find((item) => item.name === event.case && item.iteration === event.iteration);
  if (!testCase) {
    testCase = { name: event.case, status: "passed", durationMs: 0, iteration: event.iteration, steps: [] };
    suite.cases.push(testCase);
  }
  if (event.kind === "step_done" && event.step) {
    const index = event.stepIndex ?? testCase.steps.length;
    while (testCase.steps.length <= index) {
      testCase.steps.push({ name: "", protocol: "", status: "skipped", durationMs: 0 });
    }
    testCase.steps[index] = event.step;
    testCase.status = rollupStatus(testCase.steps.map((step) => step.status));
  }
  if (event.kind === "case_done") {
    if (event.status) testCase.status = event.status;
    if (event.durationMs != null) testCase.durationMs = event.durationMs;
  }
  suite.status = rollupStatus(suite.cases.map((item) => item.status));
  const next: RunResult = { ...result, suites };
  next.summary = liveSummary(next);
  return next;
}

export function resultSignature(result: RunResult | null): string {
  if (!result) return "";
  const shape = (result.suites ?? [])
    .map((suite) =>
      suite.cases
        .map((item) => `${item.name}#${item.iteration}:${item.steps.map((step) => step.status[0] ?? "").join("")}`)
        .join(","),
    )
    .join(";");
  return [result.project, result.environment, result.startedAt ?? "", result.durationMs, result.summary.status, shape].join("|");
}
