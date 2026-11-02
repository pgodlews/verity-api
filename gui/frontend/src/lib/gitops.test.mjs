// Run with: node --test src/lib/gitops.test.mjs
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import ts from "typescript";

const directory = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

function loadTypeScript(name) {
  const source = readFileSync(join(directory, name), "utf8");
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 },
  });
  const module = { exports: {} };
  new Function("module", "exports", "require", outputText)(module, module.exports, require);
  return module.exports;
}

const {
  defaultSelectedPaths,
  reconcileSelection,
  splitFiles,
  commitEnablement,
  actionEnablement,
  begin,
  noteProgress,
  succeed,
  fail,
  pullOutcome,
  pushOutcome,
  switchOutcome,
  confirmsWorktree,
  rewroteWorktree,
  toolbarLocked,
  worktreeWarning,
  upstreamPrompt,
  aheadBehindLabel,
} = loadTypeScript("gitops.ts");

const files = [
  { path: "project.yaml", projectFile: true },
  { path: "parts/api.yaml", projectFile: true },
  { path: "notes.txt", projectFile: false },
];

test("project files are selected by default and other files are not", () => {
  assert.deepEqual(defaultSelectedPaths(files), ["project.yaml", "parts/api.yaml"]);
  assert.deepEqual(defaultSelectedPaths([]), []);
  const split = splitFiles(files);
  assert.deepEqual(split.project.map((file) => file.path), ["project.yaml", "parts/api.yaml"]);
  assert.deepEqual(split.other.map((file) => file.path), ["notes.txt"]);
});

test("selection keeps the user's choices and drops paths that disappeared", () => {
  assert.deepEqual(reconcileSelection(null, files), ["project.yaml", "parts/api.yaml"]);
  assert.deepEqual(reconcileSelection(["notes.txt", "gone.txt"], files), ["notes.txt"]);
  assert.deepEqual(reconcileSelection([], files), []);
});

test("commit stays disabled until a file and a message are both present", () => {
  const cases = [
    { repo: false, selected: 1, message: "hi", busy: false, enabled: false, reason: "Not in a git repository" },
    { repo: true, selected: 1, message: "hi", busy: true, enabled: false, reason: "Wait for the git operation to finish" },
    { repo: true, selected: 0, message: "hi", busy: false, enabled: false, reason: "Select at least one file" },
    { repo: true, selected: 2, message: "  ", busy: false, enabled: false, reason: "Enter a commit message" },
    { repo: true, selected: 1, message: "ship it", busy: false, enabled: true, reason: "Commit the selected files" },
  ];
  for (const tc of cases) {
    assert.deepEqual(commitEnablement(tc), { enabled: tc.enabled, reason: tc.reason });
  }
  assert.equal(actionEnablement({ repo: true, busy: true, action: "Fetch" }).enabled, false);
  assert.equal(actionEnablement({ repo: true, busy: false, action: "Fetch" }).reason, "Fetch");
  assert.equal(actionEnablement({ repo: true, busy: false, held: true, action: "Fetch" }).enabled, false);
  assert.equal(actionEnablement({ repo: true, busy: false, detached: true, action: "Push" }).enabled, false);
  assert.equal(actionEnablement({ repo: true, busy: false, detached: false, action: "Push" }).enabled, true);
  assert.equal(commitEnablement({ repo: true, selected: 1, message: "ship it", busy: false, held: true }).enabled, false);
  assert.equal(toolbarLocked({ busy: false, hold: "diverged", pending: false }), true);
  assert.equal(toolbarLocked({ busy: false, hold: "", pending: true }), true);
  assert.equal(toolbarLocked({ busy: false, hold: "", pending: false }), false);
});

test("operation state moves through diverged, conflict, upstream, and dirty", () => {
  const running = noteProgress(begin("pull"), "counting");
  assert.equal(running.busy, true);
  assert.equal(running.line, "counting");
  assert.equal(noteProgress(running, "  ").line, "counting");

  const diverged = pullOutcome("diverged", [], "not possible to fast-forward");
  assert.equal(diverged.busy, false);
  assert.equal(diverged.hold, "diverged");
  assert.match(diverged.output, /fast-forward/);

  const conflict = pullOutcome("conflict", ["project.yaml"], "CONFLICT");
  assert.equal(conflict.hold, "conflict");
  assert.deepEqual(conflict.conflicted, ["project.yaml"]);

  assert.equal(pullOutcome("updated", [], "").line, "Pulled");
  assert.equal(pullOutcome("up-to-date", [], "").line, "Already up to date");

  const upstream = pushOutcome("no-upstream", "origin", "main", "");
  assert.equal(upstream.hold, "upstream");
  assert.equal(upstream.remote, "origin");
  assert.equal(upstream.branch, "main");
  assert.equal(pushOutcome("pushed", "origin", "main", "").line, "Pushed");

  const dirty = switchOutcome("dirty", ["project.yaml"], "");
  assert.equal(dirty.hold, "dirty");
  assert.deepEqual(dirty.dirty, ["project.yaml"]);
  assert.equal(switchOutcome("switched", [], "").line, "Switched");
  assert.equal(switchOutcome("created", [], "").line, "Branch created");

  const refused = fail("commit", "hook-says-no");
  assert.equal(refused.hold, "error");
  assert.equal(refused.output, "hook-says-no");
  assert.equal(succeed("Committed").hold, "");
  assert.equal(succeed("Committed").line, "Committed");
});

