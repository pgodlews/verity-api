import { useEffect, useRef, useState } from "react";
import { api } from "../api";
import { Alert, Check, Close, Lock } from "../icons";
import { fileBaseline } from "../lib/git";
import type { Environment, ProjectInfo, SaveEnvironmentRequest } from "../types";
import {
  credentialMessage,
  envRefName,
  isSecretName,
  refusesLiteral,
  suggestEnvVar,
  toEnvRef,
  withVariable,
  withoutVariable,
} from "./parts/env";
import { baseName, cx, errorMessage } from "./parts/format";
import { RichText, SecretChip } from "./parts/RichText";
import "./views.css";

const PRECEDENCE = ["Environment", "Suite vars", "Case vars", "Data row", "--var", "Step vars & captures"];

type Editor = { env: string; key: string; draft: string; warning: boolean };
type Notice = { kind: "saving" | "saved" | "error"; message: string };
type CellState = { id: string; kind: "saving" | "saved" | "error"; message: string };
type DraftEnv = { name: string; copy: boolean };
type DraftVar = { name: string; values: Record<string, string>; blocked: string[]; resume: boolean };

export function EnvironmentsView(props: {
  info: ProjectInfo;
  environment: string;
  onSelectEnvironment: (name: string) => void;
  onChanged: () => Promise<void> | void;
}): JSX.Element {
  const { info, environment, onSelectEnvironment, onChanged } = props;
  const environments = info.project.environments ?? [];
  const keys = unionKeys(environments);
  const active = activeEnvironment(environments, environment);
  const file = baseName(info.path) || "the project file";
  const envRefs = info.envRefs ?? [];
  const secretsSet = envRefs.filter((ref) => ref.set).length;
  const undefinedVars = info.undefinedVars ?? [];
  const captures = info.captures ?? [];

  const [editor, setEditor] = useState<Editor | null>(null);
  const [cell, setCell] = useState<CellState | null>(null);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [draftEnv, setDraftEnv] = useState<DraftEnv | null>(null);
  const [draftVar, setDraftVar] = useState<DraftVar | null>(null);
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  useEffect(() => {
    if (cell?.kind !== "saved" && notice?.kind !== "saved") return;
    const timer = window.setTimeout(() => {
      setCell((current) => (current?.kind === "saved" ? null : current));
      setNotice((current) => (current?.kind === "saved" ? null : current));
    }, 2000);
    return () => window.clearTimeout(timer);
  }, [cell, notice]);

  async function saveAll(reqs: SaveEnvironmentRequest[]) {
    let failed: unknown;
    const saved: string[] = [];
    let baseline = fileBaseline(info);
    for (const req of reqs) {
      try {
        const res = await api.saveEnvironment(baseline ? { ...req, baseline } : req);
        if (res.hashes) baseline = { ...(baseline ?? {}), ...res.hashes };
        saved.push(req.name);
      } catch (err) {
        // Earlier environments are already written: say so instead of
        // implying nothing changed.
        const msg = err instanceof Error ? err.message : String(err);
        failed = new Error(
          saved.length > 0
            ? `${req.name}: ${msg} (already saved: ${saved.join(", ")}; not saved: ${reqs.slice(saved.length).map((r) => r.name).join(", ")})`
            : `${req.name}: ${msg}`
        );
        break;
      }
    }
    try {
      await onChanged();
    } catch (err) {
      if (failed == null) failed = err;
    }
    if (failed != null) throw failed;
  }

  async function runSave(label: { cell?: string; text?: string }, work: () => Promise<void>): Promise<true | false | "busy"> {
    if (lock.current) return "busy";
    lock.current = true;
    setBusy(true);
    if (label.cell) setCell({ id: label.cell, kind: "saving", message: "Saving…" });
    else setNotice({ kind: "saving", message: label.text ?? "Saving…" });
    try {
      await work();
      if (!alive.current) return true;
      if (label.cell) setCell({ id: label.cell, kind: "saved", message: "Saved" });
      else setNotice({ kind: "saved", message: "Saved" });
      return true;
    } catch (err) {
      if (!alive.current) return false;
      const message = errorMessage(err);
      if (label.cell) setCell({ id: label.cell, kind: "error", message });
      else setNotice({ kind: "error", message });
      return false;
    } finally {
      lock.current = false;
      if (alive.current) setBusy(false);
    }
  }

  function openEditor(envName: string, key: string, value: string) {
    setEditor({ env: envName, key, draft: value, warning: false });
    const id = cellId(envName, key);
    setCell((current) => (current?.id === id ? null : current));
  }

  async function commitValue(envName: string, key: string, value: string) {
    if (refusesLiteral(key, value)) {
      if (alive.current) setEditor({ env: envName, key, draft: value, warning: true });
      return;
    }
    const env = environments.find((item) => item.name === envName);
    if (!env) return;
    const previous = env.variables?.[key] ?? "";
    if (value === previous) {
      if (alive.current) setEditor(null);
      return;
    }
    const ok = await runSave({ cell: cellId(envName, key) }, () => saveAll([{
      path: info.path,
      name: envName,
      variables: withVariable(env.variables, key, value),
    }]));
    if (ok === true && alive.current) setEditor(null);
  }

  function storeAsEnv(envName: string, key: string, current: string) {
    if (envRefName(current)) {
      openEditor(envName, key, current.trim());
      return;
    }
    const next = toEnvRef(suggestEnvVar(key));
    setEditor({ env: envName, key, draft: next, warning: false });
    void commitValue(envName, key, next);
  }

  async function removeVariable(key: string) {
    if (!window.confirm(`Remove ${key} from every environment?`)) return;
    const reqs = environments
      .filter((env) => Object.prototype.hasOwnProperty.call(env.variables ?? {}, key))
      .map((env) => ({
        path: info.path,
        name: env.name,
        variables: withoutVariable(env.variables, key),
      }));
    if (reqs.length === 0) return;
    await runSave({ text: `Removing ${key}…` }, () => saveAll(reqs));
  }

  async function submitVariable(convert: boolean) {
    if (!draftVar) return;
    const name = draftVar.name.trim();
    if (!name) {
      setNotice({ kind: "error", message: "variable names must not be empty" });
      return;
    }
    if (keys.includes(name) && !draftVar.resume) {
      setNotice({ kind: "error", message: `variable "${name}" already exists` });
      return;
    }
    const values = { ...draftVar.values };
    const blocked = environments
      .filter((env) => refusesLiteral(name, values[env.name] ?? ""))
      .map((env) => env.name);
    if (blocked.length > 0 && !convert) {
      setDraftVar({ ...draftVar, name, blocked });
      return;
    }
    if (convert) {
      const ref = toEnvRef(suggestEnvVar(name));
      for (const envName of blocked) values[envName] = ref;
    }
    const reqs = environments.map((env) => ({
      path: info.path,
      name: env.name,
      variables: withVariable(env.variables, name, values[env.name] ?? ""),
    }));
    const ok = await runSave({ text: "Saving…" }, () => saveAll(reqs));
    if (ok === "busy" || !alive.current) return;
    if (ok) setDraftVar(null);
    else setDraftVar({ name, values, blocked: [], resume: true });
  }

  async function createEnvironment() {
    if (!draftEnv) return;
    const name = draftEnv.name.trim();
    if (!name) {
      setNotice({ kind: "error", message: "environment name is required" });
      return;
    }
    if (environments.some((env) => env.name === name)) {
      setNotice({ kind: "error", message: `environment "${name}" already exists` });
      return;
    }
    const source = environments.find((env) => env.name === active);
    const variables = draftEnv.copy && source ? { ...(source.variables ?? {}) } : {};
    const ok = await runSave({ text: "Saving…" }, () => saveAll([{
      path: info.path,
      name,
      variables,
      create: true,
    }]));
    if (ok === true && alive.current) {
      setDraftEnv(null);
      onSelectEnvironment(name);
    }
  }

  async function makeDefault(env: Environment) {
    await runSave({ text: "Saving…" }, () => saveAll([{
      path: info.path,
      name: env.name,
      variables: { ...(env.variables ?? {}) },
      default: true,
    }]));
  }

  return (
    <div className="view-env">
      <div className="env-body">
        <div className="env-heading">
          <div>
            <h1 className="view-title">Environments</h1>
            <p className="view-lead">
              Every variable, side by side. Secrets are read from this machine at run time and never saved in{" "}
              <span className="mono">{file}</span>.
            </p>
          </div>
          <div className="env-actions">
            {draftEnv ? (
              <form className="env-form" onSubmit={(event) => { event.preventDefault(); void createEnvironment(); }}>
                <input
                  className="input"
                  aria-label="Environment name"
                  placeholder="Name"
                  value={draftEnv.name}
                  autoFocus
                  disabled={busy}
                  onChange={(event) => setDraftEnv({ ...draftEnv, name: event.target.value })}
                />
                {active && (
                  <label className="env-check">
                    <input
                      type="checkbox"
                      checked={draftEnv.copy}
                      disabled={busy}
                      onChange={(event) => setDraftEnv({ ...draftEnv, copy: event.target.checked })}
                    />
                    Copy values from {active}
                  </label>
                )}
                <button type="submit" className="btn btn-primary" disabled={busy}>Add</button>
                <button type="button" className="btn" disabled={busy} onClick={() => setDraftEnv(null)}>Cancel</button>
              </form>
            ) : (
              <button type="button" className="btn" disabled={busy} onClick={() => setDraftEnv({ name: "", copy: false })}>
                + Environment
              </button>
            )}
          </div>
        </div>

        {(notice || undefinedVars.length > 0 || envRefs.length > 0) && (
          <div className="banner-row">
            {notice && (
              <div className={cx("banner", notice.kind === "error" ? "banner-fail" : "banner-plain")} role={notice.kind === "error" ? "alert" : "status"}>
                {notice.kind === "error" && <Alert size={16} />}
                {notice.kind === "saved" && <Check size={16} />}
                <span>{notice.message}</span>
                {notice.kind === "error" && (
                  <button type="button" className="btn btn-sm" onClick={() => setNotice(null)}>Dismiss</button>
                )}
              </div>
            )}
            {undefinedVars.map((item) => (
              <div className="banner banner-fail" role="status" key={item.name}>
                <Alert size={16} />
                <span>
                  <strong>{item.name}</strong> has no value in {item.missingIn.join(", ") || "no environment"} — used by{" "}
                  {item.usedIn.join(", ") || "the project"}
                </span>
              </div>
            ))}
            {envRefs.length > 0 && (
              <div className="banner banner-plain" role="status">
                <Check size={16} />
                <span>
                  {secretsSet} of {envRefs.length} secrets found on this machine
                </span>
              </div>
            )}
          </div>
        )}

        {environments.length === 0 ? (
          <p className="empty">This project has no environments.</p>
        ) : (
          <div className="card env-card">
            <div className="env-scroll">
              <table className="env-table">
                <caption className="visually-hidden">
                  Variables by environment. Choose a column to make it active. Activate a value to edit it; Enter saves and Escape cancels.
                </caption>
                <thead>
                  <tr>
                    <th scope="col">Variable</th>
                    {environments.map((env) => {
                      const on = env.name === active;
                      return (
                        <th key={env.name} scope="col" className={cx(on && "env-on")}>
                          <div className="env-col">
                            <button
                              type="button"
                              className="env-col-btn"
                              aria-pressed={on}
                              onClick={() => onSelectEnvironment(env.name)}
                            >
                              <span className="env-name">{env.name}</span>
                              {env.default && <span className="env-flag"> · default</span>}
                              {on && <span className="env-flag"> · active</span>}
                            </button>
                            {!env.default && (
                              <button
                                type="button"
                                className="btn btn-sm env-default-btn"
                                disabled={busy}
                                onClick={() => void makeDefault(env)}
                              >
                                Make default
                              </button>
                            )}
                          </div>
                        </th>
                      );
                    })}
                  </tr>
                </thead>
                <tbody>
                  {keys.length === 0 && !draftVar && (
                    <tr>
                      <td className="muted" colSpan={environments.length + 1}>
                        No variables in these environments.
                      </td>
                    </tr>
                  )}
                  {keys.map((key) => (
                    <tr key={key}>
                      <th scope="row">
                        <span className="env-key">
                          {isSecretRow(key, environments) && <Lock size={12} label="Secret" />}
                          <span className="mono">{key}</span>
                          <button
                            type="button"
                            className="icon-btn env-remove"
                            aria-label={`Remove ${key}`}
                            title={`Remove ${key}`}
                            disabled={busy}
                            onClick={() => void removeVariable(key)}
                          >
                            <Close size={12} />
                          </button>
                        </span>
                      </th>
                      {environments.map((env) => {
                        const saved = env.variables?.[key] ?? "";
                        const editing = editor?.env === env.name && editor.key === key;
                        const id = cellId(env.name, key);
                        const note = cell?.id === id ? cell : null;
                        return (
                          <td key={env.name} className={cx(env.name === active && "env-on", (editing || note) && "env-wrap")}>
                            <ValueEditor
                              envName={env.name}
                              varKey={key}
                              saved={saved}
                              draft={editing ? editor.draft : saved}
                              editing={Boolean(editing && !editor.warning)}
                              warning={Boolean(editing && editor.warning)}
                              busy={busy}
                              envRefs={info.envRefs}
                              note={note}
                              onEdit={() => openEditor(env.name, key, saved)}
                              onDraft={(value) => setEditor({ env: env.name, key, draft: value, warning: false })}
                              onCommit={() => void commitValue(env.name, key, editor?.draft ?? saved)}
                              onCancel={() => setEditor(null)}
                              onStore={() => storeAsEnv(env.name, key, editing ? editor.draft : saved)}
                              onConvert={() => void commitValue(env.name, key, toEnvRef(suggestEnvVar(key)))}
                            />
                          </td>
                        );
                      })}
                    </tr>
                  ))}
                  {draftVar && (
                    <tr>
                      <th scope="row">
                        <input
                          className="input mono env-input"
                          aria-label="New variable name"
                          placeholder="Name"
                          value={draftVar.name}
                          autoFocus
                          disabled={busy}
                          spellCheck={false}
                          autoComplete="off"
                          onChange={(event) => setDraftVar({ ...draftVar, name: event.target.value, blocked: [], resume: false })}
                          onKeyDown={(event) => onDraftKey(event, () => void submitVariable(false), () => setDraftVar(null))}
                        />
                      </th>
                      {environments.map((env) => (
                        <td key={env.name} className={cx(env.name === active && "env-on", "env-wrap")}>
                          <input
                            className="input mono env-input"
                            aria-label={draftVar.name.trim()
                              ? `Value of ${draftVar.name.trim()} in ${env.name}`
                              : `Value in ${env.name}`}
                            placeholder="Value"
                            value={draftVar.values[env.name] ?? ""}
                            disabled={busy}
                            spellCheck={false}
                            autoComplete="off"
                            onChange={(event) => setDraftVar({
                              ...draftVar,
                              blocked: [],
                              values: { ...draftVar.values, [env.name]: event.target.value },
                            })}
                            onKeyDown={(event) => onDraftKey(event, () => void submitVariable(false), () => setDraftVar(null))}
                          />
                        </td>
                      ))}
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
            <div className="env-table-actions">
              {draftVar ? (
                <>
                  {draftVar.blocked.length > 0 && (
                    <CredentialWarn
                      varKey={draftVar.name.trim()}
                      environments={draftVar.blocked}
                      busy={busy}
                      dismissLabel="Edit value"
                      onConvert={() => void submitVariable(true)}
                      onDismiss={() => setDraftVar({ ...draftVar, blocked: [] })}
                      onCancel={() => setDraftVar(null)}
                    />
                  )}
                  <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void submitVariable(false)}>
                    Add variable
                  </button>
                  <button type="button" className="btn" disabled={busy} onClick={() => setDraftVar(null)}>Cancel</button>
                </>
              ) : (
                <button
                  type="button"
                  className="btn"
                  disabled={busy}
                  onClick={() => setDraftVar({ name: "", values: {}, blocked: [], resume: false })}
                >
                  + Variable
                </button>
              )}
            </div>
          </div>
        )}

        <div className="env-split">
          <section className="card side-card" aria-label="Captured at run time">
            <h2>Captured at run time</h2>
            <p>Set by a request’s Capture rules; visible to later steps in the same case, or to every later case when marked “whole run”.</p>
            {captures.length === 0 ? (
              <p className="muted">This project doesn’t capture any variables.</p>
            ) : (
              <ul className="capture-list">
                {captures.map((capture) => (
                  <li key={`${capture.step}:${capture.name}:${capture.expr ?? ""}`}>
                    <span className="var-chip">{capture.name}</span>
                    <span>from {capture.step}</span>
                    {capture.expr && <span className="mono muted">{capture.expr}</span>}
                    <span className="muted">{capture.from}</span>
                    {capture.scope === "run" && <span className="pill">whole run</span>}
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="card side-card" aria-label="Which value wins">
            <h2>Which value wins</h2>
            <ol className="prec">
              {PRECEDENCE.map((rank, index) => (
                <li key={rank} className={cx(index === PRECEDENCE.length - 1 && "winner")}>
                  {index > 0 && (
                    <span className="prec-sep" aria-hidden="true">
                      ›
                    </span>
                  )}
                  <span className="prec-rank">{rank}</span>
                </li>
              ))}
            </ol>
            <p>Later wins.</p>
          </section>
        </div>
      </div>
    </div>
  );
}

function ValueEditor({
  envName,
  varKey,
  saved,
  draft,
  editing,
  warning,
  busy,
  envRefs,
  note,
  onEdit,
  onDraft,
  onCommit,
  onCancel,
  onStore,
  onConvert,
}: {
  envName: string;
  varKey: string;
  saved: string;
  draft: string;
  editing: boolean;
  warning: boolean;
  busy: boolean;
  envRefs: ProjectInfo["envRefs"];
  note: CellState | null;
  onEdit: () => void;
  onDraft: (value: string) => void;
  onCommit: () => void;
  onCancel: () => void;
  onStore: () => void;
  onConvert: () => void;
}) {
  const shown = editing || warning ? draft : saved;
  const locked = envRefName(shown) !== undefined;
  return (
    <div className="env-cell">
      {!warning && (
        <div className="env-value">
          {editing ? (
            <input
              className="input mono env-cell-input"
              aria-label={`${varKey} in ${envName}`}
              value={draft}
              autoFocus
              disabled={busy}
              spellCheck={false}
              autoComplete="off"
              onChange={(event) => onDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  onCommit();
                } else if (event.key === "Escape") {
                  event.preventDefault();
                  onCancel();
                }
              }}
            />
          ) : (
            <button type="button" className="env-cell-btn" title={`Edit ${varKey} in ${envName}`} disabled={busy} onClick={onEdit}>
              <ValueCell value={saved} envRefs={envRefs} />
            </button>
          )}
          <button
            type="button"
            className="icon-btn env-lock"
            aria-pressed={locked}
            aria-label="Store as environment variable"
            title={locked ? "Stored as an environment variable" : `Store as ${toEnvRef(suggestEnvVar(varKey))}`}
            disabled={busy}
            onClick={onStore}
          >
            <Lock size={12} />
          </button>
        </div>
      )}
      {warning && (
        <CredentialWarn
          varKey={varKey}
          busy={busy}
          dismissLabel="Edit value"
          onConvert={onConvert}
          onDismiss={() => onDraft(draft)}
          onCancel={onCancel}
        />
      )}
      {note && (
        <span className={`env-cell-note ${note.kind}`} role={note.kind === "error" ? "alert" : "status"}>
          {note.message}
        </span>
      )}
    </div>
  );
}

function CredentialWarn({
  varKey,
  environments,
  busy,
  dismissLabel,
  onConvert,
  onDismiss,
  onCancel,
}: {
  varKey: string;
  environments?: string[];
  busy: boolean;
  dismissLabel: string;
  onConvert: () => void;
  onDismiss: () => void;
  onCancel: () => void;
}) {
  return (
    <div
      className="env-warn"
      role="alert"
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.preventDefault();
          onCancel();
        }
      }}
    >
      <p>{credentialMessage(varKey, environments)}</p>
      <div className="env-warn-actions">
        <button type="button" className="btn btn-sm" autoFocus disabled={busy} onClick={onConvert}>
          Store as {toEnvRef(suggestEnvVar(varKey))}
        </button>
        <button type="button" className="btn btn-sm" disabled={busy} onClick={onDismiss}>
          {dismissLabel}
        </button>
      </div>
    </div>
  );
}

function onDraftKey(event: { key: string; preventDefault: () => void }, submit: () => void, cancel: () => void) {
  if (event.key === "Enter") {
    event.preventDefault();
    submit();
  } else if (event.key === "Escape") {
    event.preventDefault();
    cancel();
  }
}

function cellId(envName: string, key: string): string {
  return JSON.stringify([envName, key]);
}

function unionKeys(environments: Environment[]): string[] {
  const seen = new Set<string>();
  const keys: string[] = [];
  for (const env of environments) {
    for (const key of Object.keys(env.variables ?? {})) {
      if (!seen.has(key)) {
        seen.add(key);
        keys.push(key);
      }
    }
  }
  return keys;
}

function activeEnvironment(environments: Environment[], selected: string): string {
  if (selected && environments.some((env) => env.name === selected)) return selected;
  if (!selected) return environments.find((env) => env.default)?.name ?? "";
  return "";
}

function isSecretRow(key: string, environments: Environment[]): boolean {
  if (isSecretName(key)) return true;
  return environments.some((env) => envRefName(env.variables?.[key] ?? "") !== undefined);
}

function ValueCell({ value, envRefs }: { value: string | undefined; envRefs: ProjectInfo["envRefs"] }) {
  if (value == null || value.trim() === "") return <span className="missing">—</span>;
  const exact = value.trim().match(/^\$\{env:([^}]+)\}$/);
  if (exact) return <SecretChip name={exact[1]} envRefs={envRefs} />;
  if (value.includes("${")) return <RichText text={value} envRefs={envRefs} />;
  return <span className="mono env-literal">{value}</span>;
}
