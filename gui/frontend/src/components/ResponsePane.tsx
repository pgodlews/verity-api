import { useMemo, useState } from "react";
import { Check, Copy, Download, Shield } from "../icons";
import { formatValue } from "../lib/workspace";
import { jsonTextLines } from "../lib/jsonText";
import type { StepResult } from "../types";
import { Tabs } from "./Editors";

function Syntax({ text }: { text: string }) {
  return <>{text.split(/("(?:\\.|[^"\\])*"\s*:|"(?:\\.|[^"\\])*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|\btrue\b|\bfalse\b|\bnull\b|[{}\[\],:])/g).map((token, i) => {
    const kind = /^".*:\s*$/.test(token) ? "key" : token.startsWith('"') ? "str" : /^-?\d/.test(token) ? "num" : /^(true|false|null)$/.test(token) ? "bool" : "punct";
    return <span className={`tok-${kind}`} key={i}>{token}</span>;
  })}</>;
}

async function copyText(text: string) {
  if (navigator.clipboard?.writeText) {
    try { await navigator.clipboard.writeText(text); return; } catch { /* Older desktop webviews need the selection fallback. */ }
  }
  const previous = document.activeElement as HTMLElement | null;
  const input = document.createElement("textarea");
  input.value = text; input.className = "clipboard-fallback";
  document.body.appendChild(input); input.select();
  try { if (!document.execCommand("copy")) throw new Error("Copy unavailable. Select and copy the path shown on the button."); }
  finally { input.remove(); previous?.focus(); }
}

export function ResponsePane({ result, note, environment, busy, onAssert, onCapture }: {
  result?: StepResult; note?: string; environment?: string; busy: boolean;
  onAssert: (path: string, value: unknown) => void; onCapture: (path: string) => void;
}) {
  const [tab, setTab] = useState("body");
  const [feedback, setFeedback] = useState("");
  const body = result?.response?.body;
  const lines = useMemo(() => {
    if (body === undefined || body === "") return null;
    try { return jsonTextLines(body); } catch { return null; }
  }, [body]);
  const checks = result?.assertions ?? [], captures = result?.captures ?? [];
  const passedChecks = checks.filter(c => c.passed).length;
  const statusCode = result?.response?.statusCode;
  const statusClass = statusCode
    ? statusCode >= 200 && statusCode < 300 ? "passed" : statusCode >= 400 && statusCode < 600 ? "failed" : "neutral"
    : result?.status ?? "neutral";
  const headers = Object.entries(result?.response?.headers ?? {});
  const tabs = [
    { id: "body", label: "Body" }, { id: "headers", label: "Headers", count: headers.length || undefined },
    { id: "checks", label: "Checks", count: checks.length ? `${checks.filter(c => c.passed).length}/${checks.length}` : undefined },
    { id: "captured", label: "Captured", count: captures.length || undefined },
  ];
  return <section className="response-pane" aria-label="Response" aria-busy={busy}>
    <div className="response-top">
      {result ? <>
        <span className={`pill status-${statusClass}`}>{statusClass === "passed" && <Check size={13} />}{result.response?.status || result.response?.statusCode || result.status}</span>
        <span>{result.durationMs > 0 ? `${result.durationMs} ms` : "<1 ms"}{result.attempts != null && result.attempts > 1 ? ` · ${result.attempts} attempts` : ""}</span>
        {body !== undefined && <span title="Size of the displayed, redacted response body">{new TextEncoder().encode(body).length} B shown</span>}
        {!!checks.length && <span className={passedChecks < checks.length ? "check-fail" : "check-pass"}>{passedChecks} of {checks.length} checks passed</span>}
        {environment && <span className="muted">{environment}</span>}
      </> : <strong>Response</strong>}
      <span className="redaction-note"><Shield size={13} />secrets redacted</span>
      {note && <p className="run-note" role="status">{note}</p>}
    </div>
    <Tabs label="Response sections" tabs={tabs} selected={tab} onSelect={setTab} />
    <div className="pane-content" role="tabpanel" aria-label={`Response ${tabs.find(t => t.id === tab)?.label}`}>
      {result?.error && <div className="response-error" role="alert"><strong>{result.error.code}</strong><p>{result.error.message}</p>{result.error.path && <code>{result.error.path}</code>}</div>}
      {!result ? <div className="empty response-empty"><Download size={28} /><p>Send a request to see its response.</p><span>Earlier steps in the case run first to supply captured values.</span></div> : <>
        {tab === "body" && <>
          {lines ? <><div className="code response-code">{lines.map((line, i) => <div className="code-line" key={i}>
            <span className="ln">{i + 1}</span><span className="json-text"><Syntax text={line.text} /></span>
            {line.path !== undefined && <span className="line-actions" role="group" aria-label={`Actions for ${line.path}`}>
              <button className="btn btn-sm" disabled={busy} aria-label={`Assert ${line.path}`} onClick={() => { onAssert(line.path!, line.value); setFeedback(`Added assertion for ${line.path}.`); }}><Check size={11} />Assert</button>
              <button className="btn btn-sm" disabled={busy} aria-label={`Capture ${line.path}`} onClick={() => { onCapture(line.path!); setFeedback(`Added capture for ${line.path}.`); }}><Download size={11} />Capture</button>
              <button className="btn btn-sm mono" aria-label={`Copy path ${line.path}`} title={line.path} onClick={() => { void copyText(line.path!).then(() => setFeedback(`Copied ${line.path}`)).catch(e => setFeedback(String(e))); }}><Copy size={11} /><span className="json-path">{line.path}</span></button>
            </span>}
          </div>)}</div><p className="muted response-hint">Hover a value or focus its actions to assert on it or capture it — no scripts.</p></> : body ? <pre className="code raw-body">{body}</pre> : <p className="empty">{result.status === "skipped" ? "This step was skipped." : result.status === "aborted" ? "This step did not run. The run was cancelled." : "No response body."}</p>}
        </>}
        {tab === "headers" && (headers.length ? <table className="kv mono response-table"><thead><tr><th>Header</th><th>Value</th></tr></thead><tbody>{headers.map(([key, value]) => <tr key={key}><th>{key}</th><td>{value}</td></tr>)}</tbody></table> : <p className="empty">No response headers.</p>)}
        {tab === "checks" && (checks.length ? <ul className="check-list">{checks.map((check, i) => <li key={i} className={`check-result ${check.passed ? "check-passed" : "check-failed"}`}>
          <div><span className={`dot status-${check.passed ? "passed" : "failed"}`} /><strong>{check.passed ? "Passed" : "Failed"}</strong><span>{check.description || check.type}</span></div>
          {check.message && <p>{check.message}</p>}
          {!check.passed && <dl><dt>Expected</dt><dd className="mono">{check.expected === undefined ? "Not provided" : formatValue(check.expected)}</dd><dt>Actual</dt><dd className="mono">{check.actual === undefined ? "Not provided" : formatValue(check.actual)}</dd></dl>}
        </li>)}</ul> : <p className="empty">No assertion results.</p>)}
        {tab === "captured" && (captures.length ? <table className="kv mono response-table"><thead><tr><th>Name</th><th>Value</th></tr></thead><tbody>{captures.map((capture, i) => <tr key={i}><th>{capture.name}</th><td>{capture.value}</td></tr>)}</tbody></table> : <p className="empty">No captured values.</p>)}
      </>}
      <p className="action-feedback" role="status">{feedback}</p>
    </div>
  </section>;
}
