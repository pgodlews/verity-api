const RECENTS = "verity.recent-projects";
export function recentProjects(): string[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(RECENTS) ?? "[]");
    return Array.isArray(value) ? value.filter((v): v is string => typeof v === "string" && !!v).slice(0, 8) : [];
  } catch { return []; }
}
export function rememberProject(path: string): string[] {
  const recent = [path, ...recentProjects().filter(p => p !== path)].slice(0, 8);
  try { localStorage.setItem(RECENTS, JSON.stringify(recent)); } catch { /* Storage may be disabled. */ }
  return recent;
}
export function initialTheme(): "dark" | "light" {
  try { return localStorage.getItem("verity.theme") === "light" ? "light" : "dark"; } catch { return "dark"; }
}
export function applyTheme(theme: "dark" | "light") {
  document.documentElement.dataset.theme = theme;
  try { localStorage.setItem("verity.theme", theme); } catch { /* The in-memory theme still works. */ }
}
export const fileName = (path: string) => path.split(/[\\/]/).pop() || path;

/** Run-level options from the Runs view (per user, this device only). */
export interface RunSettings { timeoutMs?: number; noRedirects?: boolean }
const RUN_SETTINGS = "verity.runSettings";
export function loadRunSettings(): RunSettings {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(RUN_SETTINGS) ?? "{}");
    if (!value || typeof value !== "object") return {};
    const { timeoutMs, noRedirects } = value as Record<string, unknown>;
    return {
      ...(typeof timeoutMs === "number" && timeoutMs > 0 ? { timeoutMs } : {}),
      ...(noRedirects === true ? { noRedirects } : {}),
    };
  } catch { return {}; }
}
export function saveRunSettings(settings: RunSettings) {
  try { localStorage.setItem(RUN_SETTINGS, JSON.stringify(settings)); } catch { /* The in-memory settings still apply. */ }
}
