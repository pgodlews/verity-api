// Helpers for the desktop Load test view (threshold parsing/formatting,
// error-rate validation, target screening, CLI command builder, and report
// formatting). Keep pure of DOM/runtime imports so node --test can run them.

import type { LoadOptions, LoadReport, LoadTargetReport, Project } from "../../types";

export const LOAD_METRICS = ["p50", "p90", "p95", "p99", "max", "mean"] as const;
export type LoadMetric = (typeof LOAD_METRICS)[number];

export const LOAD_OPS = ["<", "<="] as const;
export type LoadOp = (typeof LOAD_OPS)[number];

export const LOAD_UNITS = ["ms", "s"] as const;
export type LoadUnit = (typeof LOAD_UNITS)[number];

// core/workspace.clampMs caps desktop durations at 24 hours.
export const MAX_LOAD_TIME_MS = 86_400_000;

export interface LoadProfileDraft {
  vus: string;
  mode: "duration" | "iterations";
  durationSec: string;
  iterations: string;
  rampUpSec: string;
  thinkMs: string;
}

export type LoadProfileErrors = Partial<Record<Exclude<keyof LoadProfileDraft, "mode">, string>>;
type LoadProfileOptions = Pick<LoadOptions, "vus" | "iterations" | "durationMs" | "rampUpMs" | "thinkMs">;

/** Validate raw fields before conversion; never clamp, floor, or drop invalid input. */
export function validateLoadProfile(draft: LoadProfileDraft):
  { options: LoadProfileOptions; errors: LoadProfileErrors } | { options: null; errors: LoadProfileErrors } {
  const errors: LoadProfileErrors = {};
  const number = (raw: string) => raw.trim() === "" ? NaN : Number(raw);
  const vus = number(draft.vus);
  if (!Number.isInteger(vus) || vus < 1 || vus > 1000) {
    errors.vus = "Virtual users must be an integer from 1 to 1000.";
  }
  const iterations = number(draft.iterations);
  if (draft.mode === "iterations" && (!Number.isSafeInteger(iterations) || iterations < 1)) {
    errors.iterations = `Iterations must be a positive safe integer (at most ${Number.MAX_SAFE_INTEGER}).`;
  }
  function time(field: "durationSec" | "rampUpSec" | "thinkMs", scale: number): number {
    const value = number(draft[field]);
    const ms = value * scale;
    const roundedMs = Math.round(ms);
    if (!Number.isFinite(ms) || value < 0 || ms > MAX_LOAD_TIME_MS) {
      errors[field] = `Enter a finite number from 0 to ${MAX_LOAD_TIME_MS / scale} ${scale === 1000 ? "seconds" : "ms"} (24 hours maximum).`;
    } else if ((roundedMs === 0 && ms !== 0) || Math.abs(ms - roundedMs) > Number.EPSILON * Math.abs(ms)) {
      errors[field] = "Time must be representable in whole milliseconds.";
    } else if (field === "durationSec" && roundedMs === 0) {
      errors[field] = "Duration must be at least 0.001 seconds (24 hours maximum).";
    }
    // Only removes floating-point representation noise (e.g. 1.001s * 1000).
    return roundedMs;
  }
  const durationMs = draft.mode === "duration" ? time("durationSec", 1000) : undefined;
  const rampUpMs = time("rampUpSec", 1000);
  const thinkMs = time("thinkMs", 1);
  if (Object.keys(errors).length > 0) return { options: null, errors };
  return {
    options: { vus, ...(draft.mode === "duration" ? { durationMs } : { iterations }), rampUpMs, thinkMs },
    errors,
  };
}

export interface ThresholdDraft {
  step: string;
  metric: LoadMetric;
  op: LoadOp;
  value: string;
  unit: LoadUnit;
}

export interface ParsedThreshold {
  expr: string;
  step: string;
  metric: LoadMetric;
  op: LoadOp;
  value: number;
  unit: LoadUnit;
  limitMs: number;
}

const THRESHOLD_RE = /^(?:(.+):)?(p50|p90|p95|p99|max|mean)\s*(<|<=)\s*([0-9]*\.?[0-9]+)\s*(ms|s)$/i;

