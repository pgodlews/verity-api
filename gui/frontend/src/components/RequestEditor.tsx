import { useEffect, useState } from "react";
import { GraphQLInsert } from "./GraphQLInsert";
import { OAuthSignIn } from "./OAuthSignIn";
import { applyGraphQLTemplate, effectiveGraphQLSchema } from "../lib/graphqlInsert";
import { Check, Plus, Close, Save } from "../icons";
import { credentialSaveLine, findingCountsByTab, findingsWhere, secretEnvVars, type SecretPlace, type SecretTarget } from "../lib/stepSecrets";
import { authConfigRows } from "../lib/authFields";
import { asMap, effectiveAuth, locate, method, methodClass, resolvePreview } from "../lib/workspace";
import type { BodyMode, Draft, StepLocation } from "../lib/workspace";
import type { Assertion, ProjectInfo, SecretFinding, StepResult, StepRetry, Transfer } from "../types";
import { MapEditor, SecretBanners, Tabs, TextEditor, ValueEditor, VariablePreview } from "./Editors";

function AuthConfig({ type, config }: { type: string; config?: Record<string, unknown> }) {
  const rows = authConfigRows(type, config);
  if (!rows.length) return <p className="empty compact-empty">No configuration.</p>;
  return <dl className="auth-fields">{rows.map((row) => <div key={row.key}>
    <dt className="mono">{row.key}</dt>
    <dd className="mono">{row.masked
      ? <span className="auth-masked" aria-label={`${row.key} is hidden`}>••••••••</span>
      : <VariablePreview text={row.text} />}</dd>
  </div>)}</dl>;
}

