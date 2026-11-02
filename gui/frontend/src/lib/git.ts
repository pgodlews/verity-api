// Pure helpers for the git UI. The desktop app fills GitInfo and
// ProjectChanges; these functions only decide how to show them.

import type { StepLocation } from "./workspace";
import type { FileHash, GitStepRef, PartChange, Project, ProjectInfo, StepChange, StepOrigin } from "../types";

export interface DiffLine {
  kind: "same" | "add" | "del";
  text: string;
}

export interface TabSnap {
  /** Stable per tab. Two tabs never share one, even when a step index collides. */
  id?: string;
  suite: string;
  caseName: string;
  index: number;
  name: string;
  /** Zero-based count of same-named steps before this one when the tab was bound. */
  occurrence: number;
  /** How many steps shared that name when the tab was bound. */
  siblings: number;
  /** HashStep of the step as loaded. Empty skips content matching. */
  stepHash: string;
  dirty: boolean;
  file: string;
}

/** keep leaves the draft. reload replaces it from the project. close drops the tab.
 *  detach keeps the draft but it must not be saved: its step is gone or would
 *  share a save target with another open tab. */
export type TabPlan =
  | { kind: "keep"; at: StepLocation; banner: boolean; occurrence: number; siblings: number; stepHash: string }
  | { kind: "reload"; at: StepLocation; occurrence: number; siblings: number; stepHash: string }
  | { kind: "close"; name: string }
  | { kind: "detach"; banner: boolean };

export interface ChangeGroup {
  suite: string;
  cases: { name: string; steps: StepChange[] }[];
}

interface ResolvedStep {
  at: StepLocation;
  occurrence: number;
  siblings: number;
  stepHash: string;
  /** True when this occurrence is still the slot, but the bytes changed. */
  changed: boolean;
}

/** changedFiles null means this refresh was not an external edit (a save). */
export function planDiskReload(tabs: TabSnap[], project: Project, changedFiles: string[] | null, stepFiles?: StepOrigin[]): TabPlan[] {
  const external = changedFiles != null;
  const owners = new Map<string, string>();
  return tabs.map((tab, index) => {
    const id = tab.id || `${index}`;
    const resolved = resolveTab(project, tab, stepFiles);
    const fileHit = tab.file !== "" && (changedFiles ?? []).some((file) => samePath(file, tab.file));
    if (!resolved) {
      if (tab.dirty) return { kind: "detach", banner: true };
      if (external) return { kind: "close", name: tab.name };
      return kept(tab, false);
    }
    const key = locationKey(resolved.at);
    if (owners.has(key)) return { kind: "detach", banner: true };
    owners.set(key, id);
    const banner = tab.dirty && external && (fileHit || resolved.changed);
    const stepHash = banner ? tab.stepHash : (resolved.stepHash || tab.stepHash);
    if (tab.dirty) return { kind: "keep", at: resolved.at, banner, occurrence: resolved.occurrence, siblings: resolved.siblings, stepHash };
    return { kind: "reload", at: resolved.at, occurrence: resolved.occurrence, siblings: resolved.siblings, stepHash: resolved.stepHash || tab.stepHash };
  });
}

function kept(tab: TabSnap, banner: boolean): TabPlan {
  return {
    kind: "keep",
    at: { suite: tab.suite, caseName: tab.caseName, index: tab.index },
    banner,
    occurrence: tab.occurrence,
    siblings: tab.siblings,
    stepHash: tab.stepHash,
  };
}

function locationKey(at: StepLocation): string {
  return JSON.stringify([at.suite, at.caseName, at.index]);
}

/** Match a tab to the step it was bound to. Occurrence is the identity when
 *  the number of same-named steps is unchanged. When that count changes, only
 *  a unique content hash may follow the step. Two tabs must not be given the
 *  same index; planDiskReload detaches the second. */