/** Parses a threshold expression like "p95<500ms" or "GET widget:p95<=1s". */
export function parseThreshold(expr: string): ParsedThreshold | null {
  const trimmed = expr.trim();
  const match = THRESHOLD_RE.exec(trimmed);
  if (!match) return null;
  const num = Number(match[4]);
  if (!Number.isFinite(num) || num < 0) return null;
  const step = match[1] ?? "";
  const metric = match[2].toLowerCase() as LoadMetric;
  const op = match[3] as LoadOp;
  const unit = match[5].toLowerCase() as LoadUnit;
  const limitMs = unit === "s" ? num * 1000 : num;
  return { expr: trimmed, step, metric, op, value: num, unit, limitMs };
}

/** Formats a ThresholdDraft into a CLI/core threshold string, or "" when invalid. */
export function formatThreshold(draft: ThresholdDraft): string {
  const rawVal = draft.value.trim();
  if (!rawVal || !/^(?:[0-9]*\.?[0-9]+)$/.test(rawVal)) return "";
  const num = Number(rawVal);
  if (!Number.isFinite(num) || num < 0) return "";
  if (!LOAD_METRICS.includes(draft.metric)) return "";
  if (!LOAD_OPS.includes(draft.op)) return "";
  if (!LOAD_UNITS.includes(draft.unit)) return "";
  const step = draft.step.trim();
  const bound = `${draft.metric}${draft.op}${rawVal}${draft.unit}`;
  return step ? `${step}:${bound}` : bound;
}

/**
 * Parses a max-error-rate string ("1%" -> 0.01, "0.01" -> 0.01).
 * Returns null when invalid, or undefined when empty.
 */
export function parseErrorRate(raw: string): number | null | undefined {
  const s = raw.trim();
  if (s === "") return undefined;
  const percent = s.endsWith("%");
  const body = (percent ? s.slice(0, -1) : s).trim();
  // strconv.ParseFloat accepts signs, decimal exponents, trailing dots,
  // digit separators, and hexadecimal floats with a binary exponent.
  const digits = "[0-9](?:_?[0-9])*";
  const decimal = new RegExp(`^[+-]?(?:${digits}(?:\\.(?:${digits})?)?|\\.${digits})(?:[eE][+-]?${digits})?$`);
  const hexDigits = "[0-9a-fA-F](?:_?[0-9a-fA-F])*";
  const hex = new RegExp(`^([+-]?)0[xX]_?(${hexDigits}(?:\\.(?:${hexDigits})?)?|\\.${hexDigits})[pP]([+-]?${digits})$`);
  let n: number;
  if (decimal.test(body)) {
    n = Number(body.replace(/_/g, ""));
  } else {
    const match = hex.exec(body);
    if (!match) return null;
    const [whole, fraction = ""] = match[2].replace(/_/g, "").split(".");
    const mantissa = parseInt(whole || "0", 16) + (fraction ? parseInt(fraction, 16) / 16 ** fraction.length : 0);
    n = (match[1] === "-" ? -1 : 1) * mantissa * 2 ** Number(match[3].replace(/_/g, ""));
  }
  if (!Number.isFinite(n) || n < 0 || n > (percent ? 100 : 1)) return null;
  return percent ? n / 100 : n;
}

/** Warning sentence when VUs > 200 (matches core/load.VUWarning). */
export function vuWarning(vus: number): string {
  if (vus > 200 && vus <= 1000) {
    return `warning: --vus=${vus} is above 200; a load run can open many connections`;
  }
  return "";
}

/** Exact CLI refusal message for non-private targets (matches core/load/targets.go). */
export function refusalMessage(refused: readonly string[]): string {
  if (!refused || refused.length === 0) return "";
  return `refusing non-private target ${refused.join(", ")}; pass --allow-remote to run it`;
}

/**
 * Checks whether a host literal or localhost is in the private allow-list
 * (127.0.0.0/8, ::1, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16,
 * 169.254.0.0/16, fe80::/10, or localhost). Unresolved templates and other
 * hostnames require DNS resolution on the backend (or are refused in ?mock).
 */