export function RequestEditor({ info, environment, at, draft, onChange, tab, onTab, dirty, busy, onSave, onSend, result, findings = [], onStoreSecret, secretStoreVersion = 0 }: {
  result?: StepResult;
  info: ProjectInfo; environment: string; at: StepLocation; draft: Draft;
  onChange: (draft: Draft) => void; tab: string; onTab: (tab: string) => void;
  dirty: boolean; busy: boolean; onSave: () => void; onSend: () => void;
  findings?: SecretFinding[];
  secretStoreVersion?: number;
  onStoreSecret?: (finding: SecretFinding, target?: SecretTarget, name?: string) => void;
}) {
  const { step } = draft, req = step.request ?? {};
  const [undo, setUndo] = useState<Draft | null>(null);
  // Secret replacement must also discard the older plaintext-bearing snapshot.
  useEffect(() => setUndo(null), [secretStoreVersion]);
  useEffect(() => {
    if (!undo) return;
    const id = window.setTimeout(() => setUndo(null), 10_000);
    return () => window.clearTimeout(id);
  }, [undo]);
  const { suite, testCase } = locate(info, at);
  const env = info.project.environments?.find(e => e.name === environment);
  const vars = { ...env?.variables, ...suite?.vars, ...testCase?.vars, ...step.vars };
  const refs = effectiveAuth([env?.auth, suite?.auth, testCase?.auth, step.auth]);
  const assertions = step.assertions ?? [], transfers = step.transfers ?? [];
  const changeRequest = (patch: Record<string, unknown>) => onChange({ ...draft, step: { ...step, request: { ...req, ...patch } } });
  const updateAssertion = (index: number, patch: Partial<Assertion>) => onChange({ ...draft, step: { ...step, assertions: assertions.map((a, i) => i === index ? { ...a, ...patch } : a) } });
  const updateTransfer = (index: number, patch: Partial<Transfer>) => onChange({ ...draft, step: { ...step, transfers: transfers.map((t, i) => i === index ? { ...t, ...patch } : t) } });
  // Empty or zero clears a setting so the step falls back to the run default.
  const whole = (text: string, max: number) => { const n = Math.floor(Number(text)); return Number.isFinite(n) && n > 0 ? Math.min(n, max) : 0; };
  const setTimeoutMs = (text: string) => onChange({ ...draft, step: { ...step, timeoutMs: whole(text, 3_600_000) || undefined } });
  const updateRetry = (patch: Partial<StepRetry>) => {
    const next: StepRetry = { count: 0, until: "passed", ...step.retry, ...patch };
    if (!next.delayMs) delete next.delayMs;
    onChange({ ...draft, step: { ...step, retry: next.count > 0 ? next : undefined } });
  };
  const assertionTypes = ["status", "response_time", "jsonpath", "xpath", "json_schema", "graphql_schema", "xsd", "contains", "regex", "header"];
  const typeLabels: Record<string, string> = { status: "Status", response_time: "Response time", jsonpath: "JSONPath", xpath: "XPath", json_schema: "JSON Schema", graphql_schema: "GraphQL schema", xsd: "XSD", contains: "Body contains", regex: "Regex", header: "Header" };
  const operators = ["eq", "ne", "lt", "lte", "gt", "gte", "contains", "matches", "exists", "not_exists"];
  function addAssertion(type: string) {
    const assertion: Assertion = type === "status" ? { type, op: "eq", value: 200 }
      : type === "response_time" ? { type, op: "lt", value: 1000 }
      : type === "header" ? { type, expr: "Content-Type", op: "exists" }
      : type === "jsonpath" ? { type, expr: "$", op: "exists" }
      : type === "json_schema" ? { type, expr: "" }
      : type === "graphql_schema" ? { type }
      : { type, value: "" };
    onChange({ ...draft, step: { ...step, assertions: [...assertions, assertion] } });
  }
  const preview = resolvePreview(String(req.url ?? ""), vars);
  const warnCounts = findingCountsByTab(findings);
  const storeRow = (place: SecretPlace) => (key: string, target?: SecretTarget, name?: string) => {
    const finding = findings.find((item) => item.path[item.path.length - 1] === key && secretEnvVars([item], place)[key]);
    if (finding) onStoreSecret?.(finding, target, name);
  };
  const tabs = [
    { id: "params", label: "Params", count: step.protocol === "rest" ? Object.keys(asMap(req.query)).length || undefined : undefined, warn: warnCounts.params },
    { id: "headers", label: "Headers", count: Object.keys(asMap(req.headers)).length || undefined, warn: warnCounts.headers },
    { id: "body", label: "Body", count: step.protocol === "graphql" ? "GQL" : step.protocol === "soap" ? "XML" : draft.bodyMode === "json" ? "JSON" : undefined, warn: warnCounts.body },
    { id: "auth", label: "Auth", count: refs.length || "none" },
    { id: "assertions", label: "Assertions", count: assertions.length || undefined },
    { id: "capture", label: "Capture", count: transfers.length || undefined },
    { id: "vars", label: "Vars", count: Object.keys(step.vars ?? {}).length || undefined, warn: warnCounts.vars },
    { id: "settings", label: "Settings", count: step.retry?.count ? `${step.retry.count}×` : step.timeoutMs ? `${step.timeoutMs / 1000}s` : undefined },
  ];
  return <section className="request-pane" aria-label="Request">
    <div className="request-top">
      <div className="request-breadcrumb"><span>{at.suite}</span><span aria-hidden="true">›</span><strong>{step.name}</strong><span className="step-position">step {at.index + 1} of {testCase?.steps.length ?? 0} in case {at.caseName}</span></div>
      <div className="url-row">
        <div className="url-input">
          {step.protocol === "rest" ? <select className={methodClass(step)} aria-label="Method" value={method(step)} disabled={busy} onChange={e => changeRequest({ method: e.target.value })}>
            {Array.from(new Set([method(step), "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE", "CONNECT"])).map(m => <option key={m}>{m}</option>)}
          </select> : <span className={methodClass(step)}>{method(step)}</span>}
          <span className={`url-field${req.url ? " has-value" : ""}`}>
            <input className="mono" aria-label="Request URL" value={String(req.url ?? "")} disabled={busy} spellCheck={false} placeholder="${baseUrl}/resource" onChange={e => changeRequest({ url: e.target.value })} />
            <span className="url-chips mono" aria-hidden="true"><VariablePreview text={String(req.url ?? "")} /></span>
          </span>
        </div>
        <button className="btn btn-primary send-button" disabled={busy} onClick={onSend} title="Send (⌘/Ctrl+Enter)">Send<span className="shortcut">⌘/Ctrl ↵</span></button>
      </div>
      <p className="url-preview mono"><span aria-hidden="true">→ </span><VariablePreview text={preview || "Enter a request URL"} /></p>
      <SecretBanners findings={findingsWhere(findings, "url")} onStore={onStoreSecret} />
    </div>
    <Tabs label="Request sections" tabs={tabs} selected={tab} onSelect={onTab} />
    <div className="pane-content" role="tabpanel" aria-label={tabs.find(t => t.id === tab)?.label}>
      <fieldset className="editor-fields" disabled={busy}>
        {tab === "params" && (step.protocol === "rest" ? <MapEditor label="Query parameters" value={req.query} secrets={secretEnvVars(findings, "query")} onStoreSecret={storeRow("query")} onChange={query => changeRequest({ query })} /> : <p className="empty">{step.protocol === "graphql" ? "Edit the GraphQL query and variables in Body." : "Query parameters can be included in the request URL."}</p>)}
        {tab === "headers" && <MapEditor label="Headers" value={req.headers} secrets={secretEnvVars(findings, "header")} onStoreSecret={storeRow("header")} onChange={headers => changeRequest({ headers })} />}
        {tab === "body" && <>
          {step.protocol === "rest" && <>
            <div className="body-toolbar"><div className="body-modes" role="group" aria-label="Body type">
              {(["none", "json", "text", "form"] as BodyMode[]).map(mode => <button type="button" key={mode} aria-pressed={draft.bodyMode === mode} onClick={() => onChange({ ...draft, bodyMode: mode, bodyText: mode === "json" && !draft.bodyText ? "{}" : draft.bodyText, jsonInBody: mode === "json" && draft.bodyMode !== "json" ? false : draft.jsonInBody })}>{mode === "json" ? "JSON" : mode[0].toUpperCase() + mode.slice(1)}</button>)}
            </div>{draft.bodyMode === "json" && !draft.jsonInBody && <span className="muted">Content-Type set automatically</span>}</div>
            {draft.bodyMode === "none" && <>
              <SecretBanners findings={[...findingsWhere(findings, "json"), ...findingsWhere(findings, "body")]} onStore={onStoreSecret} />
              <p className="empty">This request has no body.</p>
            </>}
            {(draft.bodyMode === "json" || draft.bodyMode === "text") && <>
              <SecretBanners findings={[...findingsWhere(findings, "json"), ...findingsWhere(findings, "body")]} onStore={onStoreSecret} />
              <TextEditor label={draft.bodyMode === "json" ? "JSON body" : "Text body"} value={draft.bodyText} json={draft.bodyMode === "json"} secret={findingsWhere(findings, "json").length + findingsWhere(findings, "body").length > 0} onChange={bodyText => onChange({ ...draft, bodyText })} />
            </>}
            {draft.bodyMode === "form" && <>
              <SecretBanners findings={findingsWhere(findings, "form-nested")} onStore={onStoreSecret} />
              <MapEditor label="Form fields" value={req.form} secrets={secretEnvVars(findings, "form")} onStoreSecret={storeRow("form")} onChange={form => changeRequest({ form })} />
            </>}
          </>}
          {step.protocol === "graphql" && <div className="editor-stack">
            {effectiveGraphQLSchema(info.project, suite, step) && <GraphQLInsert path={info.path} suite={at.suite} caseName={at.caseName} index={at.index} environment={environment} query={String(req.query ?? "")} variablesText={draft.variablesText} undo={undo !== null} onUndo={() => { if (undo) onChange(undo); setUndo(null); }} onInsert={template => { setUndo(draft); onChange(applyGraphQLTemplate(draft, template)); }} />}
            <SecretBanners findings={findingsWhere(findings, "graphql-query")} onStore={onStoreSecret} />
            <TextEditor label="GraphQL query" value={String(req.query ?? "")} secret={findingsWhere(findings, "graphql-query").length > 0} onChange={query => changeRequest({ query })} />
            <SecretBanners findings={findingsWhere(findings, "variables")} onStore={onStoreSecret} />
            <TextEditor label="Variables (JSON object)" value={draft.variablesText} json secret={findingsWhere(findings, "variables").length > 0} onChange={variablesText => onChange({ ...draft, variablesText })} />
            <label className="field">Operation name (optional)<input className="input mono" value={String(req.operationName ?? "")} onChange={e => changeRequest({ operationName: e.target.value })} /></label>
          </div>}
          {step.protocol === "soap" && <div className="editor-stack">
            <div className="soap-options"><label className="field">SOAPAction<input className="input mono" value={String(req.soapAction ?? "")} onChange={e => changeRequest({ soapAction: e.target.value })} /></label>
              <label className="field">Version<select className="input" value={String(req.version ?? "1.1")} onChange={e => changeRequest({ version: e.target.value })}><option>1.1</option><option>1.2</option></select></label></div>
            <SecretBanners findings={findingsWhere(findings, "body")} onStore={onStoreSecret} />
            <TextEditor label="SOAP envelope" value={draft.bodyText} secret={findingsWhere(findings, "body").length > 0} onChange={bodyText => onChange({ ...draft, bodyText })} />
          </div>}
          {!["rest", "graphql", "soap"].includes(step.protocol) && <><p className="muted">The body editor is unavailable for {step.protocol}.</p><pre className="code raw-body">{JSON.stringify(req, null, 2)}</pre></>}
        </>}
        {tab === "auth" && <div className="editor-stack">
          <p className="muted">Authentication is inherited from environment → suite → case → step. Each scope adds its providers; inherit: false replaces them. Definitions are read-only here.</p>
          {[{ name: "Environment", ref: env?.auth }, { name: "Suite", ref: suite?.auth }, { name: "Case", ref: testCase?.auth }, { name: "Step", ref: step.auth }].map(scope => <div className="auth-scope" key={scope.name}><strong>{scope.name}</strong><span>{scope.ref?.inherit === false ? "replaces" : "inherits + adds"}</span><span className="mono">{scope.ref?.use?.join(", ") || "none"}</span></div>)}
          <h3 className="label">Effective authentication</h3>
          {!refs.length && <p className="empty compact-empty">No authentication on this request.</p>}
          {refs.map((ref, i) => {
            const definition = info.project.auth?.find(a => a.name === ref);
            return <div className="card auth-definition" key={`${ref}-${i}`}><strong>{ref}</strong><span className="count">{definition?.type ?? "undefined"}</span>
              {definition?.type === "oauth2" && definition.config?.grant === "authorization_code" && <OAuthSignIn key={`${info.path}-${environment}-${ref}`} path={info.path} environment={environment} authName={ref} />}
              {definition ? <AuthConfig type={definition.type} config={definition.config} /> : <p className="inline-error">This auth reference has no definition.</p>}
            </div>;
          })}
        </div>}
        {tab === "assertions" && <div className="editor-stack compact-editors">
          <p className="muted">Checks run after every send and in CI. No scripting needed.</p>
          {assertions.map((assertion, i) => {
            const check = result?.assertions?.[i];
            const matched = check?.type === assertion.type ? check : undefined;
            return <div className="card compact-row assertion-editor" role="group" aria-label={`Assertion ${i + 1}`} key={`${assertions.length}-${i}`}>
              <span className={`assertion-status ${matched ? matched.passed ? "check-pass" : "check-fail" : "muted"}`} role="img" aria-label={matched ? `Last run: ${matched.passed ? "passed" : "failed"}` : "Not checked"} title={matched ? `Last run: ${matched.passed ? "passed" : "failed"}` : "Not checked"}>
                {matched ? matched.passed ? <Check size={15} /> : <Close size={15} /> : <span className="dot status-none" />}
              </span>
              <select className="input compact-type" aria-label={`Assertion ${i + 1} type`} title={typeLabels[assertion.type] ?? assertion.type} value={assertion.type} onChange={e => updateAssertion(i, { type: e.target.value })}>
                {Array.from(new Set([...assertionTypes, assertion.type])).map(type => <option key={type} value={type}>{typeLabels[type] ?? type}</option>)}
              </select>
              {!["status", "response_time"].includes(assertion.type) && <input className="input mono compact-expr" aria-label={`Assertion ${i + 1} expression`} placeholder={assertion.type === "json_schema" ? "Schema file" : "Expression"} value={assertion.expr ?? ""} onChange={e => updateAssertion(i, { expr: e.target.value })} />}
              <select className="input compact-op" aria-label={`Assertion ${i + 1} operator`} value={assertion.op ?? ""} onChange={e => updateAssertion(i, { op: e.target.value || undefined })}>
                <option value="">Auto</option>{Array.from(new Set([...operators, ...(assertion.op ? [assertion.op] : [])])).map(op => <option key={op} value={op}>{op}</option>)}
              </select>
              <span className="compact-value"><ValueEditor label={`Assertion ${i + 1} value`} value={assertion.value} onChange={value => updateAssertion(i, { value })} /></span>
              <input className="input compact-description" aria-label={`Assertion ${i + 1} description`} placeholder="Description" value={assertion.description ?? ""} onChange={e => updateAssertion(i, { description: e.target.value })} />
              <button type="button" className="icon-btn compact-remove" aria-label={`Remove assertion ${i + 1}`} onClick={() => onChange({ ...draft, step: { ...step, assertions: assertions.filter((_, n) => n !== i) } })}><Close size={13} /></button>
            </div>;
          })}
          <div className="quick-add-row" role="group" aria-label="Add assertion"><span className="muted">+ Add</span>
            {["status", "jsonpath", "header", "contains", "response_time", "json_schema", "graphql_schema"].map(type => <button type="button" className="btn btn-sm" key={type} onClick={() => addAssertion(type)}>{typeLabels[type]}</button>)}
          </div>
        </div>}
        {tab === "capture" && <div className="editor-stack compact-editors"><p className="muted">Values captured here are available to the next steps in this case. Choose “Whole run” to share one with every case that runs after it, such as a login token.</p>
          {transfers.map((transfer, i) => <div className="card compact-row transfer-editor" role="group" aria-label={`Capture ${i + 1}`} key={i}>
            <input className="input mono compact-name" aria-label={`Capture ${i + 1} name`} placeholder="Name" value={transfer.name} onChange={e => updateTransfer(i, { name: e.target.value })} />
            <span className="muted" aria-hidden="true">←</span>
            <select className="input compact-source" aria-label={`Capture ${i + 1} from`} value={transfer.from} onChange={e => updateTransfer(i, { from: e.target.value })}>
              {Array.from(new Set(["jsonpath", "xpath", "header", "status", "body", "regex", transfer.from])).map(from => <option key={from} value={from}>{typeLabels[from] ?? from}</option>)}
            </select>
            <input className="input mono compact-expr" aria-label={`Capture ${i + 1} expression`} placeholder="Expression" value={transfer.expr ?? ""} onChange={e => updateTransfer(i, { expr: e.target.value })} />
            <select className="input compact-scope" aria-label={`Capture ${i + 1} visible to`} title="Who can use the captured value" value={transfer.scope === "run" ? "run" : "case"} onChange={e => updateTransfer(i, { scope: e.target.value === "run" ? "run" : undefined })}>
              <option value="case">This case</option>
              <option value="run">Whole run</option>
            </select>
            <button type="button" className="icon-btn compact-remove" aria-label={`Remove capture ${i + 1}`} onClick={() => onChange({ ...draft, step: { ...step, transfers: transfers.filter((_, n) => n !== i) } })}><Close size={13} /></button>
          </div>)}
          <button type="button" className="btn add-row" onClick={() => onChange({ ...draft, step: { ...step, transfers: [...transfers, { name: "", from: "jsonpath", expr: "$" }] } })}><Plus size={14} />Add capture</button>
        </div>}
        {tab === "settings" && <div className="editor-stack">
          <p className="muted">Leave a field empty to use the run’s defaults.</p>
          <div className="settings-grid">
            <label className="field">Timeout (ms)
              <input className="input mono" type="number" min={0} step={500} inputMode="numeric" placeholder="Run default (30000)" value={step.timeoutMs ?? ""} onChange={e => setTimeoutMs(e.target.value)} />
              <span className="hint">Applies to every attempt.</span>
            </label>
            <label className="field">Retries
              <input className="input mono" type="number" min={0} max={20} inputMode="numeric" placeholder="0" value={step.retry?.count || ""} onChange={e => updateRetry({ count: whole(e.target.value, 20) })} />
              <span className="hint">Up to 20 extra attempts.</span>
            </label>
            <label className="field">Delay between attempts (ms)
              <input className="input mono" type="number" min={0} step={100} inputMode="numeric" placeholder="0" disabled={!step.retry?.count} value={step.retry?.delayMs ?? ""} onChange={e => updateRetry({ delayMs: whole(e.target.value, 600_000) })} />
            </label>
            <label className="field">Retry until
              <select className="input" disabled={!step.retry?.count} value={step.retry?.until ?? "passed"} onChange={e => updateRetry({ until: e.target.value === "status" ? "status" : "passed" })}>
                <option value="passed">All assertions pass</option>
                <option value="status">No 5xx, 429 or network error</option>
              </select>
            </label>
          </div>
          {result?.attempts && result.attempts > 1 && <p className="muted">Last send took {result.attempts} attempts.</p>}
        </div>}
        {tab === "vars" && <div className="editor-stack">
          <MapEditor label="Step variables" value={step.vars} secrets={secretEnvVars(findings, "var")} onStoreSecret={storeRow("var")} onChange={vars => onChange({ ...draft, step: { ...step, vars: Object.fromEntries(Object.entries(vars).map(([key, value]) => [key, String(value)])) } })} />
          <p className="muted">Step variables override case, suite, and environment variables. Secret references stay unresolved.</p>
          <h3 className="label">Available variables</h3>
          <table className="kv"><tbody>{Object.entries(vars).map(([name, value]) => <tr key={name}><th className="mono">{name}</th><td className="mono"><VariablePreview text={value} /></td></tr>)}</tbody></table>
          {!!info.captures.length && <><h3 className="label">Captured at run time</h3>{info.captures.map((capture, i) => <div className="muted" key={i}><span className="var-chip">{capture.name}</span> · {capture.step}{capture.scope === "run" && " · whole run"}</div>)}</>}
        </div>}
      </fieldset>
    </div>
    {findingsWhere(findings, "other").length > 0 && <div className="secret-footer-extra"><SecretBanners findings={findingsWhere(findings, "other")} disabled={busy} onStore={onStoreSecret} /></div>}
    {findings.length > 0 && <p className="secret-footer-note" role="status">{credentialSaveLine(findings.length)}</p>}
    <div className="request-footer"><span className="muted">{dirty ? "Unsaved changes" : "Saved"} · ⌘/Ctrl+S to save</span><button className="btn btn-sm" disabled={busy || !dirty || findings.length > 0} title={findings.length ? credentialSaveLine(findings.length) : undefined} onClick={onSave}><Save size={13} />Save</button></div>
  </section>;
}
