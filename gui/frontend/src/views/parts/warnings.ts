const SECRET = /set environment variable\s+([A-Za-z_][A-Za-z0-9_]*)/;
const NOT_CONVERTED = /not converted|not supported/i;

export interface SecretWarning {
  name: string;
  source: string;
  raw: string;
}

export interface NotedWarning {
  where?: string;
  detail: string;
  snippet?: string;
  raw: string;
}

export interface WarningGroups {
  secrets: SecretWarning[];
  notConverted: NotedWarning[];
  look: NotedWarning[];
}

function splitWhere(text: string): { where?: string; detail: string } {
  const at = text.indexOf(": ");
  if (at > 0 && at < 96) return { where: text.slice(0, at), detail: text.slice(at + 2) };
  return { detail: text };
}

function takeSnippet(detail: string): { detail: string; snippet?: string } {
  const match = detail.match(/\s*\(first:\s*([\s\S]+)\)\s*$/);
  if (!match) return { detail };
  return { detail: detail.slice(0, match.index).trim(), snippet: match[1].trim() };
}

function note(raw: string): NotedWarning {
  const split = splitWhere(raw);
  const body = takeSnippet(split.detail);
  return { where: split.where, detail: body.detail, snippet: body.snippet, raw };
}

export function groupWarnings(warnings: string[]): WarningGroups {
  const groups: WarningGroups = { secrets: [], notConverted: [], look: [] };
  for (const raw of warnings) {
    const secret = raw.match(SECRET);
    if (secret) {
      const source = raw
        .replace(/;?\s*set environment variable\s+[A-Za-z_][A-Za-z0-9_]*.*$/i, "")
        .replace(/[;\s]+$/, "")
        .trim();
      groups.secrets.push({ name: secret[1], source: source || raw, raw });
      continue;
    }
    if (NOT_CONVERTED.test(raw)) {
      groups.notConverted.push(note(raw));
      continue;
    }
    groups.look.push(note(raw));
  }
  return groups;
}
