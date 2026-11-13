import { useEffect, useRef, useState } from "react";
import { api, canWatchProject, hasNativeDialogs, mockMode, onLoadLive, onProjectChanged, onRunEvent } from "./api";
import { emitProjectChanged } from "./gitBus";
import { Clock, Download, Folder, Gauge, GitBranch, Globe, Layers, Logo, Moon, Play, Search, Stop, Sun } from "./icons";
import { branchLabel, changedCount, changedFilePaths, isCurrentGeneration, nextGeneration, stepBaseline } from "./lib/git";
import { protocolList } from "./lib/protocols";
import { applyTheme, fileName, initialTheme, loadRunSettings, recentProjects, rememberProject, saveRunSettings } from "./lib/storage";
import type { RunSettings } from "./lib/storage";
import type { StepLocation } from "./lib/workspace";
import type { GitInfo, LoadLiveStats, LoadOptions, LoadReport, ProjectChanged, ProjectInfo, RunOptions, RunResult, Step } from "./types";
import { applyRunEvent, blankRun, countPlannedSteps } from "./views/parts/run";
import { ChangesView } from "./views/ChangesView";
import { EnvironmentsView } from "./views/EnvironmentsView";
import { ImportView } from "./views/ImportView";
import { LoadView } from "./views/LoadView";
import { RunnerView } from "./views/RunnerView";
import { WorkspaceView } from "./views/WorkspaceView";
import "./workspace.css";

type View = "workspace" | "runner" | "load" | "environments" | "changes" | "import";

interface DiskEvent {
  id: number;
  files: string[];
  git: boolean;
}

