// api.ts wraps the Go bindings with typed functions.
//
// Inside the desktop app the bindings are the Wails-bound App methods on
// window.go.main.App. Under `npm run dev` in a plain browser they come from
// the dev bridge (vite.bridge.ts), which forwards each call to
// `verity gui-call <Method>`; both end up in core/workspace, so they behave
// the same. Anywhere else a clear error is raised.

import { subscribeGitProgress, subscribeProjectChanged } from "./gitBus";
import { subscribeLoadLive, subscribeRunEvents } from "./runBus";
import type {
  Catalog,
  GitStashInfo,
  GitStashRequest,
  GitStashActionRequest,
  GitBranchInfo,
  GitBranchWriteResult,
  GitCommitRequest,
  GitCommitResult,
  GitCreateBranchRequest,
  GitFetchResult,
  GitInfo,
  GitProgress,
  GitPullRequest,
  GitPullResult,
  GitPushResult,
  GitSwitchRequest,
  GitUpstreamRequest,
  ImportPreview,
  ImportSaveRequest,
  LoadLiveStats,
  LoadOptions,
  LoadReport,
  LoadTargetReport,
  OAuthRequest,
  ProjectChanged,
  ProjectChanges,
  ProjectInfo,
  RunEvent,
  RunOptions,
  RunResult,
  SaveEnvironmentRequest,
  SaveLoadReportRequest,
  SaveStepRequest,
  SaveStepResult,
  SecretFinding,
  Step,
  ValidateResult,
  GraphQLTemplate,
  GraphQLTemplateRequest,
} from "./types";

interface GoBindings {
  OAuthLogin(req: OAuthRequest): Promise<void>;
  OAuthLogout(req: OAuthRequest): Promise<void>;
  OAuthStatus(req: OAuthRequest): Promise<boolean>;
  Catalog(): Promise<Catalog>;
  ValidateProject(path: string, environment: string): Promise<ValidateResult>;
  GraphQLTemplates(req: GraphQLTemplateRequest): Promise<GraphQLTemplate[]>;
  InspectProject(path: string): Promise<ProjectInfo>;
  RunProject(opts: RunOptions): Promise<RunResult>;
  /** Desktop app and ?mock. Absent on the dev bridge, which does not stream. */
  CancelRun?(): Promise<void>;
  StartLoad(opts: LoadOptions): Promise<LoadReport>;
  /** Desktop app and ?mock. Absent on the dev bridge, which does not stream. */
  CancelLoad?(): Promise<void>;
  LoadTargets(opts: LoadOptions): Promise<LoadTargetReport>;
  SaveLoadReport(req: SaveLoadReportRequest): Promise<string>;
  SaveStep(req: SaveStepRequest): Promise<SaveStepResult>;
  CheckStep(step: Step): Promise<SecretFinding[]>;
  KeychainSecretExists(name: string): Promise<boolean>;
  StoreKeychainSecret(name: string, value: string): Promise<string>;
  SaveEnvironment(req: SaveEnvironmentRequest): Promise<SaveStepResult>;
  ImportPreview(kind: string, spec: string, envs: string[]): Promise<ImportPreview>;
  ImportSave(req: ImportSaveRequest): Promise<string>;
  /** Native open-file dialog; resolves "" when cancelled. Desktop app only. */
  PickFile(title: string): Promise<string>;
  GitStatus(path: string): Promise<GitInfo>;
  GitChanges(path: string): Promise<ProjectChanges>;
  GitStashes(path: string): Promise<GitStashInfo[]>;
  GitStash(req: GitStashRequest): Promise<GitPullResult>;
  GitStashAction(req: GitStashActionRequest): Promise<GitPullResult>;
  GitStashChanges(req: GitStashActionRequest): Promise<ProjectChanges>;
  GitCommit(req: GitCommitRequest): Promise<GitCommitResult>;
  GitPull(req: GitPullRequest): Promise<GitPullResult>;
  GitFetch(path: string): Promise<GitFetchResult>;
  GitPush(path: string): Promise<GitPushResult>;
  GitPushUpstream(req: GitUpstreamRequest): Promise<GitPushResult>;
  GitBranches(path: string): Promise<GitBranchInfo[]>;
  GitSwitch(req: GitSwitchRequest): Promise<GitBranchWriteResult>;
  GitCreateBranch(req: GitCreateBranchRequest): Promise<GitBranchWriteResult>;
  GitMergeAbort(path: string): Promise<string>;
  OpenFile(projectPath: string, rel: string): Promise<void>;
  /** Desktop app and ?mock. Absent on the dev bridge, which cannot cancel another process. */
  CancelGit?(): Promise<void>;
  /** Desktop app only. The dev bridge does not watch files. */
  WatchProject?(path: string): Promise<void>;
  UnwatchProject?(): Promise<void>;
}