export function resolveTab(project: Project, tab: Pick<TabSnap, "suite" | "caseName" | "name" | "occurrence" | "siblings" | "stepHash">, stepFiles?: StepOrigin[]): ResolvedStep | null {
  const named = namedSteps(project, tab.suite, tab.caseName, stepFiles).filter((step) => step.name === tab.name);
  const finish = (hit: Named, changed: boolean): ResolvedStep => ({
    at: { suite: tab.suite, caseName: tab.caseName, index: hit.index },
    occurrence: named.indexOf(hit),
    siblings: named.length,
    stepHash: hit.hash,
    changed,
  });
  if (named.length === tab.siblings) {
    const hit = named[tab.occurrence];
    if (!hit) return null;
    const changed = Boolean(tab.stepHash && hit.hash && hit.hash !== tab.stepHash);
    return finish(hit, changed);
  }
  if (tab.stepHash) {
    const same = named.filter((step) => step.hash === tab.stepHash);
    if (same.length === 1) return finish(same[0], false);
  }
  return null;
}

interface Named { index: number; name: string; hash: string }

function namedSteps(project: Project, suite: string, caseName: string, stepFiles?: StepOrigin[]): Named[] {
  const steps = project.suites?.find((item) => item.name === suite)?.cases.find((item) => item.name === caseName)?.steps ?? [];
  return steps.map((step, index) => ({
    index,
    name: step.name,
    hash: stepFiles?.find((item) => item.suite === suite && item.case === caseName && item.index === index)?.hash ?? "",
  }));
}

/** The occurrence-th step with this name. There is no fallback to a different occurrence. */
export function findStepByIdentity(project: Project, suite: string, caseName: string, name: string, occurrence: number): StepLocation | null {
  const named = namedSteps(project, suite, caseName).filter((step) => step.name === name);
  const hit = named[occurrence];
  if (!hit) return null;
  return { suite, caseName, index: hit.index };
}

export function stepIdentity(info: ProjectInfo, at: StepLocation): { occurrence: number; siblings: number; hash: string; file: string } {
  const steps = info.project.suites?.find((item) => item.name === at.suite)?.cases.find((item) => item.name === at.caseName)?.steps ?? [];
  const name = steps[at.index]?.name ?? "";
  let occurrence = 0;
  let siblings = 0;
  for (let i = 0; i < steps.length; i++) {
    if (steps[i]?.name !== name) continue;
    if (i === at.index) occurrence = siblings;
    siblings++;
  }
  const origin = info.stepFiles?.find((item) => item.suite === at.suite && item.case === at.caseName && item.index === at.index);
  return { occurrence, siblings, hash: origin?.hash ?? "", file: origin?.file || info.path };
}

/** sha256 of each project file, keyed by path. Undefined when inspect did not send any. */
export function fileBaseline(info: { fileHashes?: FileHash[] }): Record<string, string> | undefined {
  if (!info.fileHashes?.length) return undefined;
  const out: Record<string, string> = {};
  for (const row of info.fileHashes) out[row.path] = row.hash;
  return out;
}

export function stepBaseline(info: { stepFiles?: StepOrigin[] }, suite: string, caseName: string, index: number): string {
  return info.stepFiles?.find((item) => item.suite === suite && item.case === caseName && item.index === index)?.hash ?? "";
}

/** sha256 of one project file from inspect. Empty when that file was not hashed. */
export function fileHashOf(info: { path?: string; fileHashes?: FileHash[] }, file: string): string {
  const want = file || info.path || "";
  if (!want) return "";
  return info.fileHashes?.find((row) => samePath(row.path, want))?.hash ?? "";
}

/** What Keep my edits adopts. A missing step hash falls back to the file hash. */
export function keepEdits(info: ProjectInfo, at: StepLocation): { stepHash: string; fileBaseline: string; file: string; occurrence: number; siblings: number } {
  const ident = stepIdentity(info, at);
  if (ident.hash) {
    return { stepHash: ident.hash, fileBaseline: "", file: ident.file, occurrence: ident.occurrence, siblings: ident.siblings };
  }
  return {
    stepHash: "",
    fileBaseline: fileHashOf(info, ident.file),
    file: ident.file,
    occurrence: ident.occurrence,
    siblings: ident.siblings,
  };
}

/** Paths whose content hash changed between two inspect results, plus paths that disappeared. */
export function changedFilePaths(before: { fileHashes?: FileHash[] } | null | undefined, after: { fileHashes?: FileHash[] } | null | undefined): string[] {
  const previous = new Map((before?.fileHashes ?? []).map((row) => [row.path, row.hash]));
  const seen = new Set<string>();
  const out: string[] = [];
  for (const row of after?.fileHashes ?? []) {
    seen.add(row.path);
    if (previous.get(row.path) !== row.hash) out.push(row.path);
  }
  for (const path of previous.keys()) {
    if (!seen.has(path)) out.push(path);
  }
  return out;
}

