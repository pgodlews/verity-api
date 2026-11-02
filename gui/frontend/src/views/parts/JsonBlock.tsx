import type { ReactNode } from "react";
import { prettyBody } from "./format";

const JSON_TOKEN =
  /("(?:\\.|[^"\\])*")(\s*:)?|\b(?:true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|[{}\[\],]/g;

function highlightJsonLine(line: string): ReactNode[] {
  const nodes: ReactNode[] = [];
  let last = 0;
  let n = 0;
  for (const match of line.matchAll(JSON_TOKEN)) {
    const start = match.index ?? 0;
    if (start > last) nodes.push(line.slice(last, start));
    const raw = match[0];
    if (match[1] && match[2] !== undefined) {
      nodes.push(
        <span className="tok-key" key={n++}>
          {match[1]}
        </span>,
      );
      nodes.push(
        <span className="tok-punct" key={n++}>
          {match[2]}
        </span>,
      );
    } else if (match[1]) {
      nodes.push(
        <span className="tok-str" key={n++}>
          {match[1]}
        </span>,
      );
    } else if (raw === "true" || raw === "false" || raw === "null") {
      nodes.push(
        <span className="tok-bool" key={n++}>
          {raw}
        </span>,
      );
    } else if (raw === "{" || raw === "}" || raw === "[" || raw === "]" || raw === ",") {
      nodes.push(
        <span className="tok-punct" key={n++}>
          {raw}
        </span>,
      );
    } else {
      nodes.push(
        <span className="tok-num" key={n++}>
          {raw}
        </span>,
      );
    }
    last = start + raw.length;
  }
  if (last < line.length) nodes.push(line.slice(last));
  if (nodes.length === 0) nodes.push(line || " ");
  return nodes;
}

export function JsonBlock({ text, label }: { text: string; label: string }) {
  const pretty = prettyBody(text);
  if (!pretty.text) return null;
  const lines = pretty.text.split("\n");
  return (
    <div className="code code-scroll" tabIndex={0} aria-label={label}>
      {lines.map((line, index) => (
        <div className="code-line" key={index}>
          <span className="ln">{index + 1}</span>
          <span>{pretty.json ? highlightJsonLine(line) : line || " "}</span>
        </div>
      ))}
    </div>
  );
}
