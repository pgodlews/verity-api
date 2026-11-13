// Types shared by the Go backend and the React frontend.
//
// Result types mirror core/spi/result.go; project types mirror core/model;
// the workspace types mirror core/workspace. Every string in a result is
// already redacted by core before it reaches the frontend.

export type Status = "passed" | "failed" | "error" | "skipped" | "aborted";

/** Progress event emitted on the Wails "run:event" channel while a run is in flight. */
export interface RunEvent {
  kind: "case_started" | "step_done" | "case_done";
  suite: string;
  case: string;
  iteration: number;
  stepIndex?: number;
  step?: StepResult;
  status?: Status;
  durationMs?: number;
}

// ---- Run results (core/spi) ----

export interface Summary {
  suites: number;
  cases: number;
  steps: number;
  assertions: number;
  passed: number;
  failed: number;
  errors: number;
  skipped: number;
  stepsFailed?: number;
  stepsErrored?: number;
  stepsSkipped?: number;
  stepsAborted?: number;
  assertionsPassed?: number;
  assertionsFailed?: number;
  casesPassed?: number;
  casesFailed?: number;
  casesErrored?: number;
  casesSkipped?: number;
  casesAborted?: number;
  /** True when the run was cancelled before it finished. */
  aborted?: boolean;
  status: Status;
}

export interface AssertionResult {
  passed: boolean;
  type: string;
  description?: string;
  message?: string;
  expected?: unknown;
  actual?: unknown;
}

export interface RequestCapture {
  method?: string;
  url?: string;
  headers?: Record<string, string>;
  /** Request cookie names; values are always masked. */
  cookies?: Record<string, string>;
  body?: string;
}

export interface ResponseCapture {
  statusCode: number;
  status?: string;
  headers?: Record<string, string>;
  body?: string;
  /** The body was cut at the 1 MiB capture limit. */
  bodyTruncated?: boolean;
  durationMs: number;
}

export interface ErrorInfo {
  code: string;
  message: string;
  path?: string;
}

/** A variable stored by one of the step's transfers (value redacted). */
export interface Capture {
  name: string;
  value: string;
}

export interface StepResult {
  name: string;
  protocol: string;
  status: Status;
  durationMs: number;
  /** Times the step executed the request, including the first. Omitted when it never ran. */
  attempts?: number;
  request?: RequestCapture;
  response?: ResponseCapture;
  assertions?: AssertionResult[];
  captures?: Capture[];
  error?: ErrorInfo;
}

export interface CaseResult {
  name: string;
  status: Status;
  durationMs: number;
  /** 0-based data-feed iteration, or -1 when the case has no feed. */
  iteration: number;
  steps: StepResult[];
}

export interface SuiteResult {
  name: string;
  status: Status;
  durationMs: number;
  cases: CaseResult[];
}

export interface RunResult {
  project: string;
  environment: string;
  startedAt?: string;
  durationMs: number;
  suites: SuiteResult[];
  summary: Summary;
}

export interface Catalog {
  protocols: string[];
  assertions: string[];
  reporters: string[];
  auth: string[];
  dataSources: string[];
}

export interface ValidationIssue {
  code: string;
  message: string;
  path?: string;
}

export interface ValidateResult {
  ok: boolean;
  project: string;
  errors: string[];
  /** The same problems with their code and project path. */
  issues?: ValidationIssue[];
}

// ---- Project model (core/model) ----

export interface AuthRef {
  use?: string[];
  /** Defaults to true; false replaces (instead of stacking on) parent auth. */
  inherit?: boolean;
}

export interface AuthDef {
  name: string;
  /** none | basic | digest | bearer | apikey | mtls | oauth2 | oauth2_client_credentials | aws_sigv4 | hmac | plugin types */
  type: string;
  config?: Record<string, unknown>;
}

/** SDL file or an introspection fetch. A step value replaces the suite value, which replaces the project value. */
export interface GraphQLSchemaSource {
  file?: string;
  introspect?: boolean;
}

/** One generated operation the GraphQL body tab can insert. */
export interface GraphQLTemplate {
  operation: string;
  kind: string;
  operationName: string;
  query: string;
  /** Pretty-printed variables JSON object. */
  variables: string;
}

export interface GraphQLTemplateRequest {
  path: string;
  suite: string;
  case: string;
  index: number;
  /** "" = the project's default environment. */
  environment?: string;
}

export interface Assertion {
  /** status | response_time | jsonpath | xpath | json_schema | graphql_schema | xsd | contains | regex | header */
  type: string;
  description?: string;
  expr?: string;
  /** eq | ne | lt | lte | gt | gte | contains | matches | exists | not_exists */
  op?: string;
  value?: unknown;
  config?: Record<string, unknown>;
}

