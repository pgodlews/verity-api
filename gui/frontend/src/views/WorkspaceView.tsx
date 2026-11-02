import { useEffect, useRef, useState } from "react";
import { api } from "../api";
import { Close } from "../icons";
import { CollectionTree } from "../components/CollectionTree";
import { RequestEditor } from "../components/RequestEditor";
import { ResponsePane } from "../components/ResponsePane";
import { applySecretReplacement, credentialSaveLine, SECRET_CHECK_DELAY_MS, storeDraftInKeychain, KeychainReplacementRequired, type SecretTarget } from "../lib/stepSecrets";
import { fileHashOf, isConflictError, keepEdits, planDiskReload, resolveTab, stepFileOf, stepIdentity } from "../lib/git";
import { findResult, locate, locationKey, makeDraft, materialize, method, methodClass, sentResponse } from "../lib/workspace";
import type { Draft, StepLocation } from "../lib/workspace";
import type { GitStepRef, ProjectInfo, RunResult, SecretFinding, Step, StepResult } from "../types";
import "../workspace.css";

interface RequestTab {
  /** Stable identity. Location is not the React key: two drafts must not merge. */
  id: string;
  at: StepLocation;
  draft: Draft;
  baseline: string;
  section: string;
  /** CheckStep results for this draft. Kept off the draft so they do not count as edits. */
  findings: SecretFinding[];
  /** File that defined the step when the tab was opened or last reloaded. */
  file: string;
  occurrence: number;
  siblings: number;
  stepHash: string;
  /** Whole-file hash sent when the step has no HashStep. */
  fileBaseline: string;
  /** This tab's file changed on disk, or a save was refused as a conflict. */
  conflict: boolean;
  secretStoreVersion?: number;
  /** False when the step is gone or would share a save target with another tab. */
  savable: boolean;
  sent?: { result?: StepResult; note: string; run: RunResult };
}
const fingerprint = (draft: Draft) => JSON.stringify(draft);
const isDirty = (tab: RequestTab) => fingerprint(tab.draft) !== tab.baseline;
let tabSeq = 0;
function createTab(info: ProjectInfo, at: StepLocation, step: Step): RequestTab {
  const draft = makeDraft(step);
  const ident = stepIdentity(info, at);
  tabSeq += 1;
  return {
    id: `tab-${tabSeq}`, at, draft, baseline: fingerprint(draft), section: "body", findings: [],
    file: ident.file, occurrence: ident.occurrence, siblings: ident.siblings, stepHash: ident.hash,
    fileBaseline: ident.hash ? "" : fileHashOf(info, ident.file),
    conflict: false, savable: true,
  };
}
function snap(tab: RequestTab) {
  return {
    id: tab.id, suite: tab.at.suite, caseName: tab.at.caseName, index: tab.at.index,
    name: tab.draft.step.name, occurrence: tab.occurrence, siblings: tab.siblings, stepHash: tab.stepHash,
    dirty: isDirty(tab), file: tab.file,
  };
}
function initialTabs(info: ProjectInfo): RequestTab[] {
  for (const suite of info.project.suites ?? []) for (const testCase of suite.cases) {
    if (testCase.steps[0]) return [createTab(info, { suite: suite.name, caseName: testCase.name, index: 0 }, testCase.steps[0])];
  }
  return [];
}

