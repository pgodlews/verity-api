import { useCallback, useEffect, useRef, useState } from "react";
import { api, mockMode, onGitProgress } from "../api";
import { ChevronDown, ChevronRight, Stop } from "../icons";
import { isCurrentGeneration, nextGeneration } from "../lib/git";
import {
  actionEnablement,
  aheadBehindLabel,
  begin,
  commitEnablement,
  confirmsWorktree,
  fail,
  fileActionsLocked,
  idleMachine,
  noteProgress,
  pullOutcome,
  pushOutcome,
  reconcileSelection,
  rewroteWorktree,
  splitFiles,
  stashConfirmation,
  stashOutcome,
  succeed,
  switchOutcome,
  toolbarLocked,
  upstreamPrompt,
  worktreeWarning,
} from "../lib/gitops";
import type { Machine, OpName } from "../lib/gitops";
import type { StepLocation } from "../lib/workspace";
import type { GitBranchInfo, GitFile, GitInfo, GitStashInfo, ProjectChanges } from "../types";
import { SemanticChanges } from "./SourceControlDiff";

export function SourceControl({ path, epoch, dirty, active = true, onOpen, onChanged }: {
  path: string;
  epoch: number;
  dirty: boolean;
  /** When false the file list is hidden, but Cancel and dialogs stay mounted. */
  active?: boolean;
  onOpen: (at: StepLocation) => void;
  /** worktree is true when the operation may have replaced files on disk. */
  onChanged: (worktree: boolean) => void;
}) {
  const [status, setStatus] = useState<GitInfo | null>(null);
  const [changes, setChanges] = useState<ProjectChanges | null>(null);
  const [stashes, setStashes] = useState<GitStashInfo[]>([]);
  const [stashMessage, setStashMessage] = useState("");
  const [includeUntracked, setIncludeUntracked] = useState(false);
  const [stashSelected, setStashSelected] = useState(false);
  const [stashDiff, setStashDiff] = useState<{ id: string; changes: ProjectChanges } | null>(null);
  const [stashError, setStashError] = useState("");
  const [branches, setBranches] = useState<GitBranchInfo[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [message, setMessage] = useState("");
  const [machine, setMachine] = useState<Machine>(idleMachine);
  const [showOther, setShowOther] = useState(false);
  const [creating, setCreating] = useState(false);
  const [branchName, setBranchName] = useState("");
  const [switchTo, setSwitchTo] = useState(true);
  const [pending, setPending] = useState<{ text: string; run: () => void; label?: string } | null>(null);
  const [remotes, setRemotes] = useState<string[]>([]);
  const [chosenRemote, setChosenRemote] = useState("");
  const [opened, setOpened] = useState("");
  const memory = useRef<{ path: string; choices: Map<string, boolean> }>({ path: "", choices: new Map() });
  const statusGen = useRef(0);
  const changesGen = useRef(0);
  const branchGen = useRef(0);
  const execGen = useRef(0);
  const busyRef = useRef(false);
  const machineRef = useRef(machine);
  machineRef.current = machine;

  const load = useCallback(async () => {
    const statusToken = (statusGen.current = nextGeneration(statusGen.current));
    const changesToken = (changesGen.current = nextGeneration(changesGen.current));
    const branchToken = (branchGen.current = nextGeneration(branchGen.current));
    const [statusRes, changesRes, branchRes, stashRes] = await Promise.allSettled([
      api.gitStatus(path),
      api.gitChanges(path),
      api.gitBranches(path),
      api.gitStashes(path),
    ]);
    if (isCurrentGeneration(statusToken, statusGen.current)) {
      if (statusRes.status === "fulfilled") {
        const next = statusRes.value;
        if (memory.current.path !== path) memory.current = { path, choices: new Map() };
        const files = next.files ?? [];
        const nextSelected = reconcileSelection(memory.current.choices, files);
        setStatus(next);
        setSelected(nextSelected);
        setMachine((current) => current.hold === "conflict" && !files.some((file) => file.state === "conflicted") ? idleMachine() : current);
      } else if (!busyRef.current && machineRef.current.hold === "") {
        setMachine(fail("", messageOf(statusRes.reason)));
      }
    }
    if (isCurrentGeneration(changesToken, changesGen.current)) {
      if (changesRes.status === "fulfilled") setChanges(changesRes.value);
      else if (!busyRef.current && machineRef.current.hold === "") setMachine(fail("", messageOf(changesRes.reason)));
    }
    if (isCurrentGeneration(branchToken, branchGen.current)) {
      setBranches(branchRes.status === "fulfilled" ? branchRes.value : []);
      setStashes(stashRes.status === "fulfilled" ? stashRes.value : []);
      setStashError(stashRes.status === "rejected" ? messageOf(stashRes.reason) : "");
      setStashDiff((current) => current && stashRes.status === "fulfilled" && stashRes.value.some((stash) => stash.id === current.id) ? current : null);
    }
  }, [path]);

  useEffect(() => {
    void load();
    return () => {
      statusGen.current = nextGeneration(statusGen.current);
      changesGen.current = nextGeneration(changesGen.current);
      branchGen.current = nextGeneration(branchGen.current);
    };
  }, [load, epoch]);

  useEffect(() => {
    execGen.current = nextGeneration(execGen.current);
    busyRef.current = false;
    setStashDiff(null);
    setPending(null);
    setMachine(idleMachine());
    void api.cancelGit();
  }, [path]);

  useEffect(() => onGitProgress((event) => {
    setMachine((current) => current.busy && current.op === event.op ? noteProgress(current, event.line) : current);
  }), []);

  function choose(file: string, on: boolean) {
    const next = on ? [...selected, file] : selected.filter((item) => item !== file);
    if (memory.current.path !== path) memory.current = { path, choices: new Map() };
    memory.current.choices.set(file, on);
    setSelected(next);
  }

  async function reload(worktree: boolean) {
    await load();
    onChanged(worktree);
  }

  async function execute(op: OpName, fn: () => Promise<Machine | void>) {
    if (busyRef.current) return;
    const token = execGen.current;
    busyRef.current = true;
    setPending(null);
    setMachine(begin(op));
    try {
      const next = await fn();
      if (token !== execGen.current) return;
      if (next) setMachine(next);
      const switched = op === "create" && next?.line === "Switched";
      if (op !== "stash-show") await reload(rewroteWorktree(op, next?.hold ?? "", switched));
    } catch (err) {
      if (token !== execGen.current) return;
      setMachine(fail(op, err instanceof Error ? err.message : String(err)));
      if (confirmsWorktree(op)) {
        try { await reload(true); } catch { /* the git error is already on screen */ }
      }
    } finally {
      if (token === execGen.current) busyRef.current = false;
    }
  }

  function guarded(op: OpName, fn: () => Promise<Machine | void>) {
    if (dirty && confirmsWorktree(op)) {
      setPending({ text: worktreeWarning(op), run: () => { void execute(op, fn); } });
      return;
    }
    void execute(op, fn);
  }

  function stashAction(action: "apply" | "pop" | "drop", id: string) {
    const op = `stash-${action}` as OpName;
    const run = () => { void execute(op, async () => {
      const result = await api.gitStashAction({ path, id, action });
      setStashDiff(null);
      return stashOutcome(op, result.status, result.conflicted ?? [], result.output ?? "");
    }); };
    const text = stashConfirmation(action, id, dirty);
    if (text) setPending({ text, run, label: action === "drop" ? "Drop stash" : action === "pop" ? "Pop stash" : "Unsaved edits" });
    else run();
  }

  const files = status?.files ?? [];
  const { project, other } = splitFiles(files);
  const repo = !!status?.repo;
  const frozen = toolbarLocked({ busy: machine.busy, hold: machine.hold, pending: pending != null });
  const filesFrozen = fileActionsLocked({ busy: machine.busy, hold: machine.hold, pending: pending != null });
  const commit = commitEnablement({ repo, selected: selected.length, message, busy: machine.busy, held: filesFrozen });
  const fetchBtn = actionEnablement({ repo, busy: machine.busy, held: machine.hold !== "" || pending != null, detached: !!status?.detached, action: "Fetch from the upstream remote" });
  const pullBtn = actionEnablement({ repo, busy: machine.busy, held: machine.hold !== "" || pending != null, detached: !!status?.detached, action: "Pull (fast-forward only)" });
  const pushBtn = actionEnablement({ repo, busy: machine.busy, held: machine.hold !== "" || pending != null, detached: !!status?.detached, action: status?.tracking ? "Push to the upstream" : "Push" });
  const counts = aheadBehindLabel(!!status?.tracking, status?.ahead ?? 0, status?.behind ?? 0, !!status?.upstreamGone);
  const current = branches.find((branch) => branch.current)?.name ?? status?.branch ?? "";
  const conflicted = conflictPaths(files, machine);
  const remoteName = chosenRemote || machine.remote;

  const away = !active && (machine.busy || machine.hold !== "" || pending != null || conflicted.length > 0 || !!status?.merging || !!status?.rebasing);
  if (!active && !away) return null;

  return <section className={active ? "view-changes" : "sc-floating"}>
    {active && <>
    <header className="changes-header">
      <div>
        <h1 className="view-title">Source control</h1>
        <p className="view-lead">Stage the files you pick, then commit. Fetch, pull, and push use the installed git. Verity never force-pushes and never stores credentials.</p>
      </div>
    </header>
    <div className="sc-toolbar">
      <label className="branch-picker">Branch
        <select aria-label="Branch" disabled={!repo || frozen || branches.length === 0} value={current}
          onChange={(event) => {
            const branch = event.target.value;
            if (!branch || branch === current) return;
            guarded("switch", async () => {
              const result = await api.gitSwitch({ path, branch });
              const next = switchOutcome(result.status, result.files ?? [], result.output ?? "");
              return { ...next, branch };
            });
          }}>
          {branches.length === 0 && <option value="">{status?.detached ? "detached" : current || "—"}</option>}
          {branches.map((branch) => <option key={branch.name} value={branch.name}>{branch.name}{branch.current ? " (current)" : ""}</option>)}
        </select>
      </label>
      <button className="btn" type="button" disabled={frozen || !repo} onClick={() => setCreating((open) => !open)}>New branch</button>
      <button className="btn" type="button" disabled={!fetchBtn.enabled} title={fetchBtn.reason} onClick={() => guarded("fetch", async () => {
        const result = await api.gitFetch(path);
        return succeed(result.output?.split("\n")[0] || "Fetched");
      })}>Fetch</button>
      <button className="btn" type="button" disabled={!pullBtn.enabled} title={pullBtn.reason} onClick={() => guarded("pull", async () => {
        const result = await api.gitPull({ path, mode: "ff-only" });
        return pullOutcome(result.status, result.conflicted ?? [], result.output ?? "");
      })}>Pull</button>
      <button className="btn" type="button" disabled={!pushBtn.enabled} title={pushBtn.reason} onClick={() => guarded("push", async () => {
        const result = await api.gitPush(path);
        setRemotes(result.remotes ?? []);
        setChosenRemote(result.remote ?? "");
        return pushOutcome(result.status, result.remote ?? "", result.branch ?? current, result.output ?? "");
      })}>Push{counts ? ` ${counts}` : ""}</button>
      {machine.busy && machine.op !== "stash-show" && <button className="btn" type="button" onClick={() => { void api.cancelGit(); }}><Stop size={14} />Cancel</button>}
      {machine.line && <span className="muted sc-line">{machine.line}</span>}
    </div>
    {creating && <div className="sc-create">
      <label>Branch name
        <input className="input" value={branchName} onChange={(event) => setBranchName(event.target.value)} disabled={frozen} />
      </label>
      <label className="check-row"><input type="checkbox" checked={switchTo} disabled={frozen} onChange={(event) => setSwitchTo(event.target.checked)} /> Switch to it</label>
      <button className="btn btn-primary" type="button" disabled={frozen || !branchName.trim() || !repo} onClick={() => guarded("create", async () => {
        const name = branchName.trim();
        const result = await api.gitCreateBranch({ path, name, switchTo, force: false });
        if (result.status !== "dirty") setBranchName("");
        const next = switchOutcome(result.status, result.files ?? [], result.output ?? "");
        return { ...next, branch: name, op: result.status === "dirty" ? "create" : next.op };
      })}>Create</button>
    </div>}
    </>}
    {!active && machine.busy && machine.op !== "stash-show" && <button className="btn" type="button" onClick={() => { void api.cancelGit(); }}><Stop size={14} />Cancel</button>}
    {pending && <div className="git-dialog" role="dialog" aria-label={pending.label ?? "Unsaved edits"}>
      <p>{pending.text}</p>
      <div className="sc-actions">
        <button className="btn btn-primary" type="button" onClick={pending.run}>Continue</button>
        <button className="btn" type="button" onClick={() => setPending(null)}>Cancel</button>
      </div>
    </div>}
    {machine.hold === "diverged" && <div className="git-dialog" role="status">
      <p>The branches have diverged, so a fast-forward pull stopped. Pull with merge creates a merge commit. Verity does not rebase.</p>
      <div className="sc-actions">
        <button className="btn btn-primary" type="button" disabled={machine.busy} onClick={() => guarded("pull", async () => {
          const result = await api.gitPull({ path, mode: "merge" });
          return pullOutcome(result.status, result.conflicted ?? [], result.output ?? "");
        })}>Pull with merge</button>
      </div>
    </div>}
    {machine.hold === "upstream" && <div className="git-dialog" role="dialog" aria-label="Set upstream">
      <p>{upstreamPrompt(remoteName, machine.branch || current)}</p>
      {remotes.length > 1 && <label>Remote
        <select aria-label="Remote" value={remoteName} onChange={(event) => setChosenRemote(event.target.value)}>
          {remotes.map((remote) => <option key={remote} value={remote}>{remote}</option>)}
        </select>
      </label>}
      <div className="sc-actions">
        {remoteName && <button className="btn btn-primary" type="button" disabled={machine.busy} onClick={() => guarded("push", async () => {
          const result = await api.gitPushUpstream({ path, remote: remoteName, branch: machine.branch || current });
          return pushOutcome(result.status, result.remote ?? remoteName, result.branch ?? machine.branch, result.output ?? "");
        })}>Push and set upstream</button>}
        <button className="btn" type="button" onClick={() => setMachine(idleMachine())}>Cancel</button>
      </div>
    </div>}
    {machine.hold === "dirty" && <div className="git-dialog" role="status">
      <p>These project files have uncommitted changes, so the switch stopped:</p>
      <ul>{machine.dirty.map((file) => <li key={file} className="mono">{file}</li>)}</ul>
      <p>Switch anyway only if git itself can keep the changes.</p>
      <div className="sc-actions">
        <button className="btn" type="button" disabled={machine.busy || !machine.branch} onClick={() => {
          const branch = machine.branch;
          void execute(machine.op === "create" ? "create" : "switch", async () => {
            if (machine.op === "create") {
              const result = await api.gitCreateBranch({ path, name: branch, switchTo: true, force: true });
              return switchOutcome(result.status, result.files ?? [], result.output ?? "");
            }
            const result = await api.gitSwitch({ path, branch, force: true });
            return switchOutcome(result.status, result.files ?? [], result.output ?? "");
          });
        }}>Switch anyway</button>
        <button className="btn" type="button" onClick={() => setMachine(idleMachine())}>Cancel</button>
      </div>
    </div>}
    {(conflicted.length > 0 || status?.merging || status?.rebasing) && <div className="sc-conflicts" role="status">
      <h2>{status?.rebasing ? "Rebase in progress" : status?.merging ? "Merge in progress" : "Conflicts"}</h2>
      {status?.rebasing && <p>A rebase is in progress. Abort it before committing or switching. Verity did not start this rebase.</p>}
      {conflicted.length > 0 && <p>Resolve them in your editor, then save. Verity reloads. Commit stages the files you select.</p>}
      {!status?.rebasing && conflicted.length === 0 && <p>The merge is still open. Abort it, or commit the files you select once the resolutions are staged.</p>}
      <ul>
        {conflicted.map((file) => <li key={file}>
          <span className="mono">{file}</span>
          <button className="btn btn-sm" type="button" onClick={() => {
            void api.openFile(path, file).then(() => { if (mockMode()) setOpened(file); }).catch((err) => {
              setMachine(fail("abort", err instanceof Error ? err.message : String(err)));
            });
          }}>Open in editor</button>
        </li>)}
      </ul>
      {opened && <p className="muted">Open in editor: {opened}</p>}
      {(status?.merging || status?.rebasing) && <button className="btn" type="button" disabled={machine.busy || pending != null} onClick={() => guarded("abort", async () => {
        await api.gitMergeAbort(path);
        return succeed(status?.rebasing ? "Rebase aborted" : "Merge aborted");
      })}>{status?.rebasing ? "Abort rebase" : "Abort merge"}</button>}
    </div>}
    {(machine.hold === "error" || machine.hold === "diverged" || machine.hold === "conflict") && machine.output &&
      <pre className="git-output" role="alert" tabIndex={0}>{machine.output}</pre>}
    {machine.hold === "error" && <button className="btn" type="button" onClick={() => setMachine(idleMachine())}>Dismiss error</button>}
    {active && <>
    {repo && <section className="sc-files" aria-label="Stashes">
      <h2>Stashes</h2>
      <label>Stash message (optional)
        <input className="input" value={stashMessage} disabled={frozen} onChange={(event) => setStashMessage(event.target.value)} />
      </label>
      <label className="check-row"><input type="checkbox" checked={includeUntracked} disabled={frozen} onChange={(event) => setIncludeUntracked(event.target.checked)} /> Include untracked files</label>
      <label className="check-row"><input type="checkbox" checked={stashSelected} disabled={frozen} onChange={(event) => setStashSelected(event.target.checked)} /> Only checked files</label>
      <button className="btn" type="button" disabled={frozen || (stashSelected && selected.length === 0)} onClick={() => guarded("stash", async () => {
        const result = await api.gitStash({ path, paths: stashSelected ? selected : [], includeUntracked, message: stashMessage });
        setStashMessage("");
        return stashOutcome("stash", result.status, [], result.output ?? "");
      })}>Stash changes</button>
      {stashError && <p role="alert">{stashError}</p>}
      {stashes.length === 0 && !stashError && <p className="muted">No stashes</p>}
      <ul className="sc-file-list">
        {stashes.map((stash) => <li key={stash.id}>
          <span>#{stash.index} {stash.branch}: {stash.message} <time dateTime={stash.date}>{stash.date}</time></span>
          <div className="sc-actions">
            <button className="btn btn-sm" type="button" disabled={frozen} onClick={() => { const token = execGen.current; void execute("stash-show", async () => {
              const data = await api.gitStashChanges({ path, id: stash.id, action: "apply" });
              if (token === execGen.current) setStashDiff({ id: stash.id, changes: data });
              return succeed("Stash diff loaded");
            }); }}>Show diff</button>
            <button className="btn btn-sm" type="button" disabled={frozen} onClick={() => stashAction("apply", stash.id)}>Apply</button>
            <button className="btn btn-sm" type="button" disabled={frozen} onClick={() => stashAction("pop", stash.id)}>Pop</button>
            <button className="btn btn-sm" type="button" disabled={frozen} onClick={() => stashAction("drop", stash.id)}>Drop</button>
          </div>
        </li>)}
      </ul>
    </section>}
    <div className="sc-commit">
      <label className="sc-message">Commit message
        <textarea className="input" rows={3} value={message} disabled={filesFrozen} onChange={(event) => setMessage(event.target.value)} />
      </label>
      <div className="sc-actions">
        <button className="btn btn-primary" type="button" disabled={!commit.enabled} title={commit.reason} onClick={() => guarded("commit", async () => {
          const result = await api.gitCommit({ path, message: message.trim(), paths: selected });
          setMessage("");
          return succeed(result.output?.split("\n")[0] || "Committed");
        })}>Commit</button>
        {!commit.enabled && <span className="muted">{commit.reason}</span>}
      </div>
    </div>
    {status && !status.repo && <p className="empty">Not in a git repository</p>}
    {repo && <div className="sc-files">
      <h2>Files</h2>
      <FileChecks files={project} selected={selected} onToggle={choose} disabled={filesFrozen} />
      {other.length > 0 && <>
        <button className="btn btn-sm sc-other" type="button" aria-expanded={showOther} onClick={() => setShowOther((open) => !open)}>
          {showOther ? <ChevronDown size={14} /> : <ChevronRight size={14} />} Other files ({other.length})
        </button>
        {showOther && <FileChecks files={other} selected={selected} onToggle={choose} disabled={filesFrozen} />}
      </>}
    </div>}
    {stashDiff && <div className="sc-actions"><h2>Stash {stashDiff.id.slice(0, 12)}</h2><button className="btn" type="button" disabled={frozen} onClick={() => setStashDiff(null)}>Working tree diff</button></div>}
    <SemanticChanges data={stashDiff?.changes ?? changes} onOpen={stashDiff ? undefined : onOpen} />
    </>}
  </section>;
}

function FileChecks({ files, selected, onToggle, disabled }: {
  files: GitFile[];
  selected: string[];
  onToggle: (path: string, on: boolean) => void;
  disabled: boolean;
}) {
  if (files.length === 0) return null;
  const chosen = new Set(selected);
  return <ul className="sc-file-list">
    {files.map((file) => <li key={file.path}>
      <label>
        <input type="checkbox" checked={chosen.has(file.path)} disabled={disabled} onChange={(event) => onToggle(file.path, event.target.checked)} />
        <span className="mono">{file.path}</span>
        <span className={`git-state git-state-${file.state}`}>{file.state}</span>
      </label>
    </li>)}
  </ul>;
}

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function conflictPaths(files: GitFile[], machine: Machine): string[] {
  const fromStatus = files.filter((file) => file.state === "conflicted").map((file) => file.path);
  if (fromStatus.length > 0) return fromStatus;
  if (machine.hold === "conflict") return machine.conflicted;
  return [];
}