test("working-tree operations ask before discarding unsaved edits", () => {
  assert.equal(confirmsWorktree("pull"), true);
  assert.equal(confirmsWorktree("switch"), true);
  assert.equal(confirmsWorktree("abort"), true);
  assert.equal(confirmsWorktree("fetch"), false);
  assert.equal(confirmsWorktree("push"), false);
  assert.equal(confirmsWorktree("commit"), false);
  assert.equal(rewroteWorktree("pull", ""), true);
  assert.equal(rewroteWorktree("pull", "conflict"), true);
  assert.equal(rewroteWorktree("pull", "diverged"), false);
  assert.equal(rewroteWorktree("switch", ""), true);
  assert.equal(rewroteWorktree("switch", "dirty"), false);
  assert.equal(rewroteWorktree("abort", ""), true);
  assert.equal(rewroteWorktree("commit", ""), false);
  assert.equal(rewroteWorktree("fetch", ""), false);
  assert.equal(rewroteWorktree("create", ""), false);
  assert.equal(rewroteWorktree("create", "", true), true);
  assert.equal(rewroteWorktree("create", "dirty", true), false);
  assert.equal(rewroteWorktree("push", "error"), false);
  assert.match(worktreeWarning("pull"), /unsaved edits/);
  assert.match(worktreeWarning("switch"), /Switching branches/);
  assert.match(worktreeWarning("abort"), /Aborting the merge/);
  assert.match(upstreamPrompt("origin", "main"), /origin\/main/);
  assert.match(upstreamPrompt("origin", "main"), /force-push/);
  assert.match(upstreamPrompt("", "main"), /no remote/);
  assert.equal(aheadBehindLabel(true, 2, 1), "↑2 ↓1");
  assert.equal(aheadBehindLabel(false, 2, 1), "");
  assert.equal(aheadBehindLabel(true, 0, 0, true), "upstream gone");
});

test("stash operations guard drafts, confirm destructive actions, and keep conflict state", () => {
  const { stashOutcome, stashConfirmation } = loadTypeScript("gitops.ts");
  for (const op of ["stash", "stash-apply", "stash-pop"]) {
    assert.equal(confirmsWorktree(op), true);
    assert.equal(rewroteWorktree(op, ""), true);
    assert.equal(rewroteWorktree(op, "conflict"), true);
    assert.equal(rewroteWorktree(op, "error"), false);
    assert.match(worktreeWarning(op), /unsaved edits/);
  }
  for (const op of ["stash-drop", "stash-show"]) {
    assert.equal(confirmsWorktree(op), false);
    assert.equal(rewroteWorktree(op, ""), false);
  }
  const id = "a".repeat(40);
  assert.equal(stashConfirmation("apply", id, false), "");
  assert.match(stashConfirmation("apply", id, true), /unsaved edits/);
  assert.match(stashConfirmation("pop", id, false), /drop it after a successful apply/);
  assert.match(stashConfirmation("pop", id, true), /unsaved edits/);
  assert.match(stashConfirmation("drop", id, false), /Permanently drop/);
  assert.doesNotMatch(stashConfirmation("drop", id, true), /unsaved edits/);
  const conflict = stashOutcome("stash-pop", "conflict", ["project.yaml"], "The stash was kept.");
  assert.equal(conflict.hold, "conflict");
  assert.equal(conflict.op, "stash-pop");
  assert.deepEqual(conflict.conflicted, ["project.yaml"]);
  assert.equal(conflict.output, "The stash was kept.");
  assert.equal(stashOutcome("stash", "stashed", [], "").line, "Changes stashed");
  assert.equal(stashOutcome("stash-pop", "applied", [], "").line, "Stash popped");
  assert.equal(stashOutcome("stash-drop", "dropped", [], "").line, "Stash dropped");
  assert.equal(toolbarLocked({ busy: false, hold: "", pending: true }), true);
});

// Load the real ?mock binding module, with its local event buses stubbed.
// Keep fixture and helper imports real so the binding contract stays covered.
function loadMockBindings() {
  const cache = new Map();
  function load(file) {
    if (cache.has(file)) return cache.get(file).exports;
    if (file.endsWith(".json")) return JSON.parse(readFileSync(file, "utf8"));
    const module = { exports: {} };
    cache.set(file, module);
    const { outputText } = ts.transpileModule(readFileSync(file, "utf8"), {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, esModuleInterop: true },
    });
    const localRequire = (name) => {
      if (name.endsWith("gitBus")) return { emitGitProgress() {} };
      if (name.endsWith("runBus")) return { emitLocalRunEvent() {} };
      if (!name.startsWith(".")) return require(name);
      return load(join(dirname(file), name.endsWith(".json") ? name : `${name}.ts`));
    };
    new Function("module", "exports", "require", outputText)(module, module.exports, localRequire);
    return module.exports;
  }
  return load(join(directory, "../dev/mock.ts")).mockBindings;
}

