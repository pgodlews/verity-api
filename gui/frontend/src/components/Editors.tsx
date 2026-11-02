import { Fragment, useEffect, useId, useRef, useState } from "react";
import { Close, Plus } from "../icons";
import { asMap, formatValue } from "../lib/workspace";
import {
  credentialBadgeLabel,
  findingBannerLabel,
  SECRET_FIELD_COPY,
  secretHint,
  storeAsLabel,
  inTextHint,
  findingPathString,
} from "../lib/stepSecrets";
import type { SecretTarget } from "../lib/stepSecrets";
import type { SecretFinding } from "../types";

export interface TabItem { id: string; label: string; count?: string | number; warn?: number }
export function Tabs({ label, tabs, selected, onSelect }: { label: string; tabs: TabItem[]; selected: string; onSelect: (tab: string) => void }) {
  const ref = useRef<HTMLDivElement>(null);
  // Tabs scroll sideways in a narrow pane; data-more-left/right fade the edge
  // that has hidden tabs, so it is clear there are more.
  const [more, setMore] = useState({ left: false, right: false });
  const measure = () => {
    const el = ref.current;
    if (!el) return;
    const next = { left: el.scrollLeft > 1, right: el.scrollLeft + el.clientWidth < el.scrollWidth - 1 };
    setMore(prev => prev.left === next.left && prev.right === next.right ? prev : next);
  };
  // Keep the selected tab in view, clear of the 40px edge fade. Scrolls only
  // the tab row (scrollIntoView could scroll the page too).
  const reveal = () => {
    const el = ref.current, tab = el?.querySelector<HTMLElement>('[aria-selected="true"]');
    if (!el || !tab) return;
    const t = tab.getBoundingClientRect(), c = el.getBoundingClientRect(), pad = 40;
    if (t.left < c.left + pad) el.scrollLeft -= c.left + pad - t.left;
    else if (t.right > c.right - pad) el.scrollLeft += t.right - (c.right - pad);
  };
  useEffect(() => {
    const el = ref.current;
    reveal();
    measure();
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => { reveal(); measure(); });
    observer.observe(el);
    return () => observer.disconnect();
  }, [selected, tabs.length]);
  return <div className="tabs" role="tablist" aria-label={label} ref={ref} onScroll={measure} data-more-left={more.left || undefined} data-more-right={more.right || undefined}>
    {tabs.map((tab, index) => <button key={tab.id} className="tab" role="tab" aria-selected={selected === tab.id} tabIndex={selected === tab.id ? 0 : -1}
      onClick={() => onSelect(tab.id)} onKeyDown={e => {
        let next = index;
        if (e.key === "ArrowRight") next = (index + 1) % tabs.length;
        else if (e.key === "ArrowLeft") next = (index + tabs.length - 1) % tabs.length;
        else if (e.key === "Home") next = 0;
        else if (e.key === "End") next = tabs.length - 1;
        else return;
        e.preventDefault(); onSelect(tabs[next].id);
        (e.currentTarget.parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next])?.focus();
      }}>{tab.label}{tab.count !== undefined && <span className="count">{tab.count}</span>}{tab.warn ? <span className="count count-warn" title={credentialBadgeLabel(tab.warn)}>{tab.warn}<span className="sr-only"> {tab.warn === 1 ? "value looks like a credential" : "values look like credentials"}</span></span> : null}</button>)}
  </div>;
}

export function MapEditor({ label, value, onChange, secrets, onStoreSecret }: {
  label: string; value: unknown; onChange: (value: Record<string, unknown>) => void;
  /** Suggested env var by row key. Values are not included. */
  secrets?: Record<string, string>;
  onStoreSecret?: (key: string, target?: SecretTarget, name?: string) => void;
}) {
  const map = asMap(value), entries = Object.entries(map);
  const baseId = useId();
  const [error, setError] = useState("");
  function change(index: number, key: string, val: unknown) {
    if (entries.some(([name], i) => name === key && i !== index)) { setError(`A key named “${key}” already exists.`); return; }
    setError("");
    onChange(Object.fromEntries(entries.map(([k, v], i) => i === index ? [key, val] : [k, v])));
  }
  return <div className="map-editor">
    {entries.length ? <table className="kv"><thead><tr><th>Key</th><th>Value</th><th><span className="sr-only">Actions</span></th></tr></thead><tbody>
      {entries.map(([key, val], i) => {
        const envVar = secrets?.[key];
        const describedBy = envVar ? `${baseId}-${i}` : undefined;
        return <Fragment key={i}>
          <tr>
            <td><input className="input mono" aria-label={`${label} key ${i + 1}`} value={key} onChange={e => change(i, e.target.value, val)} /></td>
            <td><input className={`input mono${envVar ? " secret-input" : ""}`} aria-label={`${label} value ${i + 1}`} aria-invalid={envVar ? true : undefined} aria-describedby={describedBy} value={formatValue(val)} onChange={e => change(i, key, e.target.value)} /></td>
            <td><button type="button" className="icon-btn" aria-label={`Remove ${label} ${key || i + 1}`} onClick={() => { setError(""); onChange(Object.fromEntries(entries.filter((_, n) => n !== i))); }}><Close size={13} /></button></td>
          </tr>
          {envVar && <tr className="secret-note"><td colSpan={3}><SecretWarn id={describedBy} envVar={envVar} onStore={(target, name) => onStoreSecret?.(key, target, name)} /></td></tr>}
        </Fragment>;
      })}
    </tbody></table> : <p className="empty compact-empty">No {label.toLowerCase()} on this request yet.</p>}
    {error && <p className="inline-error" role="alert">{error}</p>}
    <button type="button" className="btn btn-sm add-row" onClick={() => {
      let key = "key", i = 2;
      while (Object.prototype.hasOwnProperty.call(map, key)) key = `key${i++}`;
      onChange({ ...map, [key]: "" });
    }}><Plus size={13} />Add {label.toLowerCase()}</button>
  </div>;
}

