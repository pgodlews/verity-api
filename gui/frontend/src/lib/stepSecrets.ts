// Plaintext credentials in a step draft.
//
// CheckStep (core/secretscan) reports path, key, reason and a suggested
// env var — never the value. These helpers map a finding onto the request
// editor and replace the literal in the draft with ${env:NAME}. An
// Authorization value keeps its scheme word ("Bearer ${env:NAME}"), matching
// the scheme core/secretscan strips before it decides there is a literal.

import { toEnvRef } from "../views/parts/env";
import type { SecretFinding, Step } from "../types";
import type { Draft } from "./workspace";
import { cloneStep } from "./workspace";

/** Debounce for CheckStep while a draft is dirty. */
export const SECRET_CHECK_DELAY_MS = 300;

export const SECRET_FIELD_COPY =
  "Looks like a credential. Project files are committed to git, so keep it out of them.";

export type RequestSection = "params" | "headers" | "body" | "vars";

/** Where a finding is edited. "other" is anything the section editors do not show inline. */
export type SecretPlace =
  | "header"
  | "query"
  | "form"
  | "var"
  | "json"
  | "variables"
  | "body"
  | "graphql-query"
  | "form-nested"
  | "url"
  | "other";

const AUTH_SCHEME = /^(bearer|basic|digest|token|apikey|api-key|negotiate|ntlm|aws4-hmac-sha256)\s+/i;

export function isAuthorizationKey(key: string): boolean {
  return key.toLowerCase().replace(/[-_.\s]/g, "").includes("authorization");
}

export type SecretTarget = "env" | "keychain";

/**
 * Store an environment or keychain reference in place of a literal, preserving
 * an Authorization scheme word and the whitespace that follows it.
 */
export function secretReplacement(key: string, value: string, envVar: string, target: SecretTarget = "env"): string {
  const ref = target === "keychain" ? "${secret:keychain:" + envVar + "}" : toEnvRef(envVar);
  if (!isAuthorizationKey(key)) return ref;
  const scheme = value.match(AUTH_SCHEME)?.[0];
  return scheme ? scheme + ref : ref;
}

export function secretHint(envVar: string): string {
  return `Set ${envVar} on this machine or in an untracked .env file (verity run --env-file .env)`;
}

export function storeAsLabel(envVar: string): string {
  return `Store as ${toEnvRef(envVar)}`;
}

export function credentialBadgeLabel(count: number): string {
  return count === 1 ? "1 value looks like a credential" : `${count} values look like credentials`;
}

export function credentialSaveLine(count: number): string {
  if (count === 1) return "1 value looks like a credential — store it in the OS keychain or as an environment variable to save.";
  return `${count} values look like credentials — store them in the OS keychain or as environment variables to save.`;
}

export function secretPlace(path: readonly string[]): SecretPlace {
  if (path[0] === "vars") return path.length === 2 ? "var" : "other";
  if (path[0] !== "request" || path.length < 2) return "other";
  switch (path[1]) {
    case "url":
      return path.length === 2 ? "url" : "other";
    case "headers":
      return path.length === 3 ? "header" : "other";
    case "query":
      if (path.length === 3) return "query";
      if (path.length === 2) return "graphql-query";
      return "other";
    case "form":
      if (path.length === 3) return "form";
      if (path.length > 3) return "form-nested";
      return "other";
    case "json":
      return path.length >= 3 ? "json" : "other";
    case "variables":
      return path.length >= 3 ? "variables" : "other";
    case "body":
      return "body";
    default:
      return "other";
  }
}

/** Request-editor tab that should badge this finding. Unmapped paths return undefined. */
export function findingTab(path: readonly string[]): RequestSection | undefined {
  switch (secretPlace(path)) {
    case "header":
      return "headers";
    case "query":
      return "params";
    case "var":
      return "vars";
    case "form":
    case "form-nested":
    case "json":
    case "variables":
    case "body":
    case "graphql-query":
      return "body";
    default:
      return undefined;
  }
}

export function findingCountsByTab(findings: readonly SecretFinding[]): Partial<Record<RequestSection, number>> {
  const counts: Partial<Record<RequestSection, number>> = {};
  for (const finding of findings) {
    const tab = findingTab(finding.path);
    if (!tab) continue;
    counts[tab] = (counts[tab] ?? 0) + 1;
  }
  return counts;
}

export function findingsWhere(findings: readonly SecretFinding[], place: SecretPlace): SecretFinding[] {
  return findings.filter((finding) => secretPlace(finding.path) === place);
}

/** envVar by row key for a flat map (headers, query, form, vars). */
export function secretEnvVars(findings: readonly SecretFinding[], place: SecretPlace): Record<string, string> {
  const out: Record<string, string> = {};
  for (const finding of findingsWhere(findings, place)) {
    const key = finding.path[finding.path.length - 1];
    if (key) out[key] = finding.envVar;
  }
  return out;
}