export function isPrivateHost(host: string): boolean {
  let h = host.trim().toLowerCase();
  if (!h || h.includes("${") || h.includes("}")) return false;
  if (h.startsWith("[") && h.endsWith("]")) h = h.slice(1, -1);
  if (h === "localhost" || h === "::1") return true;
  if (h.startsWith("::ffff:")) h = h.slice("::ffff:".length);
  if (/^fe[89ab][0-9a-f]:/i.test(h)) return true;
  const parts = h.split(".");
  if (parts.length === 4 && parts.every((p) => /^\d+$/.test(p))) {
    const octets = parts.map(Number);
    if (octets.some((n) => n < 0 || n > 255)) return false;
    const [a, b] = octets;
    if (a === 127 || a === 10) return true;
    if (a === 172 && b >= 16 && b <= 31) return true;
    if (a === 192 && b === 168) return true;
    if (a === 169 && b === 254) return true;
    return false;
  }
  return false;
}

/** Extracts the host(s) from a step URL or target, matching core/load/targets.go. */
export function extractEndpointHosts(raw: string): { hosts: string[]; local: boolean } {
  const s = raw.trim();
  if (!s) return { hosts: [], local: false };
  if (s.startsWith("unix:")) return { hosts: [], local: true };
  if (!s.includes("://")) {
    return { hosts: [stripPort(s)], local: false };
  }
  try {
    const u = new URL(s);
    const scheme = u.protocol.replace(/:$/, "").toLowerCase();
    if (scheme === "dns" || scheme === "passthrough") {
      const hosts: string[] = [];
      if (u.hostname) hosts.push(stripBrackets(u.hostname));
      const pathPart = u.pathname.replace(/^\/+/, "").split("/")[0];
      if (pathPart) hosts.push(stripPort(pathPart));
      return { hosts, local: false };
    }
    if (u.hostname) return { hosts: [stripBrackets(u.hostname)], local: false };
  } catch {
    // Fall back to manual host extraction when ${...} remains in the URL.
    const afterScheme = s.split("://")[1] ?? "";
    const hostPort = afterScheme.replace(/^\/+/, "").split("/")[0] ?? "";
    if (hostPort) return { hosts: [stripPort(hostPort)], local: false };
  }
  return { hosts: [], local: false };
}

function stripBrackets(h: string): string {
  return h.startsWith("[") && h.endsWith("]") ? h.slice(1, -1) : h;
}

function stripPort(hostPort: string): string {
  if (hostPort.startsWith("[")) {
    const end = hostPort.indexOf("]");
    if (end > 0) return hostPort.slice(1, end);
  }
  const idx = hostPort.lastIndexOf(":");
  if (idx > 0 && !hostPort.slice(0, idx).includes(":")) {
    return hostPort.slice(0, idx);
  }
  return hostPort;
}

/** Unique non-skipped step names in project order for the selected suites/cases. */
export function collectSelectedSteps(
  project: Project,
  suites?: readonly string[],
  cases?: readonly string[],
): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  const suiteSet = suites && suites.length > 0 ? new Set(suites) : null;
  const caseSet = cases && cases.length > 0 ? new Set(cases) : null;
  for (const suite of project.suites ?? []) {
    if (suiteSet && !suiteSet.has(suite.name)) continue;
    for (const testCase of suite.cases ?? []) {
      if (caseSet && !caseSet.has(testCase.name)) continue;
      for (const step of testCase.steps ?? []) {
        if (step.skip || !step.name || seen.has(step.name)) continue;
        seen.add(step.name);
        out.push(step.name);
      }
    }
  }
  return out;
}

