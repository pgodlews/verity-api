// Helpers for the environments editor.
//
// Refusal rules match the backend so a typed secret is stopped before
// SaveEnvironment: core/secrets.LooksLikeSecret and isSecretName in
// core/workspace/saveenv.go. A match is stored as ${env:NAME}, never as the
// literal the user typed.

const secretWords = ["password", "passwd", "secret", "token", "apikey", "api_key", "api-key", "private_key", "credential"];

/** The part of a value outside ${...} references: what would be stored literally. */
export function literalPart(value: string): string {
  return value.replace(/\$\{[^}]+\}/g, "").trim();
}

/** True when the backend treats this variable name as secret-like. */
export function isSecretName(key: string): boolean {
  const lower = key.toLowerCase();
  return secretWords.some((word) => lower.includes(word));
}

/**
 * True when the value itself looks like a hard-coded secret. Only the text
 * outside ${...} references counts ("hunter2${x}" still stores hunter2).
 */
export function looksLikeCredential(value: string): boolean {
  const trimmed = literalPart(value);
  if (trimmed === "") return false;
  if (/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(trimmed)) return true;
  if (/^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(trimmed)) return true;
  if (/^[A-Za-z0-9_-]{24,}$/.test(trimmed)) return true;
  return false;
}

/** True when SaveEnvironment would reject this pair. */
export function refusesLiteral(key: string, value: string): boolean {
  if (looksLikeCredential(value)) return true;
  return isSecretName(key) && literalPart(value) !== "";
}

/** Upper snake case environment-variable name suggested from a project key. */
export function suggestEnvVar(key: string): string {
  const split = key
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1_$2")
    .replace(/[^A-Za-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .replace(/_+/g, "_");
  return split.toUpperCase() || "VAR";
}

export function toEnvRef(name: string): string {
  return `\${env:${name}}`;
}

/** The NAME inside an exact ${env:NAME} value, ignoring surrounding space. */
export function envRefName(value: string): string | undefined {
  const match = value.trim().match(/^\$\{env:([^}]+)\}$/);
  return match?.[1];
}

/** ${env:NAME} for value when it is already one, otherwise the name suggested from key. */
export function storedEnvRef(key: string, value: string): string {
  return toEnvRef(envRefName(value) ?? suggestEnvVar(key));
}

/** Same sentence the backend returns, plus the environments that are blocked when several are saved together. */
export function credentialMessage(key: string, environments?: readonly string[]): string {
  const base = `${key} looks like a credential; store it as \${env:NAME} and set NAME on this machine`;
  if (!environments || environments.length === 0) return base;
  return `${base} (${environments.join(", ")})`;
}

export function withVariable(
  variables: Record<string, string> | undefined,
  key: string,
  value: string,
): Record<string, string> {
  return { ...(variables ?? {}), [key]: value };
}

export function withoutVariable(
  variables: Record<string, string> | undefined,
  key: string,
): Record<string, string> {
  const next = { ...(variables ?? {}) };
  delete next[key];
  return next;
}