export interface Transfer {
  name: string;
  /** jsonpath | xpath | header | status | body | regex */
  from: string;
  expr?: string;
  default?: string;
  /** "case" (default) or "run": also visible to cases that start later. */
  scope?: "case" | "run";
}

/** Per-step re-execution. Total attempts are 1 + count (count is 0–20). */
export interface StepRetry {
  count: number;
  /** Pause between attempts. A cancelled run ends the pause. */
  delayMs?: number;
  /**
   * passed: retry while an assertion fails or the attempt is a transport error.
   * status: retry only on a transport error, HTTP 429, or HTTP 5xx.
   */
  until: "passed" | "status";
}

/**
 * Step.request is protocol-specific:
 *  rest:    method, url, headers{}, query{}, and one of body (string) | json (any) | form{}
 *  graphql: url, query, variables{}, operationName, headers{}
 * The workspace bridge substitutes jsonText / variablesText source strings to
 * preserve number literals; SaveStep decodes them without float64 conversion.
 * Schema lives on the project, suite, or step (`schema`), not inside request.
 *  soap:    url, body (envelope), soapAction, version ("1.1"|"1.2"), headers{}
 * Values may contain ${var}, ${env:NAME} and ${secret:...} references.
 */
export interface Step {
  name: string;
  protocol: string;
  request?: Record<string, unknown>;
  auth?: AuthRef;
  assertions?: Assertion[];
  transfers?: Transfer[];
  vars?: Record<string, string>;
  skip?: boolean;
  /** Per-attempt HTTP timeout in milliseconds. Overrides the run timeout. */
  timeoutMs?: number;
  retry?: StepRetry;
  schema?: GraphQLSchemaSource;
}

export interface TestCase {
  name: string;
  vars?: Record<string, string>;
  auth?: AuthRef;
  feed?: unknown;
  steps: Step[];
  continueOnError?: boolean;
}

export interface Suite {
  name: string;
  vars?: Record<string, string>;
  auth?: AuthRef;
  cases: TestCase[];
  parallel?: number;
  schema?: GraphQLSchemaSource;
}

export interface Environment {
  name: string;
  variables?: Record<string, string>;
  default?: boolean;
  auth?: AuthRef;
}

export interface Project {
  schemaVersion: string;
  name: string;
  description?: string;
  environments?: Environment[];
  auth?: AuthDef[];
  suites?: Suite[];
  schema?: GraphQLSchemaSource;
}

// ---- Workspace service (core/workspace) ----

/** An ${env:NAME} reference and whether NAME is set on this machine. */
export interface EnvRef {
  name: string;
  set: boolean;
  /** Where it is used: "Suite/Case/Step", "auth <name>" or "environment <name>". */
  usedIn: string[];
}

/** A ${var} that nothing defines in some environments. */
export interface UndefinedVar {
  name: string;
  /** Environment names in which the variable has no value. */
  missingIn: string[];
  usedIn: string[];
}

/** A variable captured at run time by a step's transfer. */
export interface CaptureDef {
  name: string;
  from: string;
  expr?: string;
  scope?: "case" | "run";
  /** "Suite/Case/Step" of the step that captures it. */
  step: string;
}

export interface ProjectInfo {
  /** Absolute path of the project file. */
  path: string;
  project: Project;
  envRefs: EnvRef[];
  undefinedVars: UndefinedVar[];
  captures: CaptureDef[];
  /** File that defines each step, when the backend could walk the project. */
  stepFiles?: StepOrigin[];
  /** sha256 of each project file as loaded. SaveEnvironment sends these back. */
  fileHashes?: FileHash[];
}

/** The file SaveStep would write for one step. */
export interface StepOrigin {
  suite: string;
  case: string;
  index: number;
  file: string;
  /** Zero-based count of earlier steps in the case with the same name. */
  occurrence?: number;
  /** sha256 of the step as loaded. SaveStep sends it back as baseline. */
  hash?: string;
}

export interface FileHash {
  path: string;
  hash: string;
}

// ---- Read-only git (core/workspace git.go) ----

export interface GitFile {
  path: string;
  /** modified | added | deleted | renamed | untracked | conflicted */
  state: string;
  projectFile: boolean;
  renamedFrom?: string;
}

export interface GitStepRef {
  suite: string;
  case: string;
  name: string;
  index: number;
  /** added | removed | modified | moved */
  state: string;
}