/** Steps, environments, auth, suite and case scope, project meta, and other files. */
export function reportedChangeCount(data: {
  steps?: unknown[];
  environments?: unknown[];
  auth?: unknown[];
  suites?: unknown[];
  cases?: unknown[];
  meta?: unknown[];
  otherFiles?: number;
} | null | undefined): number {
  if (!data) return 0;
  return (data.steps?.length ?? 0) + (data.environments?.length ?? 0) + (data.auth?.length ?? 0)
    + (data.suites?.length ?? 0) + (data.cases?.length ?? 0) + (data.meta?.length ?? 0) + (data.otherFiles ?? 0);
}

export function nextGeneration(current: number): number {
  return current + 1;
}

export function isCurrentGeneration(request: number, current: number): boolean {
  return request === current;
}

/** SaveStep/SaveEnvironment refuse with verr code "conflict" and write nothing. */
export function isConflictError(err: unknown): boolean {
  if (err && typeof err === "object" && "code" in err && (err as { code?: unknown }).code === "conflict") return true;
  const text = err instanceof Error ? err.message : String(err ?? "");
  return /\bconflict\b/i.test(text);
}

export function samePath(a: string, b: string): boolean {
  return normalizePath(a) === normalizePath(b);
}

function normalizePath(path: string): string {
  return path.replace(/\\/g, "/").replace(/\/+$/, "");
}

export function stepFileOf(info: ProjectInfo, at: StepLocation): string {
  const hit = info.stepFiles?.find((item) => item.suite === at.suite && item.case === at.caseName && item.index === at.index);
  return hit?.file || info.path;
}

/** M modified (including moved), A added. Removed requests are not in the tree. */
export function stepMarker(steps: GitStepRef[] | undefined, suite: string, caseName: string, index: number, name: string): "M" | "A" | null {
  const hit = (steps ?? []).find((step) => step.suite === suite && step.case === caseName && step.index === index && step.name === name);
  if (!hit) return null;
  if (hit.state === "added") return "A";
  if (hit.state === "modified" || hit.state === "moved") return "M";
  return null;
}

export function groupStepChanges(steps: StepChange[] | undefined): ChangeGroup[] {
  const groups: ChangeGroup[] = [];
  for (const step of steps ?? []) {
    let suite = groups.find((group) => group.suite === step.suite);
    if (!suite) {
      suite = { suite: step.suite, cases: [] };
      groups.push(suite);
    }
    let testCase = suite.cases.find((item) => item.name === step.case);
    if (!testCase) {
      testCase = { name: step.case, steps: [] };
      suite.cases.push(testCase);
    }
    testCase.steps.push(step);
  }
  return groups;
}

export function partLabel(part: Pick<PartChange, "part" | "key">): string {
  return part.key ? `${part.part}[${part.key}]` : part.part;
}

export function branchLabel(info: { detached?: boolean; short?: string; branch?: string }): string {
  if (info.detached) {
    return info.short ? `detached ${info.short}` : "detached";
  }
  return info.branch || "";
}

export function changedCount(info: { files?: unknown[] }): number {
  return info.files?.length ?? 0;
}

/** Line-level diff. A trailing newline does not create an extra blank line. */
export function lineDiff(before: string, after: string): DiffLine[] {
  const a = splitLines(before);
  const b = splitLines(after);
  if (a.length * b.length > 40000) {
    return [
      ...a.map((text) => ({ kind: "del" as const, text })),
      ...b.map((text) => ({ kind: "add" as const, text })),
    ];
  }
  const dp: number[][] = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ kind: "same", text: a[i] });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push({ kind: "del", text: a[i] });
      i++;
    } else {
      out.push({ kind: "add", text: b[j] });
      j++;
    }
  }
  while (i < a.length) out.push({ kind: "del", text: a[i++] });
  while (j < b.length) out.push({ kind: "add", text: b[j++] });
  return out;
}

function splitLines(text: string): string[] {
  if (text === "") return [];
  const lines = text.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  return lines;
}