export function App() {
  const [info, setInfo] = useState<ProjectInfo | null>(null);
  const [environment, setEnvironment] = useState("");
  const [lastRun, setLastRun] = useState<RunResult | null>(null);
  const [running, setRunning] = useState(false);
  const [lastLoad, setLastLoad] = useState<LoadReport | null>(null);
  const [loadLive, setLoadLive] = useState<LoadLiveStats | null>(null);
  const [loadRunning, setLoadRunning] = useState(false);
  const [planned, setPlanned] = useState(0);
  const [runSettings, setRunSettings] = useState<RunSettings>(loadRunSettings);
  const [saving, setSaving] = useState(false);
  const [opening, setOpening] = useState(false);
  const [view, setView] = useState<View>("workspace");
  const [openStep, setOpenStep] = useState<StepLocation | null>(null);
  const [theme, setTheme] = useState(initialTheme);
  const [recents, setRecents] = useState(recentProjects);
  const [path, setPath] = useState("");
  const [error, setError] = useState("");
  const [dirty, setDirty] = useState(false);
  const [filter, setFilter] = useState("");
  const [git, setGit] = useState<GitInfo | null>(null);
  const [gitEpoch, setGitEpoch] = useState(0);
  const [disk, setDisk] = useState<DiskEvent | null>(null);
  const searchInput = useRef<HTMLInputElement>(null);
  const diskSeq = useRef(0);
  const inspectGen = useRef(0);
  const gitGen = useRef(0);
  const runLock = useRef(false);
  const runGen = useRef(0);
  const projectPathRef = useRef<string | null>(null);
  const runningRef = useRef(false);
  const loadRunningRef = useRef(false);
  const openLock = useRef(false);
  const busy = running || loadRunning || saving || opening;
  // The footer lists the protocols the backend actually registers.
  const [protocols, setProtocols] = useState("");
  useEffect(() => {
    let live = true;
    // Promise.resolve defers the call so a missing binding becomes a rejection.
    Promise.resolve().then(() => api.catalog()).then((c) => { if (live) setProtocols(protocolList(c.protocols)); }).catch(() => undefined);
    return () => { live = false; };
  }, []);
  useEffect(() => {
    function focusSearch(event: KeyboardEvent) {
      if (info && (event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault(); setView("workspace"); searchInput.current?.focus();
      }
    }
    window.addEventListener("keydown", focusSearch);
    return () => window.removeEventListener("keydown", focusSearch);
  }, [info]);

  useEffect(() => {
    return onRunEvent((event) => {
      if (!runningRef.current || !info) return;
      setLastRun((prev) => applyRunEvent(prev ?? blankRun(info.project.name, environment), event));
    });
  }, [info, environment]);

  useEffect(() => {
    return onLoadLive((stats) => {
      if (!loadRunningRef.current) return;
      setLoadLive(stats);
    });
  }, []);

  useEffect(() => {
    if (!info) { setGit(null); return; }
    const path = info.path;
    const token = (gitGen.current = nextGeneration(gitGen.current));
    void api.gitStatus(path).then((next) => {
      if (isCurrentGeneration(token, gitGen.current)) setGit(next);
    }).catch((err) => {
      if (isCurrentGeneration(token, gitGen.current)) setError(String(err));
    });
  }, [info?.path]);

  useEffect(() => {
    if (!info) return;
    const path = info.path;
    const off = onProjectChanged((event: ProjectChanged) => {
      setGitEpoch((n) => n + 1);
      const inspectToken = (inspectGen.current = nextGeneration(inspectGen.current));
      const statusToken = (gitGen.current = nextGeneration(gitGen.current));
      void api.inspect(path).then((next) => {
        if (isCurrentGeneration(inspectToken, inspectGen.current)) {
          diskSeq.current += 1;
          setDisk({ id: diskSeq.current, files: event.files ?? [], git: !!event.git });
          setInfo(next);
        }
      }).catch((err) => {
        if (isCurrentGeneration(inspectToken, inspectGen.current)) setError(String(err));
      });
      void api.gitStatus(path).then((next) => {
        if (isCurrentGeneration(statusToken, gitGen.current)) setGit(next);
      }).catch((err) => {
        if (isCurrentGeneration(statusToken, gitGen.current)) setError(String(err));
      });
    });
    if (canWatchProject()) void api.watchProject(path);
    return () => {
      inspectGen.current = nextGeneration(inspectGen.current);
      gitGen.current = nextGeneration(gitGen.current);
      off();
      if (canWatchProject()) void api.unwatchProject();
    };
  }, [info?.path]);

  async function openProject(projectPath: string) {
    if (rejectProjectChangeDuringRun()) return;
    if (!projectPath.trim() || openLock.current) return;
    openLock.current = true;
    setOpening(true); setError("");
    const requested = projectPath.trim();
    const inspectToken = (inspectGen.current = nextGeneration(inspectGen.current));
    gitGen.current = nextGeneration(gitGen.current);
    setGit(null); setDisk(null);
    try {
      const next = await api.inspect(requested);
      if (!isCurrentGeneration(inspectToken, inspectGen.current)) return;
      runGen.current = nextGeneration(runGen.current);
      projectPathRef.current = next.path;
      setInfo(next);
      setEnvironment(next.project.environments?.find(e => e.default)?.name ?? next.project.environments?.[0]?.name ?? "");
      setLastRun(null); setLastLoad(null); setLoadLive(null); setOpenStep(null); setDirty(false); setFilter(""); setView("workspace");
      setGitEpoch(0);
      setRecents(rememberProject(next.path));
    } catch (e) {
      if (isCurrentGeneration(inspectToken, inspectGen.current)) setError(String(e));
    }
    finally { setOpening(false); openLock.current = false; }
  }
  async function pickProject() {
    if (rejectProjectChangeDuringRun()) return;
    setError("");
    try {
      const selected = await api.pickFile("Open project");
      if (selected) await openProject(selected);
    } catch (e) { setError(String(e)); }
  }
  async function saveStep(suite: string, caseName: string, index: number, step: Step, baseline?: string, fileBaseline?: string): Promise<string> {
    if (!info) throw new Error("Open a project before saving.");
    const stepHash = baseline?.trim() ?? "";
    const fileHash = fileBaseline?.trim() ?? "";
    if (!stepHash && !fileHash) throw new Error("Reload this request before saving. The editor has no baseline hash.");
    setSaving(true);
    const inspectToken = (inspectGen.current = nextGeneration(inspectGen.current));
    const statusToken = (gitGen.current = nextGeneration(gitGen.current));
    try {
      await api.saveStep({
        path: info.path, suite, case: caseName, index, step,
        ...(stepHash ? { baseline: stepHash } : {}),
        ...(!stepHash && fileHash ? { fileBaseline: fileHash } : {}),
      });
      const next = await api.inspect(info.path);
      const gitNext = await api.gitStatus(info.path);
      if (isCurrentGeneration(inspectToken, inspectGen.current)) setInfo(next);
      if (isCurrentGeneration(statusToken, gitGen.current)) setGit(gitNext);
      setGitEpoch((n) => n + 1);
      return stepBaseline(next, suite, caseName, index);
    } finally { setSaving(false); }
  }
  async function refreshAfterGit(worktree: boolean) {
    if (!info) return;
    const path = info.path;
    const previous = info;
    const statusToken = (gitGen.current = nextGeneration(gitGen.current));
    try {
      const gitNext = await api.gitStatus(path);
      if (isCurrentGeneration(statusToken, gitGen.current)) setGit(gitNext);
      if (!worktree) {
        setGitEpoch((n) => n + 1);
        return;
      }
      const inspectToken = (inspectGen.current = nextGeneration(inspectGen.current));
      const next = await api.inspect(path);
      if (!isCurrentGeneration(inspectToken, inspectGen.current)) return;
      diskSeq.current += 1;
      setDisk({ id: diskSeq.current, files: changedFilePaths(previous, next), git: true });
      setInfo(next);
      setGitEpoch((n) => n + 1);
    } catch (err) {
      if (isCurrentGeneration(statusToken, gitGen.current)) setError(String(err));
    }
  }
  async function refreshProject() {
    if (!info) return;
    setSaving(true);
    const inspectToken = (inspectGen.current = nextGeneration(inspectGen.current));
    const statusToken = (gitGen.current = nextGeneration(gitGen.current));
    try {
      const next = await api.inspect(info.path);
      const gitNext = await api.gitStatus(info.path);
      if (isCurrentGeneration(inspectToken, inspectGen.current)) setInfo(next);
      if (isCurrentGeneration(statusToken, gitGen.current)) setGit(gitNext);
      setGitEpoch((n) => n + 1);
    } finally { setSaving(false); }
  }
  async function run(opts: Pick<RunOptions, "suites" | "cases" | "untilStep"> = {}, collection = true): Promise<RunResult> {
    if (!info) throw new Error("Open a project before running.");
    if (openLock.current) throw new Error("Wait for the project to finish opening before starting a run.");
    if (runLock.current) throw new Error("A run is already in progress.");
    const previous = lastRun;
    runLock.current = true;
    runGen.current = nextGeneration(runGen.current);
    runningRef.current = true;
    setRunning(true);
    setError("");
    setPlanned(countPlannedSteps(info.project, opts));
    setLastRun(blankRun(info.project.name, environment));
    if (collection) setView("runner");
    try {
      const result = await api.run({ path: info.path, environment, parallel: 1, vars: {}, ...runSettings, ...opts });
      runningRef.current = false;
      setLastRun(result);
      return result;
    } catch (err) {
      runningRef.current = false;
      setLastRun(previous);
      throw err;
    } finally { runLock.current = false; setRunning(false); }
  }
  function runCollection(opts: { suites?: string[]; cases?: string[] } = {}) {
    void run(opts).catch(e => setError(String(e)));
  }
  async function startLoad(opts: LoadOptions): Promise<LoadReport> {
    if (!info) throw new Error("Open a project before running a load test.");
    if (openLock.current) throw new Error("Wait for the project to finish opening before starting a load test.");
    if (runLock.current) throw new Error("A run is already in progress.");
    if (opts.path !== info.path) throw new Error("The project changed. Reopen the Load test view before starting.");
    const projectPath = info.path;
    const token = (runGen.current = nextGeneration(runGen.current));
    runLock.current = true;
    loadRunningRef.current = true;
    setLoadRunning(true);
    // Keep the previous report until a new (possibly partial) report replaces it.
    setLoadLive(null);
    setError("");
    try {
      const rep = await api.startLoad(opts);
      if (isCurrentGeneration(token, runGen.current) && projectPathRef.current === projectPath) {
        setLastLoad(rep);
      }
      return rep;
    } finally {
      if (isCurrentGeneration(token, runGen.current)) {
        loadRunningRef.current = false;
        runLock.current = false;
        setLoadRunning(false);
      }
    }
  }
  function rejectProjectChangeDuringRun() {
    if (!runLock.current) return false;
    setError("Wait for the active run or load test to finish, or cancel it, before opening or importing another project.");
    return true;
  }
  function navigate(next: View) {
    if (next === "import" && rejectProjectChangeDuringRun()) return;
    setView(next);
  }
  function changeTheme() {
    const next = theme === "dark" ? "light" : "dark";
    applyTheme(next); setTheme(next);
  }
  function goHome() {
    if (rejectProjectChangeDuringRun()) return;
    if (dirty && !window.confirm("Discard unsaved changes and close this project?")) return;
    runGen.current = nextGeneration(runGen.current);
    projectPathRef.current = null;
    setInfo(null); setLastRun(null); setLastLoad(null); setLoadLive(null); setGit(null); setDisk(null); setView("workspace"); setDirty(false); setError("");
  }
  const selectedEnv = info?.project.environments?.find(e => e.name === environment);
  const navigation = [
    { id: "workspace" as const, label: "Workspace", Icon: Layers },
    { id: "environments" as const, label: "Environments", Icon: Globe },
    { id: "changes" as const, label: "Source control", Icon: GitBranch },
    { id: "runner" as const, label: "Runs", Icon: Clock },
    { id: "load" as const, label: "Load test", Icon: Gauge },
    { id: "import" as const, label: "Import", Icon: Download },
  ];

  return <div className="verity-app">
    <header className="app-header">
      <div className="brand"><Logo size={22} /><strong>Verity</strong></div>
      {info ? <>
        <div className="project-crumb"><strong>{info.project.name}</strong><span>/</span><span className="mono" title={info.path}>{fileName(info.path)}</span></div>
        <span className="save-state">{saving ? "Saving…" : dirty ? "Unsaved changes" : "Saved"}</span>
        <div className="header-search-wrap"><label className="header-search"><Search size={15} /><input ref={searchInput} type="search" aria-label="Search requests, variables, assertions" placeholder="Search requests, variables, assertions" value={filter} onChange={e => { setFilter(e.target.value); setView("workspace"); }} /><kbd>⌘/Ctrl K</kbd></label></div>
        <label className="environment-picker"><Globe size={15} /><span>Env</span>
          <select aria-label="Environment" disabled={busy} value={environment} onChange={e => setEnvironment(e.target.value)}>
            {!info.project.environments?.length && <option value="">Default</option>}
            {info.project.environments?.map(env => <option key={env.name} value={env.name}>{env.name}</option>)}
          </select>
        </label>
        {running || loadRunning ? (
          <button className="btn" onClick={() => { void (loadRunning ? api.cancelLoad() : api.cancel()); }}><Stop size={14} />Cancel</button>
        ) : (
          <button className="btn" disabled={busy} onClick={() => runCollection()} title={dirty ? "Run the saved collection; save your drafts to include edits" : "Run collection"}><Play size={14} />Run collection</button>
        )}
        <button className="icon-btn" aria-label="Close project and open another" title="Open another project" disabled={busy} onClick={goHome}><Folder /></button>
      </> : <span className="header-spacer muted">API contract testing</span>}
      <button className="icon-btn" onClick={changeTheme} aria-label={`Switch to ${theme === "dark" ? "light" : "dark"} theme`} title="Toggle theme">{theme === "dark" ? <Sun /> : <Moon />}</button>
    </header>
    {error && <div className="error-banner" role="alert">{error}<button className="btn btn-sm" onClick={() => setError("")}>Dismiss</button></div>}
    <div className="app-body">
      {info && <nav className="activity-rail" aria-label="Sections">
        {navigation.map(({ id, label, Icon }) => <button key={id} className="icon-btn rail-button" aria-label={label} title={label} aria-current={view === id ? "page" : undefined} onClick={() => navigate(id)}><Icon size={19} /></button>)}
      </nav>}
      <div className="app-content">
        {info && <ChangesView key={info.path} active={view === "changes"} path={info.path} epoch={gitEpoch} dirty={dirty} onChanged={(worktree) => { void refreshAfterGit(worktree); }} onOpen={(at) => { setOpenStep(at); setView("workspace"); }} />}
        {info && <div className="workspace-host" hidden={view !== "workspace"}>
          <WorkspaceView key={info.path} info={info} environment={environment} lastRun={lastRun} running={busy} active={view === "workspace"} openStep={openStep}
            filter={filter} onFilter={setFilter} changedSteps={git?.changedSteps}
            disk={disk}
            onSaveStep={saveStep} onSend={at => run({ suites: [at.suite], cases: [at.caseName], untilStep: at.index + 1 }, false)}
            onImport={() => navigate("import")} onDirtyChange={setDirty} />
        </div>}
        {info && view === "runner" && <RunnerView info={info} result={lastRun} running={running} planned={planned}
          settings={runSettings} onSettings={next => { setRunSettings(next); saveRunSettings(next); }} onCancel={() => { void api.cancel(); }} onRun={runCollection}
          onOpenStep={(suite, caseName, index) => { setOpenStep({ suite, caseName, index }); setView("workspace"); }} onSaveStep={saveStep} />}
        {info && view === "load" && <LoadView info={info} environment={environment} report={lastLoad} live={loadLive}
          running={loadRunning} settings={runSettings} onStart={startLoad} onCancel={() => { void api.cancelLoad(); }} />}
        {info && view === "environments" && <EnvironmentsView info={info} environment={environment} onSelectEnvironment={setEnvironment} onChanged={refreshProject} />}
        {view === "import" && <>
          {!info && <button className="btn welcome-back" onClick={() => setView("workspace")}>Back to welcome</button>}
          {running || loadRunning ? <p className="inline-error" role="alert">Wait for the active run or load test to finish, or cancel it, before importing another project.</p> :
            <ImportView onImported={projectPath => { void openProject(projectPath); }} onCancel={() => setView("workspace")} />}
        </>}
        {!info && view !== "import" && <main className="welcome">
          <div className="welcome-logo"><Logo size={42} /></div>
          <h1>Welcome to Verity</h1><p className="muted">Your APIs. Your contracts. One workspace.</p>
          <form className="open-project" onSubmit={e => { e.preventDefault(); void (hasNativeDialogs() ? pickProject() : openProject(path)); }}>
            {!hasNativeDialogs() && <label className="field">Project file<input className="input mono" value={path} onChange={e => setPath(e.target.value)} placeholder="project.yaml" /></label>}
            <button type="submit" className="btn btn-primary" disabled={opening || (!hasNativeDialogs() && !path.trim())}><Folder size={16} />{opening ? "Opening…" : "Open project…"}</button>
          </form>
          <button className="btn" disabled={opening} onClick={() => navigate("import")}><Download size={15} />Import from Postman / OpenAPI / WSDL</button>
          <section className="recent-projects"><h2 className="label">Recent projects</h2>
            {recents.length ? recents.map(recent => <button className="recent-project" key={recent} disabled={opening} onClick={() => { void openProject(recent); }}><Folder size={16} /><span><strong>{fileName(recent)}</strong><small className="mono">{recent}</small></span></button>) : <p className="muted">Projects you open will appear here.</p>}
          </section>
        </main>}
      </div>
    </div>
    <footer className="status-bar">
      {lastRun ? <button className="status-link" onClick={() => setView("runner")}><span className={`dot status-${lastRun.summary.status}`} />Last run ({lastRun.environment || "default"}): {lastRun.summary.passed} checks passed · {lastRun.summary.failed} failed · {lastRun.summary.errors} errors</button> : <span>No runs yet</span>}
      {info && <span>{environment || "Default"} · {Object.keys(selectedEnv?.variables ?? {}).length} variables · {info.envRefs.filter(ref => ref.set).length} of {info.envRefs.length} secrets set</span>}
      {info && git && (git.available && git.repo ? <span className="git-status">
        <GitBranch size={13} />
        <span>{branchLabel(git)}</span>
        <button className="status-link" onClick={() => setView("changes")}>{changedCount(git)} changed</button>
        {git.upstreamGone ? <span>upstream gone</span> : git.tracking && <span title={`Ahead ${git.ahead}, behind ${git.behind}${git.upstream ? ` (${git.upstream})` : ""}`}>↑{git.ahead} ↓{git.behind}</span>}
      </span> : <span className="muted">Not in a git repository</span>)}
      {info && mockMode() && <button className="btn btn-sm" onClick={() => {
        void import("./dev/mock").then(({ noteMockDiskChange }) => {
          noteMockDiskChange();
          emitProjectChanged({ files: [info.path], git: true });
        });
      }}>Simulate disk change</button>}
      {protocols && <span className="protocol-list">{protocols}</span>}
    </footer>
  </div>;
}
