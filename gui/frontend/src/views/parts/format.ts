import type { EnvRef } from "../../types";
import { formatJSONText } from "../../lib/jsonText";

export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}

export function baseName(path: string): string {
  const cut = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return (cut >= 0 ? path.slice(cut + 1) : path).trim();
}

export function dirName(path: string): string {
  const cut = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return cut >= 0 ? path.slice(0, cut) : "";
}

export function joinPath(dir: string, name: string): string {
  if (!dir) return name;
  if (dir.endsWith("/") || dir.endsWith("\\")) return dir + name;
  const sep = dir.includes("\\") && !dir.includes("/") ? "\\" : "/";
  return dir + sep + name;
}

/** File name for the saved project: the spec's name, with a .yaml extension. */
export function defaultYamlName(specPath: string): string {
  let base = baseName(specPath);
  const suffixes = [
    ".postman_collection.json",
    ".postman_environment.json",
    ".openapi.json",
    ".openapi.yaml",
    ".openapi.yml",
  ];
  const lower = base.toLowerCase();
  const hit = suffixes.find((suffix) => lower.endsWith(suffix));
  if (hit) base = base.slice(0, base.length - hit.length);
  else base = base.replace(/\.(json|ya?ml|wsdl|xml)$/i, "");
  base = base.trim() || "project";
  return `${base}.yaml`;
}

/** Output path next to the spec, unless Save as is already a path. */
export function resolveOut(spec: string, saveAs: string): string {
  const name = saveAs.trim();
  if (!name) return "";
  if (name.startsWith("/") || name.startsWith("\\") || /^[A-Za-z]:[\\/]/.test(name)) return name;
  if (name.includes("/") || name.includes("\\")) return name;
  return joinPath(dirName(spec), name);
}

export function shellArg(value: string): string {
  if (value !== "" && /^[A-Za-z0-9._:@/=+-]+$/.test(value)) return value;
  return `'${value.replace(/'/g, `'"'"'`)}'`;
}

export function cliCommand(projectPath: string, environment: string, options: { timeoutMs?: number; noRedirects?: boolean } = {}): string {
  const file = baseName(projectPath) || "project.yaml";
  const parts = [`verity run ${shellArg(file)}`];
  if (environment) parts.push(`--env ${shellArg(environment)}`);
  if (options.timeoutMs) parts.push(`--timeout ${options.timeoutMs}ms`);
  if (options.noRedirects) parts.push("--follow-redirects=false");
  return parts.join(" ");
}

export function formatMs(ms: number): string {
  if (!Number.isFinite(ms)) return "—";
  return `${Math.round(ms)} ms`;
}

export function formatWhen(iso?: string): string {
  if (!iso) return "";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

export function formatValue(value: unknown): string {
  if (value === undefined || value === null) return "—";
  if (typeof value === "string") return JSON.stringify(value);
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

export function prettyBody(body: string): { text: string; json: boolean } {
  const trimmed = body.trim();
  if (!trimmed) return { text: "", json: false };
  try {
    return { text: formatJSONText(trimmed), json: true };
  } catch {
    return { text: body, json: false };
  }
}

export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message || "Something went wrong";
  if (typeof err === "string" && err.trim()) return err;
  if (err && typeof err === "object") {
    const record = err as { message?: unknown; code?: unknown };
    const message = typeof record.message === "string" ? record.message : "";
    const code = typeof record.code === "string" ? record.code : "";
    if (code && message) return `${code}: ${message}`;
    if (message) return message;
    if (code) return code;
  }
  return "Something went wrong";
}

/** Import save reports an existing file with code "exists" (or that word). */
export function isExistsError(err: unknown): boolean {
  if (err && typeof err === "object" && "code" in err && (err as { code?: unknown }).code === "exists") {
    return true;
  }
  return /\bexists\b/i.test(errorMessage(err));
}

export function envRefStatus(name: string, envRefs: EnvRef[] | undefined): EnvRef | undefined {
  return envRefs?.find((ref) => ref.name === name);
}

export async function copyText(text: string): Promise<boolean> {
  try {
    if (!navigator.clipboard?.writeText) return false;
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