export function TextEditor({ label, value, onChange, json = false, secret = false }: { label: string; value: string; onChange: (text: string) => void; json?: boolean; secret?: boolean }) {
  const id = useId();
  let error = "";
  if (json) { try { JSON.parse(value); } catch { error = "Invalid JSON. Fix it before saving or sending."; } }
  return <label className="field code-field">{label}
    <textarea className={`input mono code-editor${secret ? " has-secret" : ""}`} aria-invalid={!!error || secret} aria-describedby={error ? id : undefined} value={value} spellCheck={false} onChange={e => onChange(e.target.value)} />
    {error && <span id={id} className="inline-error">{error}</span>}
  </label>;
}

export function SecretWarn({ id, envVar, pathLabel, disabled, onStore, inTextKey }: {
  id?: string; envVar: string; pathLabel?: string; disabled?: boolean; onStore: (target?: SecretTarget, name?: string) => void;
  /** Set for a literal inside a URL or XML/form body: no one-click fix. */
  inTextKey?: string;
}) {
  const [keychainName, setKeychainName] = useState(envVar);
  return <div id={id} className="secret-warn" role="alert">
    <p>{pathLabel ? <><span className="mono">{pathLabel}</span>{" — "}</> : null}{SECRET_FIELD_COPY}</p>
    {inTextKey === undefined ? <>
      <div className="secret-warn-actions">
        <button type="button" className="btn btn-sm btn-primary" disabled={disabled} onClick={() => onStore("env")}>{storeAsLabel(envVar)}</button>
        <label className="field">Keychain name
          <input className="input mono" value={keychainName} disabled={disabled} onChange={event => setKeychainName(event.target.value)} placeholder="project/NAME" />
        </label>
        <button type="button" className="btn btn-sm" disabled={disabled || !keychainName} onClick={() => onStore("keychain", keychainName)}>Store in OS keychain</button>
      </div>
      <p className="hint">{secretHint(envVar)}. Keychain names may include a project/ prefix; replacing an existing item requires confirmation.</p>
    </> : <p className="hint">{inTextHint(inTextKey, envVar)}</p>}
  </div>;
}

export function SecretBanners({ findings, disabled, onStore }: {
  findings: SecretFinding[]; disabled?: boolean; onStore?: (finding: SecretFinding, target?: SecretTarget, name?: string) => void;
}) {
  if (!findings.length) return null;
  return <div className="secret-banners">{findings.map((finding) => <SecretWarn
    key={finding.path.join("\0") + "\0" + finding.key}
    envVar={finding.envVar}
    pathLabel={finding.inText ? `${findingPathString(finding.path)} · ${finding.key}` : findingBannerLabel(finding.path)}
    inTextKey={finding.inText ? finding.key : undefined}
    disabled={disabled}
    onStore={(target, name) => onStore?.(finding, target, name)}
  />)}</div>;
}

// Preserve in-progress input (e.g. a decimal or a partially typed JSON array).
// Strings can be entered directly; valid JSON retains its type when saved.
export function ValueEditor({ label, value, onChange }: { label: string; value: unknown; onChange: (value: unknown) => void }) {
  const [text, setText] = useState(() => JSON.stringify(value) ?? "");
  const lastEmitted = useRef(JSON.stringify(value));
  useEffect(() => {
    const serialized = JSON.stringify(value);
    if (serialized !== lastEmitted.current) setText(serialized ?? "");
    lastEmitted.current = serialized;
  }, [value]);
  return <input className="input mono" aria-label={label} value={text} placeholder="JSON value or text" onChange={e => {
    const next = e.target.value; setText(next);
    let parsed: unknown = next;
    try { parsed = JSON.parse(next); } catch { /* A plain string is also a valid assertion value. */ }
    lastEmitted.current = JSON.stringify(parsed);
    onChange(parsed);
  }} />;
}

export function VariablePreview({ text }: { text: string }) {
  return <>{text.split(/(\$\{[^}]+\})/g).map((part, i) => part.startsWith("${")
    ? <span className={`var-chip${/^\$\{(?:env|secret):/.test(part) ? " secret" : ""}`} key={i}>{part}</span>
    : <span key={i}>{part}</span>)}</>;
}