// httpBindings calls the dev bridge, which answers with {ok, value | error}.
const httpBindings = new Proxy({} as GoBindings, {
  get: (_, method: string) => async (...args: unknown[]) => {
    // vite.bridge.ts refuses requests without this header (cross-site guard).
    const res = await fetch(`/__verity/${method}`, {
      method: "POST",
      headers: { "X-Verity-Bridge": "1" },
      body: JSON.stringify(args),
    });
    const out = await res.json();
    if (!out.ok) throw new Error(out.error);
    return out.value;
  },
});

// mockBindings serves fixture data (src/dev/mock.ts) when the URL has ?mock,
// so views can be developed without the Go backend. Dev builds only.
const mockBindings = import.meta.env.DEV && new Proxy({} as GoBindings, {
  get: (_, method: string) => async (...args: unknown[]) => {
    const { mockBindings: m } = await import("./dev/mock");
    return (m as unknown as Record<string, (...a: unknown[]) => Promise<unknown>>)[method](...args);
  },
});

const useMock = () => mockMode();

function bindings(): GoBindings {
  const w = window as unknown as { go?: { main?: { App?: GoBindings } } };
  const app = w.go?.main?.App;
  if (!app && mockBindings && useMock()) return mockBindings;
  if (!app && import.meta.env.DEV) return httpBindings;
  if (!app) {
    throw new Error(
      "Wails bindings unavailable. Run inside the desktop app (wails dev) — see gui/README.md."
    );
  }
  return app;
}

/** True when native dialogs (PickFile) are available. */
export const hasNativeDialogs = () =>
  (typeof window !== "undefined" && !!(window as unknown as { go?: unknown }).go) || useMock();

type WailsRuntime = {
  EventsOn?: (name: string, cb: (event: RunEvent) => void) => (() => void) | void;
  EventsOff?: (name: string) => void;
};

/** True in a Vite dev build opened with ?mock. */
export function mockMode(): boolean {
  return import.meta.env.DEV && typeof location !== "undefined" && new URLSearchParams(location.search).has("mock");
}

/** True when the desktop app can watch the project directory. */
export function canWatchProject(): boolean {
  const app = (window as unknown as { go?: { main?: { App?: GoBindings } } }).go?.main?.App;
  return typeof app?.WatchProject === "function";
}

/** Subscribe to run progress. Wails delivers "run:event"; ?mock uses a local bus. */
export function onRunEvent(handler: (event: RunEvent) => void): () => void {
  const runtime = (window as unknown as { runtime?: WailsRuntime }).runtime;
  if (runtime?.EventsOn) {
    const off = runtime.EventsOn("run:event", handler);
    return () => {
      if (typeof off === "function") off();
      else runtime.EventsOff?.("run:event");
    };
  }
  return subscribeRunEvents(handler);
}

/** Subscribe to load-test live stats. Wails delivers "load:live"; ?mock uses a local bus. */
export function onLoadLive(handler: (stats: LoadLiveStats) => void): () => void {
  const runtime = typeof window !== "undefined" ? (window as unknown as { runtime?: WailsRuntime }).runtime : undefined;
  if (runtime?.EventsOn) {
    const off = runtime.EventsOn("load:live", handler as unknown as (event: RunEvent) => void);
    return () => {
      if (typeof off === "function") off();
      else runtime.EventsOff?.("load:live");
    };
  }
  return subscribeLoadLive(handler);
}

/** Subscribe to external project edits. Wails delivers "project:changed"; ?mock uses a local bus. */
export function onProjectChanged(handler: (event: ProjectChanged) => void): () => void {
  const runtime = (window as unknown as { runtime?: WailsRuntime }).runtime;
  if (runtime?.EventsOn) {
    const off = runtime.EventsOn("project:changed", handler as unknown as (event: RunEvent) => void);
    return () => {
      if (typeof off === "function") off();
      else runtime.EventsOff?.("project:changed");
    };
  }
  if (mockMode()) return subscribeProjectChanged(handler);
  return () => {};
}