test("?mock stashes honour selection, untracked option, shifted IDs, and stale IDs", async () => {
  const mock = loadMockBindings();
  const path = "project.yaml";
  assert.deepEqual(await mock.GitStashes(path), []);
  assert.equal((await mock.GitStash({ path, paths: ["project.yaml"], message: "token=hidden", includeUntracked: false })).status, "stashed");
  const first = (await mock.GitStashes(path))[0];
  assert.equal(first.index, 0);
  assert.equal(first.branch, "main");
  assert.doesNotMatch(first.message, /hidden/);
  assert.match(first.message, /REDACTED/);
  assert.deepEqual((await mock.GitStatus(path)).files.map((file) => file.path), ["notes.txt"]);
  await assert.rejects(mock.GitStash({ path, includeUntracked: false }), /nothing to stash/);
  await assert.rejects(mock.GitStash({ path, paths: ["missing"], includeUntracked: true }), /nothing to stash/);
  await mock.GitStash({ path, paths: ["notes.txt"], includeUntracked: true });
  const second = (await mock.GitStashes(path))[0];
  assert.notEqual(first.id, second.id);
  assert.deepEqual((await mock.GitStashes(path)).map((stash) => stash.index), [0, 1]);
  assert.equal((await mock.GitStashChanges({ path, id: first.id })).steps.length, 2);
  assert.equal((await mock.GitStashChanges({ path, id: second.id })).steps.length, 0);
  assert.equal((await mock.GitStashAction({ path, id: first.id, action: "apply" })).status, "applied");
  assert.equal((await mock.GitStashes(path)).length, 2);
  assert.equal((await mock.GitStashAction({ path, id: second.id, action: "pop" })).status, "applied");
  assert.deepEqual((await mock.GitStashes(path)).map((stash) => stash.id), [first.id]);
  await mock.GitStashAction({ path, id: first.id, action: "drop" });
  await assert.rejects(mock.GitStashAction({ path, id: first.id, action: "apply" }), /no longer in the list/);
  await assert.rejects(mock.GitStashChanges({ path, id: first.id }), /no longer in the list/);
  await assert.rejects(mock.GitStashAction({ path, id: "stash@{0}", action: "drop" }), /full stash object id/);
});

test("?mock conflicting pop retains the stash for the editor flow", async () => {
  const mock = loadMockBindings();
  const path = "project.yaml";
  await mock.GitStash({ path, paths: ["project.yaml"], includeUntracked: false });
  const { id } = (await mock.GitStashes(path))[0];
  await mock.GitStashAction({ path, id, action: "apply" });
  const conflict = await mock.GitStashAction({ path, id, action: "pop" });
  assert.equal(conflict.status, "conflict");
  assert.deepEqual(conflict.conflicted, ["project.yaml"]);
  assert.equal((await mock.GitStashes(path))[0].id, id);
  assert.match(conflict.output, /stash was kept/);
  assert.equal((await mock.GitStatus(path)).files.find((file) => file.path === "project.yaml").state, "conflicted");
});

test("stash conflicts allow staging resolved files while confirmations still freeze file actions", () => {
  const { fileActionsLocked } = loadTypeScript("gitops.ts");
  assert.equal(fileActionsLocked({ busy: false, hold: "conflict", pending: false }), false);
  assert.equal(fileActionsLocked({ busy: false, hold: "conflict", pending: true }), true);
  assert.equal(fileActionsLocked({ busy: true, hold: "conflict", pending: false }), true);
  assert.equal(fileActionsLocked({ busy: false, hold: "error", pending: false }), true);
  assert.equal(fileActionsLocked({ busy: false, hold: "upstream", pending: false }), true);
  assert.equal(toolbarLocked({ busy: false, hold: "conflict", pending: false }), true);
  assert.equal(commitEnablement({ repo: true, selected: 1, message: "Resolve", busy: false,
    held: fileActionsLocked({ busy: false, hold: "conflict", pending: false }) }).enabled, true);
});

test("?mock untracked restore errors keep the stash without merge state", async () => {
  const mock = loadMockBindings();
  const path = "project.yaml";
  await mock.GitStash({ path, paths: ["notes.txt"], includeUntracked: true });
  const { id } = (await mock.GitStashes(path))[0];
  await mock.GitStashAction({ path, id, action: "apply" });
  await assert.rejects(mock.GitStashAction({ path, id, action: "pop" }), /could not restore untracked files/);
  assert.equal((await mock.GitStashes(path))[0].id, id);
  assert.equal((await mock.GitStatus(path)).merging, false);
});