export function WorkspaceView({ info, environment, lastRun, running, active, openStep, filter, onFilter, changedSteps, disk, onSaveStep, onSend, onImport, onDirtyChange }: {
  info: ProjectInfo; environment: string; lastRun: RunResult | null; running: boolean; active: boolean;
  openStep: StepLocation | null;
  filter: string; onFilter: (filter: string) => void;
  changedSteps?: GitStepRef[];
  /** Set when files changed outside Verity. Null on an ordinary save refresh. */
  disk?: { id: number; files: string[] } | null;
  onSaveStep: (suite: string, caseName: string, index: number, step: Step, baseline?: string, fileBaseline?: string) => Promise<string>;
  onSend: (at: StepLocation) => Promise<RunResult>;
  onImport: () => void; onDirtyChange: (dirty: boolean) => void;
}) {
  const bootRef = useRef<RequestTab[] | null>(null);
  if (bootRef.current === null) bootRef.current = initialTabs(info);
  const [tabs, setTabs] = useState(bootRef.current);
  const [selected, setSelected] = useState<string | null>(bootRef.current[0]?.id ?? null);
  const [working, setWorking] = useState(false);
  const [replaceKeychain, setReplaceKeychain] = useState<{ id: string; draft: Draft; finding: SecretFinding; name: string } | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const lock = useRef(false);
  const tabsRef = useRef(tabs);
  tabsRef.current = tabs;
  const seenDisk = useRef(0);
  const handledOpen = useRef<StepLocation | null>(null);
  const tab = tabs.find(t => t.id === selected);
  const busy = running || working;
  useEffect(() => {
    if (replaceKeychain && (replaceKeychain.id !== selected || replaceKeychain.draft !== tab?.draft)) setReplaceKeychain(null);
  }, [replaceKeychain, selected, tab?.draft]);
  const dirty = tabs.some(isDirty);
  const selectedFingerprint = tab ? fingerprint(tab.draft) : "";
  const selectedDirty = !!tab && isDirty(tab);
  useEffect(() => { onDirtyChange(dirty); }, [dirty, onDirtyChange]);
  // Check a dirty draft after a short pause. A newer edit or a closed tab
  // drops the response so a slow CheckStep cannot paint stale findings.
  useEffect(() => {
    if (!selected || !tab) return;
    const key = selected;
    if (!selectedDirty) {
      if (tab.findings.length) updateTab(key, (current) => current.findings.length ? { ...current, findings: [] } : current);
      return;
    }
    let step: Step;
    try { step = materialize(tab.draft); }
    catch { return; }
    const draftFp = selectedFingerprint;
    let cancelled = false;
    const timer = window.setTimeout(() => {
      void api.checkStep(step).then((found) => {
        if (cancelled) return;
        const next = found ?? [];
        updateTab(key, (current) => {
          // A save can land while this response is in flight. Only the draft
          // that was checked, and only while it is still dirty, may take it.
          if (fingerprint(current.draft) !== draftFp || !isDirty(current)) return current;
          if (JSON.stringify(current.findings) === JSON.stringify(next)) return current;
          return { ...current, findings: next };
        });
      }).catch(() => { /* SaveStep still reports a refusal. */ });
    }, SECRET_CHECK_DELAY_MS);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [selected, selectedFingerprint, selectedDirty]);
  useEffect(() => {
    // A save refreshes clean tabs and leaves drafts alone. An external edit
    // (project:changed) reloads a clean request, closes it when the step is
    // gone, and banners a dirty draft whose file changed instead of
    // overwriting it.
    const external = !!disk && disk.id !== seenDisk.current;
    if (disk && external) seenDisk.current = disk.id;
    const current = tabsRef.current;
    const plans = planDiskReload(current.map(snap), info.project, external && disk ? disk.files : null, info.stepFiles);
    const closed = plans.filter((plan) => plan.kind === "close");
    if (closed.length === 1 && closed[0].kind === "close") setNotice(`${closed[0].name} is no longer in the project.`);
    else if (closed.length > 1) setNotice(`${closed.length} requests are no longer in the project.`);
    const next = current.flatMap((tab, index) => {
      const plan = plans[index];
      if (!plan || plan.kind === "close") return [];
      if (plan.kind === "detach") return [{ ...tab, conflict: true, savable: false }];
      if (plan.kind === "keep") {
        const stepHash = plan.stepHash || tab.stepHash;
        return [{
          ...tab, at: plan.at, savable: true,
          conflict: external ? tab.conflict || plan.banner : tab.conflict,
          occurrence: plan.occurrence, siblings: plan.siblings, stepHash,
          fileBaseline: plan.banner ? tab.fileBaseline : (stepHash ? "" : fileHashOf(info, stepFileOf(info, plan.at))),
        }];
      }
      const step = locate(info, plan.at).step;
      if (!step) return [];
      const draft = makeDraft(step);
      const ident = stepIdentity(info, plan.at);
      const stepHash = ident.hash || plan.stepHash;
      return [{
        ...tab, at: plan.at, draft, baseline: fingerprint(draft), findings: [],
        file: ident.file, occurrence: plan.occurrence, siblings: plan.siblings,
        stepHash, fileBaseline: stepHash ? "" : fileHashOf(info, ident.file), conflict: false, savable: true,
      }];
    });
    tabsRef.current = next;
    setTabs(next);
    setSelected((selectedNow) => selectedNow && next.some((item) => item.id === selectedNow) ? selectedNow : (next[0]?.id ?? null));
  }, [info, disk]);
  useEffect(() => {
    if (!openStep || handledOpen.current === openStep) return;
    handledOpen.current = openStep;
    const step = locate(info, openStep).step;
    if (!step) { setError("That step is no longer in the project."); return; }
    const key = locationKey(openStep);
    const existing = tabsRef.current.find((item) => item.savable && locationKey(item.at) === key);
    if (existing) { setSelected(existing.id); return; }
    const created = createTab(info, openStep, step);
    setTabs((current) => current.some((item) => item.id === created.id) ? current : [...current, created]);
    setSelected(created.id);
  }, [openStep, info]);
  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  function updateTab(id: string, change: (tab: RequestTab) => RequestTab) {
    setTabs(current => current.map(t => t.id === id ? change(t) : t));
  }
  function open(at: StepLocation) {
    const step = locate(info, at).step;
    if (!step) return;
    const key = locationKey(at);
    const existing = tabs.find((item) => item.savable && locationKey(item.at) === key);
    if (existing) { setSelected(existing.id); setError(""); setNotice(""); return; }
    const created = createTab(info, at, step);
    setTabs((current) => [...current, created]);
    setSelected(created.id); setError(""); setNotice("");
  }
  function close(id: string) {
    const closing = tabs.find(t => t.id === id);
    if (!closing || busy) return;
    if (isDirty(closing) && !window.confirm(`Discard unsaved changes to “${closing.draft.step.name}”?`)) return;
    const index = tabs.findIndex(t => t.id === id), remaining = tabs.filter(t => t.id !== id);
    setTabs(remaining);
    if (selected === id) setSelected(remaining.length ? remaining[Math.min(index, remaining.length - 1)].id : null);
  }
  async function save(snapshot: RequestTab) {
    if (!snapshot.savable) throw new Error("This request no longer has its own place in the project. Reload it before saving.");
    const step = materialize(snapshot.draft);
    const hash = await onSaveStep(
      snapshot.at.suite, snapshot.at.caseName, snapshot.at.index, step,
      snapshot.stepHash, snapshot.stepHash ? undefined : snapshot.fileBaseline,
    );
    const draft = makeDraft(step);
    const ident = stepIdentity(info, snapshot.at);
    const stepHash = hash || snapshot.stepHash;
    updateTab(snapshot.id, current => ({
      ...current, draft, baseline: fingerprint(draft), findings: [], conflict: false,
      file: ident.file, occurrence: ident.occurrence, siblings: ident.siblings,
      stepHash, fileBaseline: stepHash ? "" : current.fileBaseline,
    }));
  }
  async function perform(send: boolean) {
    if (!tab || busy || lock.current) return;
    if (!send && tab.findings.length) return;
    lock.current = true; setWorking(true); setError(""); setNotice("");
    const snapshot = tab;
    try {
      if (isDirty(snapshot) && snapshot.findings.length) {
        setError(credentialSaveLine(snapshot.findings.length));
        return;
      }
      if (isDirty(snapshot)) await save(snapshot);
      if (send) {
        const run = await onSend(snapshot.at);
        updateTab(snapshot.id, current => ({ ...current, sent: sentResponse(run, snapshot.at, snapshot.draft.step.name) }));
      } else setNotice(`Saved ${snapshot.draft.step.name}.`);
    } catch (e) {
      if (isConflictError(e)) updateTab(snapshot.id, (current) => ({ ...current, conflict: true }));
      else setError(String(e));
    }
    finally { lock.current = false; setWorking(false); }
  }
  useEffect(() => {
    if (!active) return;
    function shortcut(event: KeyboardEvent) {
      if (!(event.ctrlKey || event.metaKey) || event.altKey) return;
      if (event.key.toLowerCase() === "s") { event.preventDefault(); if (!event.repeat && !tab?.findings.length) void perform(false); }
      else if (event.key === "Enter") { event.preventDefault(); if (!event.repeat) void perform(true); }
    }
    window.addEventListener("keydown", shortcut);
    return () => window.removeEventListener("keydown", shortcut);
  });
  function edit(draft: Draft) {
    if (!selected || busy) return;
    updateTab(selected, current => ({ ...current, draft }));
  }
  async function storeSecret(finding: SecretFinding, target: SecretTarget = "env", name = finding.envVar, confirmed = false) {
    if (!selected || busy || lock.current) return;
    setReplaceKeychain(null);
    if (target === "keychain") {
      const snapshot = tabsRef.current.find(item => item.id === selected);
      if (!snapshot) return;
      lock.current = true;
      setWorking(true);
      setError("");
      try {
        const draft = await storeDraftInKeychain(snapshot.draft, finding, name, api.storeKeychainSecret, { exists: api.keychainSecretExists, confirmed });
        // An outside reload may have replaced the draft while the OS dialog was open.
        updateTab(snapshot.id, current => current.draft === snapshot.draft
          ? { ...current, draft, secretStoreVersion: (current.secretStoreVersion ?? 0) + 1, findings: current.findings.filter(item => item.path.join("\0") !== finding.path.join("\0")) }
          : { ...current, secretStoreVersion: (current.secretStoreVersion ?? 0) + 1 });
        setNotice("Stored in OS keychain. Save the request to keep its reference.");
      } catch (err) {
        if (err instanceof KeychainReplacementRequired) setReplaceKeychain({ id: snapshot.id, draft: snapshot.draft, finding, name });
        else setError(String(err));
      } finally {
        lock.current = false;
        setWorking(false);
      }
      return;
    }
    const pathKey = finding.path.join("\0");
    updateTab(selected, current => {
      const draft = applySecretReplacement(current.draft, finding);
      if (draft === current.draft) return current;
      return { ...current, draft, secretStoreVersion: (current.secretStoreVersion ?? 0) + 1, findings: current.findings.filter((item) => item.path.join("\0") !== pathKey) };
    });
  }
  function addAssertion(path: string, value: unknown) {
    if (!tab || !selected || busy) return;
    updateTab(selected, current => ({ ...current, section: "assertions", draft: { ...current.draft, step: { ...current.draft.step, assertions: [...(current.draft.step.assertions ?? []), { type: "jsonpath", expr: path, op: "eq", value, description: `${path} equals response value` }] } } }));
  }
  function addCapture(path: string) {
    if (!tab || !selected || busy) return;
    updateTab(selected, current => {
      const transfers = current.draft.step.transfers ?? [];
      let name = "capturedValue", i = 2;
      while (transfers.some(t => t.name === name)) name = `capturedValue${i++}`;
      return { ...current, section: "capture", draft: { ...current.draft, step: { ...current.draft.step, transfers: [...transfers, { name, from: "jsonpath", expr: path }] } } };
    });
  }
  const latest = tab ? findResult(lastRun, tab.at, tab.draft.step.name) : undefined;
  const ownSend = tab?.sent && (!latest || tab.sent.run === lastRun) ? tab.sent : undefined;
  return <div className="workspace">
    <CollectionTree info={info} lastRun={lastRun} selected={tab?.savable ? locationKey(tab.at) : null} filter={filter} onFilter={onFilter} changedSteps={changedSteps} onOpen={open} onImport={onImport} />
    <main className="workspace-main">
      <div className="open-tabs" role="tablist" aria-label="Open requests">
        {tabs.map((t, index) => {
          return <div className={`open-tab${t.id === selected ? " selected" : ""}`} key={t.id}>
            <button className="request-tab" role="tab" aria-selected={t.id === selected} tabIndex={t.id === selected ? 0 : -1} onClick={() => setSelected(t.id)} onKeyDown={event => {
              let next = index;
              if (event.key === "ArrowRight") next = (index + 1) % tabs.length;
              else if (event.key === "ArrowLeft") next = (index + tabs.length - 1) % tabs.length;
              else if (event.key === "Home") next = 0;
              else if (event.key === "End") next = tabs.length - 1;
              else if (event.key === "Delete") { event.preventDefault(); close(t.id); return; }
              else return;
              event.preventDefault(); setSelected(tabs[next].id);
              event.currentTarget.closest('[role="tablist"]')?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus();
            }}><span className={methodClass(t.draft.step)}>{method(t.draft.step)}</span>{t.draft.step.name}{isDirty(t) && <span className="dirty-dot" role="img" aria-label="Unsaved changes" />}</button>
            <button className="icon-btn close-tab" disabled={busy} aria-label={`Close ${t.draft.step.name}`} onClick={() => close(t.id)}><Close size={12} /></button>
          </div>;
        })}
      </div>
      {error && <div className="error-banner" role="alert">{error}<button className="btn btn-sm" onClick={() => setError("")}>Dismiss</button></div>}
      {notice && <div className="workspace-notice" role="status">{notice}</div>}
      {tab?.conflict && isDirty(tab) && <div className="disk-banner" role="status">
        <span>{tab.savable ? "This request changed on disk (branch switch or pull?)" : "This request no longer has its own place in the project."}</span>
        <button className="btn btn-sm" onClick={() => {
          const resolved = resolveTab(info.project, snap(tab), info.stepFiles);
          if (!resolved) {
            const name = tab.draft.step.name;
            const index = tabs.findIndex((item) => item.id === tab.id);
            const remaining = tabs.filter((item) => item.id !== tab.id);
            setTabs(remaining);
            if (selected === tab.id) setSelected(remaining[Math.min(Math.max(index, 0), Math.max(remaining.length - 1, 0))]?.id ?? null);
            setNotice(`${name} is no longer in the project.`);
            return;
          }
          const step = locate(info, resolved.at).step;
          if (!step) return;
          const draft = makeDraft(step);
          const decision = keepEdits(info, resolved.at);
          const stepHash = decision.stepHash || resolved.stepHash;
          updateTab(tab.id, (current) => ({
            ...current, at: resolved.at, draft, baseline: fingerprint(draft), findings: [],
            file: decision.file, occurrence: decision.occurrence, siblings: decision.siblings,
            stepHash, fileBaseline: stepHash ? "" : decision.fileBaseline, conflict: false, savable: true,
          }));
        }}>Reload from disk</button>
        <button className="btn btn-sm" onClick={() => {
          void (async () => {
            try {
              const fresh = await api.inspect(info.path);
              const decision = keepEdits(fresh, tab.at);
              setError("");
              updateTab(tab.id, (current) => ({
                ...current, conflict: false, stepHash: decision.stepHash, fileBaseline: decision.fileBaseline,
                file: decision.file, occurrence: decision.occurrence, siblings: decision.siblings,
              }));
            } catch (err) {
              setError(String(err));
            }
          })();
        }}>Keep my edits</button>
      </div>}
      {replaceKeychain && replaceKeychain.id === selected && <div className="secret-warn" role="alert">
        <p>Replace keychain item "{replaceKeychain.name}"?</p>
        <button type="button" className="btn btn-sm" disabled={busy} onClick={() => {
          const pending = replaceKeychain;
          setReplaceKeychain(null);
          const current = tabsRef.current.find(item => item.id === pending.id);
          if (current?.draft === pending.draft) void storeSecret(pending.finding, "keychain", pending.name, true);
        }}>Replace</button>
        <button type="button" className="btn btn-sm" disabled={busy} onClick={() => setReplaceKeychain(null)}>Cancel</button>
      </div>}
      {tab ? <div className="request-response" role="tabpanel" aria-label={tab.draft.step.name}>
        <RequestEditor secretStoreVersion={tab.secretStoreVersion ?? 0} key={`request-${selected}`} info={info} environment={environment} result={latest ?? (tab.sent?.result?.name === tab.draft.step.name ? tab.sent.result : undefined)} at={tab.at} draft={tab.draft} findings={tab.findings} onStoreSecret={storeSecret} onChange={edit} tab={tab.section} onTab={section => updateTab(selected!, current => ({ ...current, section }))} dirty={isDirty(tab)} busy={busy} onSave={() => { void perform(false); }} onSend={() => { void perform(true); }} />
        <ResponsePane key={`response-${selected}`} result={ownSend?.result ?? latest} note={ownSend?.note} environment={ownSend?.run.environment ?? (latest ? lastRun?.environment : undefined)} busy={busy} onAssert={addAssertion} onCapture={addCapture} />
      </div> : <div className="empty workspace-empty">Open a request from the collection to get started.</div>}
    </main>
  </div>;
}
