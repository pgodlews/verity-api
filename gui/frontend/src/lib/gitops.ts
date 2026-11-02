// Pure helpers for the source-control screen. They decide which files start
// selected, when Commit is enabled, and how a git operation moves between
// idle, running, and the typed results (diverged, conflict, no upstream,
// dirty). The desktop app performs the git calls.

export interface FileChoice {
  path: string;
  projectFile: boolean;
}

export type OpName = "fetch" | "pull" | "push" | "commit" | "switch" | "abort" | "create" | "stash" | "stash-apply" | "stash-pop" | "stash-drop" | "stash-show";

export type Hold = "" | "diverged" | "conflict" | "upstream" | "dirty" | "error";

export interface Machine {
  busy: boolean;
  op: OpName | "";
  hold: Hold;
  output: string;
  line: string;
  conflicted: string[];
  dirty: string[];
  remote: string;
  branch: string;
}

export function defaultSelectedPaths(files: FileChoice[]): string[] {
  return files.filter((file) => file.projectFile).map((file) => file.path);
}

/** previous null means the user has not chosen yet: select project files. */
export function reconcileSelection(previous: string[] | null, files: FileChoice[]): string[] {
  if (previous == null) return defaultSelectedPaths(files);
  const known = new Set(files.map((file) => file.path));
  return previous.filter((path) => known.has(path));
}

export function splitFiles<T extends FileChoice>(files: T[]): { project: T[]; other: T[] } {
  return {
    project: files.filter((file) => file.projectFile),
    other: files.filter((file) => !file.projectFile),
  };
}

export function commitEnablement(input: {
  repo: boolean;
  selected: number;
  message: string;
  busy: boolean;
  held?: boolean;
}): { enabled: boolean; reason: string } {
  if (!input.repo) return { enabled: false, reason: "Not in a git repository" };
  if (input.busy) return { enabled: false, reason: "Wait for the git operation to finish" };
  if (input.held) return { enabled: false, reason: "Finish or cancel the open git dialog" };
  if (input.selected < 1) return { enabled: false, reason: "Select at least one file" };
  if (input.message.trim() === "") return { enabled: false, reason: "Enter a commit message" };
  return { enabled: true, reason: "Commit the selected files" };
}

export function actionEnablement(input: {
  repo: boolean;
  busy: boolean;
  action: string;
  held?: boolean;
  detached?: boolean;
}): { enabled: boolean; reason: string } {
  if (!input.repo) return { enabled: false, reason: "Not in a git repository" };
  if (input.busy) return { enabled: false, reason: "Wait for the git operation to finish" };
  if (input.held) return { enabled: false, reason: "Finish or cancel the open git dialog" };
  if (input.detached) return { enabled: false, reason: "HEAD is detached" };
  return { enabled: true, reason: input.action };
}

/** True while a git operation or a confirmation dialog owns the toolbar. */
export function toolbarLocked(input: { busy: boolean; hold: Hold; pending: boolean }): boolean {
  return input.busy || input.hold !== "" || input.pending;
}

/** Conflicts are resolved in the editor, then staged through the normal Commit flow. */
export function fileActionsLocked(input: { busy: boolean; hold: Hold; pending: boolean }): boolean {
  return input.busy || (input.hold !== "" && input.hold !== "conflict") || input.pending;
}

export function idleMachine(): Machine {
  return {
    busy: false, op: "", hold: "", output: "", line: "",
    conflicted: [], dirty: [], remote: "", branch: "",
  };
}

export function begin(op: OpName): Machine {
  return { ...idleMachine(), busy: true, op, line: "started" };
}

export function noteProgress(machine: Machine, line: string): Machine {
  if (!machine.busy || line.trim() === "") return machine;
  return { ...machine, line };
}

export function succeed(line: string): Machine {
  return { ...idleMachine(), line: line.trim() || "finished" };
}

export function fail(op: OpName | "", output: string): Machine {
  return { ...idleMachine(), hold: "error", op, output };
}

export function pullOutcome(status: string, conflicted: string[], output: string): Machine {
  if (status === "diverged") return { ...idleMachine(), hold: "diverged", output, op: "pull" };
  if (status === "conflict") return { ...idleMachine(), hold: "conflict", conflicted, output, op: "pull" };
  const line = status === "up-to-date" ? "Already up to date" : "Pulled";
  return succeed(line);
}

export function pushOutcome(status: string, remote: string, branch: string, output: string): Machine {
  if (status === "no-upstream") {
    return { ...idleMachine(), hold: "upstream", remote, branch, output, op: "push" };
  }
  return succeed("Pushed");
}

export function switchOutcome(status: string, files: string[], output: string): Machine {
  if (status === "dirty") return { ...idleMachine(), hold: "dirty", dirty: files, output, op: "switch" };
  if (status === "created") return succeed("Branch created");
  return succeed("Switched");
}

/** Operations that may replace files on disk need the draft warning. */
export function confirmsWorktree(op: OpName): boolean {
  return op === "pull" || op === "switch" || op === "abort" || op === "stash" || op === "stash-apply" || op === "stash-pop";
}

/** The operation replaced working-tree bytes, so open drafts need a new inspect baseline.
 *  Creating a branch rewrites files only when the checkout succeeded. */
export function rewroteWorktree(op: OpName, hold: Hold, switched = false): boolean {
  if (hold === "dirty" || hold === "diverged" || hold === "error" || hold === "upstream") return false;
  if (op === "create") return switched;
  return op === "pull" || op === "switch" || op === "abort" || op === "stash" || op === "stash-apply" || op === "stash-pop";
}

export function worktreeWarning(op: OpName): string {
  switch (op) {
    case "pull":
      return "You have unsaved edits. Pull rewrites files on disk. Continue anyway?";
    case "switch":
      return "You have unsaved edits. Switching branches rewrites files on disk. Continue anyway?";
    case "abort":
      return "You have unsaved edits. Aborting the merge rewrites files on disk. Continue anyway?";
    default:
      return "You have unsaved edits. This stash operation rewrites files on disk. Continue anyway?";
  }
}

export function upstreamPrompt(remote: string, branch: string): string {
  if (!remote) {
    return `Branch ${branch || "HEAD"} has no upstream, and no remote is configured. Add a remote in git, then push again. Verity does not ask for or store credentials.`;
  }
  return `Push branch ${branch} and set its upstream to ${remote}/${branch}? Verity will run git push -u. It will not force-push.`;
}

export function aheadBehindLabel(tracking: boolean, ahead: number, behind: number, gone = false): string {
  if (gone) return "upstream gone";
  if (!tracking) return "";
  return `↑${ahead} ↓${behind}`;
}

export function stashOutcome(op: OpName, status: string, conflicted: string[], output: string): Machine {
  if (status === "conflict") return { ...idleMachine(), hold: "conflict", op, conflicted, output };
  if (status === "stashed") return succeed("Changes stashed");
  if (status === "dropped") return succeed("Stash dropped");
  return succeed(op === "stash-pop" ? "Stash popped" : "Stash applied");
}

export function stashConfirmation(action: "apply" | "pop" | "drop", id: string, dirty: boolean): string {
  const warning = dirty && action !== "drop" ? " You have unsaved edits; files on disk may change." : "";
  if (action === "pop") return `Apply stash ${id.slice(0, 12)} and drop it after a successful apply? Conflicts keep the stash.${warning}`;
  if (action === "drop") return `Permanently drop stash ${id.slice(0, 12)}?`;
  return dirty ? worktreeWarning("stash-apply") : "";
}