/** Client-side target preview for the selected environment, suites, and cases. */
export function collectProjectTargets(
  project: Project,
  environment: string,
  suites?: readonly string[],
  cases?: readonly string[],
  extraVars?: Record<string, string>,
  allowRemote = false,
): LoadTargetReport {
  const envName = environment || project.environments?.find((e) => e.default)?.name || project.environments?.[0]?.name || "";
  const envVars = project.environments?.find((e) => e.name === envName)?.variables ?? {};
  const suiteSet = suites && suites.length > 0 ? new Set(suites) : null;
  const caseSet = cases && cases.length > 0 ? new Set(cases) : null;
  const seen = new Set<string>();

  for (const suite of project.suites ?? []) {
    if (suiteSet && !suiteSet.has(suite.name)) continue;
    for (const testCase of suite.cases ?? []) {
      if (caseSet && !caseSet.has(testCase.name)) continue;
      for (const step of testCase.steps ?? []) {
        if (step.skip || !step.request) continue;
        const vars: Record<string, string> = {
          ...envVars,
          ...(suite.vars ?? {}),
          ...(testCase.vars ?? {}),
          ...(extraVars ?? {}),
          ...(step.vars ?? {}),
        };
        for (const key of ["url", "target", "address", "endpoint", "host"]) {
          const raw = step.request[key];
          if (typeof raw !== "string" || !raw.trim()) continue;
          const interp = raw.replace(/\$\{([^}:]+)\}/g, (m, k: string) => vars[k] ?? m);
          const { hosts, local } = extractEndpointHosts(interp);
          if (local) continue;
          for (const h of hosts) {
            if (h) seen.add(h);
          }
        }
      }
    }
  }
  const hosts = [...seen].sort();
  const refused = hosts.filter((h) => !isPrivateHost(h));
  return {
    hosts,
    refused,
    message: !allowRemote && refused.length > 0 ? refusalMessage(refused) : undefined,
  };
}

function shellArg(value: string): string {
  if (value !== "" && /^[A-Za-z0-9._:@/=+-]+$/.test(value)) return value;
  return `'${value.replace(/'/g, `'"'"'`)}'`;
}

function baseFileName(path: string): string {
  const cut = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return (cut >= 0 ? path.slice(cut + 1) : path).trim() || "project.yaml";
}

function formatDurationFlag(ms: number): string {
  if (ms > 0 && ms % 1000 === 0) return `${ms / 1000}s`;
  return `${ms}ms`;
}

/** Builds the CLI command string equivalent to the configured load run. */
export function loadCliCommand(projectPath: string, opts: LoadOptions): string {
  const parts = [`verity load ${shellArg(baseFileName(projectPath))}`];
  if (opts.environment) parts.push(`--env ${shellArg(opts.environment)}`);
  for (const s of opts.suites ?? []) parts.push(`--suite ${shellArg(s)}`);
  for (const c of opts.cases ?? []) parts.push(`--case ${shellArg(c)}`);
  parts.push(`--vus ${opts.vus}`);
  if (opts.durationMs && opts.durationMs > 0) {
    parts.push(`--duration ${formatDurationFlag(opts.durationMs)}`);
  } else if (opts.iterations && opts.iterations > 0) {
    parts.push(`--iterations ${opts.iterations}`);
  }
  if (opts.rampUpMs && opts.rampUpMs > 0) {
    parts.push(`--ramp-up ${formatDurationFlag(opts.rampUpMs)}`);
  }
  if (opts.thinkMs && opts.thinkMs > 0) {
    parts.push(`--think ${formatDurationFlag(opts.thinkMs)}`);
  }
  for (const th of opts.thresholds ?? []) {
    if (th.trim()) parts.push(`--threshold ${shellArg(th.trim())}`);
  }
  if (opts.maxErrorRate?.trim()) {
    parts.push(`--max-error-rate ${shellArg(opts.maxErrorRate.trim())}`);
  }
  if (opts.timeoutMs && opts.timeoutMs > 0) {
    parts.push(`--timeout ${formatDurationFlag(opts.timeoutMs)}`);
  }
  if (opts.allowRemote) {
    parts.push("--allow-remote");
  }
  return parts.join(" ");
}

const MILLIS_KEYS = new Set([
  "minMs",
  "meanMs",
  "p50Ms",
  "p90Ms",
  "p95Ms",
  "p99Ms",
  "maxMs",
  "actualMs",
  "limitMs",
]);

/** Formats a LoadReport as indented JSON with 3-decimal millisecond fields, matching core/load.WriteJSON. */
export function formatReportJson(report: LoadReport): string {
  const tokenPrefix = "__VERITY_MS_";
  const tokens = new Map<string, string>();
  let seq = 0;
  const raw = JSON.stringify(
    report,
    (key, value: unknown) => {
      if (MILLIS_KEYS.has(key) && typeof value === "number" && Number.isFinite(value)) {
        seq += 1;
        const tok = `${tokenPrefix}${seq}__`;
        tokens.set(`"${tok}"`, value.toFixed(3));
        return tok;
      }
      return value;
    },
    2,
  );
  let out = raw;
  for (const [k, v] of tokens) {
    out = out.replace(k, v);
  }
  return out + "\n";
}