export interface GitInfo {
  available: boolean;
  repo: boolean;
  root?: string;
  branch?: string;
  detached: boolean;
  short?: string;
  ahead: number;
  behind: number;
  tracking: boolean;
  upstream?: string;
  /** The configured upstream ref is missing. */
  upstreamGone?: boolean;
  /** MERGE_HEAD exists. */
  merging?: boolean;
  /** rebase-merge or rebase-apply exists. */
  rebasing?: boolean;
  files: GitFile[];
  changedSteps: GitStepRef[];
}

export interface PartChange {
  part: string;
  key?: string;
  before: string;
  after: string;
}

export interface StepChange {
  suite: string;
  case: string;
  name: string;
  index: number;
  state: string;
  fromSuite?: string;
  fromCase?: string;
  fromName?: string;
  fromIndex?: number;
  parts?: PartChange[];
}

export interface EnvChange {
  name: string;
  state: string;
  parts?: PartChange[];
}

export interface AuthChange {
  name: string;
  state: string;
  parts?: PartChange[];
}

export interface ScopeChange {
  suite?: string;
  name: string;
  state: string;
  parts?: PartChange[];
}

export interface ProjectChanges {
  available: boolean;
  repo: boolean;
  steps: StepChange[];
  environments: EnvChange[];
  auth: AuthChange[];
  suites: ScopeChange[];
  cases: ScopeChange[];
  /** Project name, description, schemaVersion, or a generic "project" entry. */
  meta?: PartChange[];
  otherFiles: number;
}

/** Desktop "project:changed" event, after the debounce. */
export interface ProjectChanged {
  files: string[];
  git: boolean;
}

/** Desktop "git:progress" event while fetch, pull, or push is running. */
export interface GitProgress {
  op: string;
  line: string;
}

export interface GitCommitRequest {
  path: string;
  message: string;
  paths: string[];
}

export interface GitCommitResult {
  output?: string;
}

export interface GitPullRequest {
  path: string;
  /** ff-only (default) or merge. */
  mode?: string;
}

export interface GitPullResult {
  /** updated | up-to-date | diverged | conflict */
  status: string;
  conflicted?: string[];
  output?: string;
}

export interface GitFetchResult {
  output?: string;
}

export interface GitPushResult {
  /** pushed | no-upstream */
  status: string;
  remote?: string;
  branch?: string;
  remotes?: string[];
  output?: string;
}

export interface GitUpstreamRequest {
  path: string;
  remote: string;
  branch: string;
}

export interface GitBranchInfo {
  name: string;
  current: boolean;
  upstream?: string;
  ahead: number;
  behind: number;
  /** The configured upstream ref is missing. */
  gone?: boolean;
}

export interface GitSwitchRequest {
  path: string;
  branch: string;
  /** Switch even when project files have uncommitted changes. */
  force?: boolean;
}

export interface GitCreateBranchRequest {
  path: string;
  name: string;
  switchTo?: boolean;
  force?: boolean;
}

export interface GitBranchWriteResult {
  /** switched | created | dirty */
  status: string;
  files?: string[];
  output?: string;
}

export interface RunOptions {
  path: string;
  /** "" = the project's default environment. */
  environment: string;
  parallel: number;
  vars: Record<string, string>;
  /** Exact names or globs; empty = all. */
  suites?: string[];
  cases?: string[];
  /** > 0: run only the first N steps of each case (earlier steps feed captures). */
  untilStep?: number;
  /** Per-attempt HTTP timeout for steps without their own; 0/absent = 30 s. */
  timeoutMs?: number;
  /** true = do not follow HTTP redirects (followed by default, as in the CLI). */
  noRedirects?: boolean;
}

export interface SaveStepRequest {
  path: string;
  suite: string;
  case: string;
  /** 0-based index of the step within the case. */
  index: number;
  step: Step;
  /** sha256 of the step as loaded. When empty, fileBaseline is checked instead. */
  baseline?: string;
  /** sha256 of the file that defines the step. Used when baseline is empty. */
  fileBaseline?: string;
}

export interface SaveStepResult {
  /** The file that was rewritten (the project file or one of its includes). */
  file: string;
  /** Every file this save wrote. An environment save can list several. */
  files?: string[];
  /** sha256 of the bytes just written, keyed by path. */
  hashes?: Record<string, string>;
}

/**
 * Replace one environment's variables (or create it). Values must not be
 * literal credentials: the backend refuses them — use "${env:NAME}".
 */
export interface SaveEnvironmentRequest {
  path: string;
  name: string;
  variables: Record<string, string>;
  /** true: make this the default (clears others); false: clear its flag. */
  default?: boolean;
  /** Create a new environment in the root project file. */
  create?: boolean;
  /** sha256 of project files as loaded, keyed by absolute path. */
  baseline?: Record<string, string>;
}