/** Subscribe to git progress. Wails delivers "git:progress"; ?mock uses a local bus. */
export function onGitProgress(handler: (event: GitProgress) => void): () => void {
  const runtime = (window as unknown as { runtime?: WailsRuntime }).runtime;
  if (runtime?.EventsOn) {
    const off = runtime.EventsOn("git:progress", handler as unknown as (event: RunEvent) => void);
    return () => {
      if (typeof off === "function") off();
      else runtime.EventsOff?.("git:progress");
    };
  }
  if (mockMode()) return subscribeGitProgress(handler);
  return () => {};
}

export const api = {
  oauthLogin: (req: OAuthRequest) => bindings().OAuthLogin(req),
  oauthLogout: (req: OAuthRequest) => bindings().OAuthLogout(req),
  oauthStatus: (req: OAuthRequest) => bindings().OAuthStatus(req),
  catalog: () => bindings().Catalog(),
  validate: (path: string, environment = "") => bindings().ValidateProject(path, environment),
  graphqlTemplates: (req: GraphQLTemplateRequest) => bindings().GraphQLTemplates(req),
  inspect: (path: string) => bindings().InspectProject(path),
  run: (opts: RunOptions) => bindings().RunProject(opts),
  cancel: async () => {
    const app = (window as unknown as { go?: { main?: { App?: GoBindings } } }).go?.main?.App;
    if (app?.CancelRun) {
      await app.CancelRun();
      return;
    }
    if (useMock()) {
      const { cancelMockRun } = await import("./dev/mock");
      cancelMockRun();
    }
  },
  startLoad: (opts: LoadOptions) => bindings().StartLoad(opts),
  cancelLoad: async () => {
    const app = (window as unknown as { go?: { main?: { App?: GoBindings } } }).go?.main?.App;
    if (app?.CancelLoad) {
      await app.CancelLoad();
      return;
    }
    if (useMock()) {
      const { cancelMockLoad } = await import("./dev/mock");
      cancelMockLoad();
    }
  },
  loadTargets: (opts: LoadOptions) => bindings().LoadTargets(opts),
  saveLoadReport: (req: SaveLoadReportRequest) => bindings().SaveLoadReport(req),
  saveStep: (req: SaveStepRequest) => bindings().SaveStep(req),
  /** Plaintext credentials in a step being edited; SaveStep refuses any. */
  checkStep: (step: Step) => bindings().CheckStep(step),
  keychainSecretExists: (name: string) => bindings().KeychainSecretExists(name),
  storeKeychainSecret: (name: string, value: string) => bindings().StoreKeychainSecret(name, value),
  saveEnvironment: (req: SaveEnvironmentRequest) => bindings().SaveEnvironment(req),
  importPreview: (kind: string, spec: string, envs: string[] = []) =>
    bindings().ImportPreview(kind, spec, envs),
  importSave: (req: ImportSaveRequest) => bindings().ImportSave(req),
  pickFile: (title: string) => bindings().PickFile(title),
  gitStatus: (path: string) => bindings().GitStatus(path),
  gitChanges: (path: string) => bindings().GitChanges(path),
  gitStashes: (path: string) => bindings().GitStashes(path),
  gitStash: (req: GitStashRequest) => bindings().GitStash(req),
  gitStashAction: (req: GitStashActionRequest) => bindings().GitStashAction(req),
  gitStashChanges: (req: GitStashActionRequest) => bindings().GitStashChanges(req),
  gitCommit: (req: GitCommitRequest) => bindings().GitCommit(req),
  gitPull: (req: GitPullRequest) => bindings().GitPull(req),
  gitFetch: (path: string) => bindings().GitFetch(path),
  gitPush: (path: string) => bindings().GitPush(path),
  gitPushUpstream: (req: GitUpstreamRequest) => bindings().GitPushUpstream(req),
  gitBranches: (path: string) => bindings().GitBranches(path),
  gitSwitch: (req: GitSwitchRequest) => bindings().GitSwitch(req),
  gitCreateBranch: (req: GitCreateBranchRequest) => bindings().GitCreateBranch(req),
  gitMergeAbort: (path: string) => bindings().GitMergeAbort(path),
  openFile: (projectPath: string, rel: string) => bindings().OpenFile(projectPath, rel),
  cancelGit: async () => {
    const app = (window as unknown as { go?: { main?: { App?: GoBindings } } }).go?.main?.App;
    if (app?.CancelGit) {
      await app.CancelGit();
      return;
    }
    if (useMock()) {
      const { cancelMockGit } = await import("./dev/mock");
      cancelMockGit();
    }
  },
  watchProject: (path: string) => bindings().WatchProject?.(path) ?? Promise.resolve(),
  unwatchProject: () => bindings().UnwatchProject?.() ?? Promise.resolve(),
};