/** JSON path from the body or variables object, e.g. $.user.password. Never includes a value. */
export function jsonPointerLabel(path: readonly string[]): string {
  let out = "$";
  for (const part of path.slice(2)) {
    if (/^\d+$/.test(part)) out += `[${part}]`;
    else if (/^[A-Za-z_][A-Za-z0-9_]*$/.test(part)) out += `.${part}`;
    else out += `[${JSON.stringify(part)}]`;
  }
  return out;
}

/** Dotted path from the step root, e.g. request.headers.X-Api-Key. */
export function findingPathString(path: readonly string[]): string {
  let out = "";
  for (const part of path) {
    if (/^\d+$/.test(part)) out += `[${part}]`;
    else out += (out ? "." : "") + part;
  }
  return out;
}

/** Label shown on a banner. Flat rows omit it; the key is already in the row. */
export function findingBannerLabel(path: readonly string[]): string | undefined {
  switch (secretPlace(path)) {
    case "json":
    case "variables":
    case "form-nested":
      return jsonPointerLabel(path);
    case "body":
      return path.length > 2 ? jsonPointerLabel(path) : undefined;
    case "other":
      return findingPathString(path);
    default:
      return undefined;
  }
}

function isScalar(value: unknown): boolean {
  return typeof value === "string" || typeof value === "number" || typeof value === "boolean";
}

function scalarString(value: unknown): string {
  return typeof value === "string" ? value : String(value);
}

function getIn(value: unknown, path: readonly string[]): unknown {
  let current = value;
  for (const key of path) {
    if (Array.isArray(current)) {
      const index = Number(key);
      if (!Number.isInteger(index)) return undefined;
      current = current[index];
    } else if (current !== null && typeof current === "object") {
      current = (current as Record<string, unknown>)[key];
    } else return undefined;
  }
  return current;
}

/** Clone along path and set the leaf. Returns undefined when a segment is missing. */
function setIn(value: unknown, path: readonly string[], next: string): unknown | undefined {
  if (path.length === 0) return next;
  const [head, ...tail] = path;
  if (Array.isArray(value)) {
    const index = Number(head);
    if (!Number.isInteger(index) || index < 0 || index >= value.length) return undefined;
    const child = setIn(value[index], tail, next);
    if (child === undefined) return undefined;
    const copy = value.slice();
    copy[index] = child;
    return copy;
  }
  if (value === null || typeof value !== "object") return undefined;
  if (!Object.prototype.hasOwnProperty.call(value, head)) return undefined;
  const record = value as Record<string, unknown>;
  const child = setIn(record[head], tail, next);
  if (child === undefined) return undefined;
  return { ...record, [head]: child };
}

/** Character spans of every value at path in JSON text (duplicate keys included). */
export function jsonValueSpans(text: string, path: readonly string[]): Array<[number, number]> {
  const spans: Array<[number, number]> = [];
  let i = 0;
  const ws = () => { while (i < text.length && /\s/.test(text[i])) i++; };
  const str = (): string => {
    const start = i;
    i++; // opening quote
    while (i < text.length && text[i] !== '"') i += text[i] === "\\" ? 2 : 1;
    i++; // closing quote
    return JSON.parse(text.slice(start, i)) as string;
  };
  const value = (at: readonly string[]): void => {
    ws();
    const match = at.length === path.length && at.every((p, n) => p === path[n]);
    const start = i;
    const c = text[i];
    if (c === "{") {
      i++; ws();
      while (i < text.length && text[i] !== "}") {
        ws(); const key = str(); ws(); i++; // colon
        value([...at, key]); ws();
        if (text[i] === ",") i++;
        ws();
      }
      i++;
    } else if (c === "[") {
      i++; ws();
      for (let n = 0; i < text.length && text[i] !== "]"; n++) {
        value([...at, String(n)]); ws();
        if (text[i] === ",") i++;
        ws();
      }
      i++;
    } else if (c === '"') {
      str();
    } else {
      while (i < text.length && !/[\s,\]}]/.test(text[i])) i++;
    }
    if (match) spans.push([start, i]);
  };
  try { value([]); } catch { return []; }
  return spans;
}

/**
 * Replace the values at rest in JSON text, editing only those characters:
 * every other byte (formatting, large numbers JavaScript would round) stays.
 */
function replaceJsonText(text: string, rest: readonly string[], key: string, envVar: string, target: SecretTarget): string | undefined {
  try { JSON.parse(text); } catch { return undefined; }
  const spans = jsonValueSpans(text, rest);
  if (!spans.length) return undefined;
  let out = text;
  for (const [start, end] of [...spans].reverse()) {
    const current = JSON.parse(out.slice(start, end)) as unknown;
    if (!isScalar(current)) return undefined;
    out = out.slice(0, start) + JSON.stringify(secretReplacement(key, scalarString(current), envVar, target)) + out.slice(end);
  }
  return out;
}

