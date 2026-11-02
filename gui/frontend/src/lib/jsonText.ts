export type JSONTextNode = {
  kind: "object" | "array" | "string" | "number" | "literal";
  raw: string;
  value?: unknown;
  entries?: Array<{ key: string; keyRaw: string; value: JSONTextNode }>;
  elements?: JSONTextNode[];
};

export function parseJSONText(text: string): JSONTextNode {
  let at = 0;
  const whitespace = () => { while (at < text.length && /[\x20\t\r\n]/.test(text[at])) at++; };
  const string = (): { raw: string; value: string } => {
    const start = at++;
    while (at < text.length) {
      const c = text[at++];
      if (c === '"') {
        const raw = text.slice(start, at);
        return { raw, value: JSON.parse(raw) as string };
      }
      if (c === "\\") at++;
      else if (c.charCodeAt(0) < 0x20) throw new Error("control character in JSON string");
    }
    throw new Error("unterminated JSON string");
  };
  const value = (): JSONTextNode => {
    whitespace();
    const start = at;
    const c = text[at];
    if (c === '"') {
      const parsed = string();
      return { kind: "string", raw: parsed.raw, value: parsed.value };
    }
    if (c === "{") {
      at++;
      const entries: NonNullable<JSONTextNode["entries"]> = [];
      whitespace();
      if (text[at] === "}") at++;
      else {
        if (at >= text.length) throw new Error("unterminated JSON container");
        while (at < text.length) {
          whitespace();
          if (text[at] !== '"') throw new Error("JSON object key must be a string");
          const key = string();
          whitespace();
          if (text[at++] !== ":") throw new Error("missing colon in JSON object");
          entries.push({ key: key.value, keyRaw: key.raw, value: value() });
          whitespace();
          if (text[at] === "}") { at++; break; }
          if (text[at++] !== ",") throw new Error("missing comma in JSON object");
        }
        if (text[at - 1] !== "}") throw new Error("unterminated JSON container");
      }
      return { kind: "object", raw: text.slice(start, at), entries };
    }
    if (c === "[") {
      at++;
      const elements: JSONTextNode[] = [];
      whitespace();
      if (text[at] === "]") at++;
      else {
        if (at >= text.length) throw new Error("unterminated JSON container");
        while (at < text.length) {
          elements.push(value());
          whitespace();
          if (text[at] === "]") { at++; break; }
          if (text[at++] !== ",") throw new Error("missing comma in JSON array");
        }
        if (text[at - 1] !== "]") throw new Error("unterminated JSON container");
      }
      return { kind: "array", raw: text.slice(start, at), elements };
    }
    if (text.startsWith("true", at) || text.startsWith("false", at) || text.startsWith("null", at)) {
      const raw = text.startsWith("true", at) ? "true" : text.startsWith("false", at) ? "false" : "null";
      at += raw.length;
      return { kind: "literal", raw, value: raw === "null" ? null : raw === "true" };
    }
    const match = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(text.slice(at));
    if (match) {
      at += match[0].length;
      return { kind: "number", raw: match[0] };
    }
    throw new Error("invalid JSON value");
  };
  const parsed = value();
  whitespace();
  if (at !== text.length) throw new Error("unexpected text after JSON value");
  return { ...parsed, raw: text };
}

export function formatJSONText(text: string): string {
  const root = parseJSONText(text);
  const render = (node: JSONTextNode, depth: number): string => {
    const indent = "  ".repeat(depth), childIndent = "  ".repeat(depth + 1);
    if (node.kind === "object") {
      const entries = node.entries ?? [];
      if (!entries.length) return "{}";
      return `{\n${entries.map(entry => `${childIndent}${entry.keyRaw}: ${render(entry.value, depth + 1)}`).join(",\n")}\n${indent}}`;
    }
    if (node.kind === "array") {
      const elements = node.elements ?? [];
      if (!elements.length) return "[]";
      return `[\n${elements.map(element => childIndent + render(element, depth + 1)).join(",\n")}\n${indent}]`;
    }
    return node.raw;
  };
  return render(root, 0);
}

function rawJSON(text: string): unknown {
  const value = Number(text);
  if (!Number.isFinite(value) || Object.is(value, -0) || (Number.isInteger(value) && !Number.isSafeInteger(value))) return text;
  return value;
}

export function jsonTextValue(node: JSONTextNode, values = new WeakMap<JSONTextNode, unknown>()): unknown {
  if (node.kind === "number") return rawJSON(node.raw);
  if (values.has(node)) return values.get(node);
  if (node.kind === "object") {
    const out = Object.create(null) as Record<string, unknown>;
    values.set(node, out);
    for (const entry of node.entries ?? []) out[entry.key] = jsonTextValue(entry.value, values);
    return out;
  }
  if (node.kind === "array") {
    const out: unknown[] = [];
    values.set(node, out);
    for (const element of node.elements ?? []) out.push(jsonTextValue(element, values));
    return out;
  }
  return node.value;
}

export interface JSONTextLine { text: string; path?: string; value?: unknown }
export function jsonTextLines(text: string): JSONTextLine[] {
  const lines: JSONTextLine[] = [];
  const values = new WeakMap<JSONTextNode, unknown>();
  const pathFor = (path: string, key: string): string => {
    if (/^[A-Za-z_][\w]*$/.test(key)) return `${path}.${key}`;
    const escaped = JSON.stringify(key).slice(1, -1).replace(/'/g, "\\'");
    return `${path}['${escaped}']`;
  };
  const visit = (node: JSONTextNode, path: string, depth: number, prefix = "", comma = "") => {
    const indent = "  ".repeat(depth);
    if (node.kind === "object") {
      const entries = node.entries ?? [];
      lines.push({ text: indent + prefix + "{" + (entries.length ? "" : "}" + comma), path, value: jsonTextValue(node, values) });
      entries.forEach((entry, index) => visit(entry.value, pathFor(path, entry.key), depth + 1, entry.keyRaw + ": ", index < entries.length - 1 ? "," : ""));
      if (entries.length) lines.push({ text: indent + "}" + comma });
    } else if (node.kind === "array") {
      const elements = node.elements ?? [];
      lines.push({ text: indent + prefix + "[" + (elements.length ? "" : "]" + comma), path, value: jsonTextValue(node, values) });
      elements.forEach((element, index) => visit(element, `${path}[${index}]`, depth + 1, "", index < elements.length - 1 ? "," : ""));
      if (elements.length) lines.push({ text: indent + "]" + comma });
    } else lines.push({ text: indent + prefix + node.raw + comma, path, value: jsonTextValue(node, values) });
  };
  visit(parseJSONText(text), "$", 0);
  return lines;
}