export interface ImportStats {
  suites: number;
  cases: number;
  steps: number;
  assertions: number;
  auth: number;
  environments: number;
}

export interface ImportPreview {
  yaml: string;
  warnings: string[];
  stats: ImportStats;
}

export interface ImportSaveRequest {
  /** openapi | wsdl | postman */
  kind: string;
  spec: string;
  /** Postman environment files (ignored for other kinds). */
  envs: string[];
  out: string;
  overwrite: boolean;
}

/** Identifies a process-only OAuth session; never carries tokens. */
export interface OAuthRequest { path: string; environment: string; authName: string; }

/** A plaintext credential in a step (workspace.CheckStep). Never carries the value. */
export interface SecretFinding {
  /** Keys / indexes from the step root, e.g. ["request", "headers", "X-Api-Key"]. */
  path: string[];
  key: string;
  /** "name": the key names a credential; "shape": the value looks like a token. */
  reason: "name" | "shape";
  /**
   * The literal sits inside a larger string (XML/form body, URL): path points
   * at that string and the fix is an edit inside it, not a whole-value swap.
   */
  inText?: boolean;
  /** Suggested NAME for the ${env:NAME} replacement. */
  envVar: string;
  file?: string;
  line?: number;
}

export interface GitStashInfo {
  id: string;
  index: number;
  branch: string;
  message: string;
  date: string;
}

export interface GitStashRequest {
  path: string;
  paths?: string[];
  includeUntracked: boolean;
  message?: string;
}

export interface GitStashActionRequest {
  path: string;
  id: string;
  action: "apply" | "pop" | "drop";
}

// ---- Load testing (core/load, core/workspace) ----

export interface LoadLatency {
  minMs: number;
  meanMs: number;
  p50Ms: number;
  p90Ms: number;
  p95Ms: number;
  p99Ms: number;
  maxMs: number;
}

export interface LoadStatusCount {
  code: number;
  count: number;
}

export interface LoadMetrics {
  name?: string;
  requests: number;
  assertionFailures: number;
  transportErrors: number;
  otherErrors?: number;
  errorRate: number;
  rps: number;
  /** Protocol call of the final attempt (network exchange). */
  latency: LoadLatency;
  /** Whole step: attempts, retry delays, auth, assertions, transfers. */
  stepLatency?: LoadLatency;
  statusCodes: LoadStatusCount[];
  errors: string[];
}

export interface LoadThresholdResult {
  expr: string;
  passed: boolean;
  step?: string;
  metric?: string;
  actualMs: number;
  limitMs: number;
  message?: string;
}

export interface LoadErrorRateResult {
  max: number;
  actual: number;
  passed: boolean;
  message?: string;
}

export interface LoadSetupStep {
  suite: string;
  name: string;
  status: string;
  error?: string;
}

/** Redacted load report produced by core/load (schemaVersion "1"). */
export interface LoadReport {
  schemaVersion: string;
  project: string;
  environment: string;
  startedAt: string;
  finishedAt: string;
  durationMs: number;
  interrupted: boolean;
  vus: number;
  iterations: number;
  rampUpMs?: number;
  thinkMs?: number;
  configuredDurationMs?: number;
  configuredIterations?: number;
  targets: string[];
  summary: LoadMetrics;
  steps: LoadMetrics[];
  thresholds: LoadThresholdResult[];
  errorRateThreshold?: LoadErrorRateResult;
  setup?: LoadSetupStep[];
  setupFailed?: boolean;
  /** verr code of the first failed setup step. */
  setupErrorCode?: string;
  teardown?: LoadSetupStep[];
  teardownFailed?: boolean;
  passed: boolean;
}

/** Progress sample emitted on the Wails "load:live" channel while a load run is in flight. */
export interface LoadLiveStats {
  vus: number;
  rps: number;
  p95Ms: number;
  errors: number;
  requests: number;
  elapsedMs: number;
}

/** Pre-run target check result from workspace.LoadTargets. */
export interface LoadTargetReport {
  hosts: string[];
  refused: string[];
  message?: string;
}

export interface LoadOptions {
  path: string;
  environment: string;
  vars?: Record<string, string>;
  suites?: string[];
  cases?: string[];
  vus: number;
  iterations?: number;
  durationMs?: number;
  rampUpMs?: number;
  thinkMs?: number;
  thresholds?: string[];
  maxErrorRate?: string;
  allowRemote?: boolean;
  timeoutMs?: number;
  noRedirects?: boolean;
  liveEveryMs?: number;
}

export interface SaveLoadReportRequest {
  path?: string;
  out: string;
  overwrite?: boolean;
  report: LoadReport;
}