/**
 * Replace the literal at finding.path with an environment or keychain reference.
 * Return the same draft when the path is missing or the leaf is not a scalar.
 * Do not copy the previous value into any new field.
 */
export function applySecretReplacement(
  draft: Draft,
  finding: Pick<SecretFinding, "path" | "key" | "envVar" | "inText">,
  target: SecretTarget = "env",
): Draft {
  // A literal inside a URL or an XML/form body is edited by hand.
  if (finding.inText) return draft;
  const path = finding.path;
  if (path[0] === "request" && (path[1] === "json" || path[1] === "variables" || path[1] === "body")) {
    const field = path[1] === "variables" ? "variablesText" : "bodyText";
    const rest = path.slice(2);
    const text = path[1] === "body" && rest.length === 0
      ? secretReplacement(finding.key, draft.bodyText, finding.envVar, target)
      : replaceJsonText(draft[field], rest, finding.key, finding.envVar, target);
    if (text === undefined) return draft;
    const step = cloneStep(draft.step);
    const req = { ...(step.request ?? {}) };
    if (path[1] === "json") {
      delete req.json; delete req.body;
      req.jsonText = text;
    } else if (path[1] === "variables") {
      delete req.variables;
      req.variablesText = text;
    } else {
      // text is the whole body after the replacement; never keep the old
      // plaintext in the step, even when only a nested JSON value changed.
      req.body = text;
    }
    step.request = req;
    return { ...draft, step, [field]: text };
  }

  const step = cloneStep(draft.step);
  const current = getIn(step, path);
  if (!isScalar(current)) return draft;
  const updated = setIn(step, path, secretReplacement(finding.key, scalarString(current), finding.envVar, target));
  if (updated === undefined || typeof updated !== "object") return draft;
  return { ...draft, step: updated as Step };
}

/** Hint for a literal inside a larger string, which has no one-click fix. */
export function inTextHint(key: string, envVar: string): string {
  return `Replace the ${key} value in this text with ${toEnvRef(envVar)} by hand, then set ${envVar} on this machine or in an untracked .env file.`;
}

/** Extract just the credential; Authorization scheme words stay in the draft. */
export function keychainValue(draft: Draft, finding: SecretFinding): string {
  if (finding.inText) throw new Error("Replace credentials inside text by hand.");
  const path = finding.path;
  let values: unknown[];
  if (path[0] === "request" && ["json", "variables", "body"].includes(path[1])) {
    const text = path[1] === "variables" ? draft.variablesText : draft.bodyText;
    if (path[1] === "body" && path.length === 2) values = [text];
    else {
      try {
        JSON.parse(text);
        values = jsonValueSpans(text, path.slice(2)).map(([start, end]) => {
          const raw = text.slice(start, end);
          const parsed: unknown = JSON.parse(raw);
          // Preserve numeric credentials without JavaScript rounding.
          return typeof parsed === "number" ? raw : parsed;
        });
      } catch { throw new Error("Fix the JSON before storing this credential."); }
    }
  } else values = [getIn(draft.step, path)];
  if (!values.length || values.some(value => !isScalar(value))) {
    throw new Error("This credential field is no longer available.");
  }
  const credentials = values.map(value => {
    const text = scalarString(value);
    return isAuthorizationKey(finding.key) ? text.replace(AUTH_SCHEME, "") : text;
  });
  if (new Set(credentials).size !== 1) throw new Error("Duplicate JSON keys contain different credentials; edit them separately.");
  if (!credentials[0] || /\$\{[^}]+\}/.test(credentials[0])) throw new Error("Store a complete credential value without references.");
  return credentials[0];
}

/** Signals that the UI must ask before replacing this named item. */
export class KeychainReplacementRequired extends Error {
  constructor(readonly itemName: string) { super(`Replace keychain item "${itemName}"?`); }
}

/** A failed store leaves the original draft intact. Values never enter errors. */
export async function storeDraftInKeychain(
  draft: Draft, finding: SecretFinding, name: string,
  store: (name: string, value: string) => Promise<string>,
  overwrite?: { exists: (name: string) => Promise<boolean>; confirmed?: boolean },
): Promise<Draft> {
  const value = keychainValue(draft, finding);
  const next = applySecretReplacement(draft, { ...finding, envVar: name }, "keychain");
  if (next === draft) throw new Error("This credential field is no longer available.");
  if (overwrite && !overwrite.confirmed && await overwrite.exists(name)) {
    throw new KeychainReplacementRequired(name);
  }
  await store(name, value);
  return next;
}
