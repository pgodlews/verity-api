import type { AuthRef, ProjectInfo, RunResult, Step } from "../types";
import { parseJSONText } from "./jsonText";

export interface StepLocation { suite: string; caseName: string; index: number }
export const locationKey = (at: StepLocation) => JSON.stringify([at.suite, at.caseName, at.index]);
export const cloneStep = (step: Step): Step => structuredClone(step);
export const formatValue = (value: unknown): string => typeof value === "string" ? value : JSON.stringify(value, null, 2) ?? "";
export function asMap(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}
export function locate(info: ProjectInfo, at: StepLocation) {
  const suite = info.project.suites?.find(s => s.name === at.suite);
  const testCase = suite?.cases.find(c => c.name === at.caseName);
  return { suite, testCase, step: testCase?.steps[at.index] };
}
export function findResult(run: RunResult | null, at: StepLocation, name: string) {
  const cases = run?.suites.find(s => s.name === at.suite)?.cases.filter(c => c.name === at.caseName) ?? [];
  // Data feeds can produce several case results; show the latest iteration.
  const result = cases[cases.length - 1]?.steps[at.index];
  return result?.name === name ? result : undefined;
}
export function sentResponse(run: RunResult, at: StepLocation, targetName: string) {
  const cases = run.suites.find(s => s.name === at.suite)?.cases.filter(c => c.name === at.caseName) ?? [];
  const steps = cases[cases.length - 1]?.steps ?? [];
  const result = steps[steps.length - 1];
  const earlier = steps.slice(0, -1).filter(s => s.status !== "skipped" && s.status !== "aborted").map(s => s.name);
  let note = earlier.length ? `Ran ${earlier.join(", ")} first.` : "";
  if (result && steps.length < at.index + 1) note += ` Stopped at ${result.name} before ${targetName}.`;
  if (!result) note = "The run returned no step results.";
  if (cases.length > 1) note += ` Showing the last of ${cases.length} case iterations.`;
  return { result, note: note.trim(), run };
}
export function method(step: Step): string {
  return step.protocol === "graphql" ? "GQL" : step.protocol === "soap" ? "SOAP" : String(step.request?.method ?? (step.protocol === "rest" ? "GET" : step.protocol)).toUpperCase();
}
export function methodClass(step: Step): string {
  const name = method(step).toLowerCase();
  return `method method-${["get", "post", "put", "patch", "delete", "gql", "soap"].includes(name) ? name : "other"}`;
}
export function effectiveAuth(refs: (AuthRef | undefined)[]): string[] {
  return refs.reduce<string[]>((names, ref) => [...(ref?.inherit === false ? [] : names), ...(ref?.use ?? [])], []);
}
export function resolvePreview(text: string, vars: Record<string, string>, seen = new Set<string>()): string {
  return text.replace(/\$\{([^}]+)\}/g, (original, name: string) => {
    if (name.includes(":") || seen.has(name) || !Object.prototype.hasOwnProperty.call(vars, name)) return original;
    return resolvePreview(vars[name], vars, new Set([...seen, name]));
  });
}

export type BodyMode = "none" | "json" | "text" | "form";
export interface Draft {
  step: Step;
  bodyMode: BodyMode;
  bodyText: string;
  variablesText: string;
  // Imported JSON can live in request.body. Preserve that representation on save.
  jsonInBody: boolean;
}
export function makeDraft(step: Step): Draft {
  const copy = cloneStep(step), req = copy.request ?? {};
  let bodyMode: BodyMode = "none";
  if ("json" in req || "jsonText" in req) bodyMode = "json";
  else if ("form" in req) bodyMode = "form";
  else if ("body" in req) {
    bodyMode = "text";
    try { parseJSONText(String(req.body)); bodyMode = "json"; } catch { /* Plain text body. */ }
  }
  return {
    step: copy, bodyMode, jsonInBody: !("json" in req || "jsonText" in req),
    bodyText: "jsonText" in req ? String(req.jsonText) : "json" in req ? JSON.stringify(req.json, null, 2) : String(req.body ?? ""),
    variablesText: typeof req.variablesText === "string" ? req.variablesText : JSON.stringify(req.variables ?? {}, null, 2),
  };
}
export function materialize(draft: Draft): Step {
  const step = cloneStep(draft.step);
  const req = { ...step.request };
  if (step.protocol === "rest") {
    delete req.body; delete req.json; delete req.jsonText; delete req.form;
    if (draft.bodyMode === "json") {
      try { parseJSONText(draft.bodyText); } catch { throw new Error("Body must contain valid JSON before saving or sending."); }
      if (draft.jsonInBody) req.body = draft.bodyText; else req.jsonText = draft.bodyText;
    } else if (draft.bodyMode === "text") req.body = draft.bodyText;
    else if (draft.bodyMode === "form") req.form = draft.step.request?.form ?? {};
  } else if (step.protocol === "graphql") {
    try {
      if (parseJSONText(draft.variablesText).kind !== "object") throw new Error("not an object");
    } catch { throw new Error("GraphQL variables must contain valid JSON."); }
    delete req.variables;
    req.variablesText = draft.variablesText;
  } else if (step.protocol === "soap") req.body = draft.bodyText;
  step.request = req;
  return step;
}

export interface JsonLine { text: string; path?: string; value?: unknown }
export function jsonLines(value: unknown): JsonLine[] {
  const lines: JsonLine[] = [];
  function visit(item: unknown, path: string, depth: number, prefix = "", comma = "") {
    const indent = "  ".repeat(depth);
    if (item !== null && typeof item === "object") {
      const array = Array.isArray(item), entries = Object.entries(item);
      const open = array ? "[" : "{", close = array ? "]" : "}";
      lines.push({ text: indent + prefix + open + (entries.length ? "" : close + comma), path, value: item });
      entries.forEach(([key, child], i) => {
        const escaped = JSON.stringify(key).slice(1, -1).replace(/'/g, "\\'");
        const childPath = array ? `${path}[${key}]` : /^[A-Za-z_][\w]*$/.test(key) ? `${path}.${key}` : `${path}['${escaped}']`;
        visit(child, childPath, depth + 1, array ? "" : JSON.stringify(key) + ": ", i < entries.length - 1 ? "," : "");
      });
      if (entries.length) lines.push({ text: indent + close + comma });
    } else lines.push({ text: indent + prefix + JSON.stringify(item) + comma, path, value: item });
  }
  visit(value, "$", 0);
  return lines;
}
