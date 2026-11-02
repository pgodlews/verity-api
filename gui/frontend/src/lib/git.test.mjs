// Run with: node --test src/lib/git.test.mjs
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
  planDiskReload,
  resolveTab,
  findStepByIdentity,
  stepMarker,
  groupStepChanges,
  lineDiff,
  branchLabel,
  changedCount,
  partLabel,
  samePath,
  fileBaseline,
  fileHashOf,
  keepEdits,
  stepBaseline,
  nextGeneration,
  isCurrentGeneration,
  isConflictError,
  changedFilePaths,
  reportedChangeCount,
} = loadTypeScript("git.ts");

function tab(over) {
  return {
    id: "t",
    suite: "Smoke",
    caseName: "widgets",
    index: 0,
    name: "list",
    occurrence: 0,
    siblings: 2,
    stepHash: "",
    dirty: false,
    file: "/proj/api.yaml",
    ...over,
  };
}

const project = {
  suites: [{
    name: "Smoke",
    cases: [{
      name: "widgets",
      steps: [{ name: "list" }, { name: "list" }, { name: "create" }],
    }],
  }],
};

test("step markers use M for modified and moved, A for added, and skip removed", () => {
  const steps = [
    { suite: "Smoke", case: "widgets", name: "list", index: 0, state: "modified" },
    { suite: "Smoke", case: "widgets", name: "create", index: 2, state: "added" },
    { suite: "Smoke", case: "widgets", name: "login", index: 1, state: "moved" },
    { suite: "Smoke", case: "widgets", name: "gone", index: 3, state: "removed" },
  ];
  assert.equal(stepMarker(steps, "Smoke", "widgets", 0, "list"), "M");
  assert.equal(stepMarker(steps, "Smoke", "widgets", 2, "create"), "A");
  assert.equal(stepMarker(steps, "Smoke", "widgets", 1, "login"), "M");
  assert.equal(stepMarker(steps, "Smoke", "widgets", 3, "gone"), null);
  assert.equal(stepMarker(steps, "Smoke", "widgets", 0, "other"), null);
  assert.equal(stepMarker(undefined, "Smoke", "widgets", 0, "list"), null);
});

test("changes group by suite and case in first-seen order", () => {
  const groups = groupStepChanges([
    { suite: "Smoke", case: "widgets", name: "list", index: 0, state: "modified", parts: [] },
    { suite: "Auth", case: "login", name: "sign in", index: 0, state: "added", parts: [] },
    { suite: "Smoke", case: "other", name: "gone", index: 0, state: "removed", parts: [] },
    { suite: "Smoke", case: "widgets", name: "create", index: 1, state: "modified", parts: [] },
  ]);
  assert.deepEqual(groups.map((group) => group.suite), ["Smoke", "Auth"]);
  assert.deepEqual(groups[0].cases.map((item) => item.name), ["widgets", "other"]);
  assert.deepEqual(groups[0].cases[0].steps.map((step) => step.name), ["list", "create"]);
  assert.deepEqual(groupStepChanges([]), []);
  assert.deepEqual(groupStepChanges(undefined), []);
});

test("line diff is line-level and ignores a single trailing newline", () => {
  assert.deepEqual(lineDiff("a\nb\n", "a\nb"), [
    { kind: "same", text: "a" },
    { kind: "same", text: "b" },
  ]);
  assert.deepEqual(lineDiff("a\nb\nc", "a\nB\nc"), [
    { kind: "same", text: "a" },
    { kind: "del", text: "b" },
    { kind: "add", text: "B" },
    { kind: "same", text: "c" },
  ]);
  assert.deepEqual(lineDiff("a\nc", "a\nb\nc"), [
    { kind: "same", text: "a" },
    { kind: "add", text: "b" },
    { kind: "same", text: "c" },
  ]);
  assert.deepEqual(lineDiff("a\nb", "a"), [
    { kind: "same", text: "a" },
    { kind: "del", text: "b" },
  ]);
  assert.deepEqual(lineDiff("", ""), []);
});

test("a pull that changes the step banners a dirty draft opened before it", () => {
  const dirty = tab({ dirty: true, siblings: 1, occurrence: 0, stepHash: "old" });
  const one = { suites: [{ name: "Smoke", cases: [{ name: "widgets", steps: [{ name: "list" }] }] }] };
  const files = [{ suite: "Smoke", case: "widgets", index: 0, file: "/proj/api.yaml", hash: "new" }];
  const plan = planDiskReload([dirty], one, [], files)[0];
  assert.equal(plan.kind, "keep");
  assert.equal(plan.banner, true);
  assert.equal(plan.stepHash, "old");
  const quiet = planDiskReload([dirty], one, null, files)[0];
  assert.equal(quiet.kind, "keep");
  assert.equal(quiet.banner, false);
  assert.equal(quiet.stepHash, "new");
});

test("an in-place edit of a duplicate name stays on that occurrence", () => {
  const one = { suites: [{ name: "Smoke", cases: [{ name: "widgets", steps: [{ name: "list" }, { name: "list" }] }] }] };
  const files = [
    { suite: "Smoke", case: "widgets", index: 0, file: "/proj/api.yaml", hash: "edited" },
    { suite: "Smoke", case: "widgets", index: 1, file: "/proj/api.yaml", hash: "old" },
  ];
  const bound = { suite: "Smoke", caseName: "widgets", name: "list", occurrence: 0, siblings: 2, stepHash: "old" };
  const resolved = resolveTab(one, bound, files);
  assert.equal(resolved.at.index, 0);
  assert.equal(resolved.changed, true);
  assert.equal(resolved.stepHash, "edited");
});

test("file hash diff and reported changes include project meta", () => {
  assert.deepEqual(changedFilePaths(
    { fileHashes: [{ path: "/p/a.yaml", hash: "1" }, { path: "/p/b.yaml", hash: "2" }] },
    { fileHashes: [{ path: "/p/a.yaml", hash: "9" }] },
  ), ["/p/a.yaml", "/p/b.yaml"]);
  assert.deepEqual(changedFilePaths({ fileHashes: [{ path: "/p/a.yaml", hash: "1" }] }, { fileHashes: [{ path: "/p/a.yaml", hash: "1" }] }), []);
  assert.equal(reportedChangeCount(null), 0);
  assert.equal(reportedChangeCount({ steps: [], meta: [{ part: "project", before: "", after: "modified" }] }), 1);
  assert.equal(reportedChangeCount({ otherFiles: 2 }), 2);
});

test("a dirty draft banners only when its own file changed", () => {
  const dirty = tab({ dirty: true });
  const hit = planDiskReload([dirty], project, ["/proj/api.yaml"])[0];
  assert.equal(hit.kind, "keep");
  assert.equal(hit.banner, true);
  assert.deepEqual(hit.at, { suite: "Smoke", caseName: "widgets", index: 0 });
  const other = planDiskReload([dirty], project, ["/proj/other.yaml"])[0];
  assert.equal(other.kind, "keep");
  assert.equal(other.banner, false);
  assert.equal(samePath("/proj/api.yaml/", "/proj/api.yaml"), true);
  assert.equal(samePath("C:/proj/api.yaml", "C:\\proj\\api.yaml"), true);
});

test("reload is per tab: a conflict on A does not banner B", () => {
  const a = tab({ id: "a", dirty: true, file: "/proj/a.yaml", siblings: 1, occurrence: 0 });
  const b = tab({ id: "b", dirty: true, file: "/proj/b.yaml", name: "create", index: 2, occurrence: 0, siblings: 1 });
  const oneCase = {
    suites: [{ name: "Smoke", cases: [{ name: "widgets", steps: [{ name: "list" }, { name: "create" }] }] }],
  };
  const plans = planDiskReload([a, b], oneCase, ["/proj/a.yaml"]);
  assert.equal(plans[0].banner, true);
  assert.equal(plans[1].banner, false);
  assert.equal(plans[1].kind, "keep");
});

test("duplicate steps do not converge onto one save target", () => {
  const remaining = {
    suites: [{ name: "Smoke", cases: [{ name: "widgets", steps: [{ name: "list", }] }] }],
  };
  const files = [{ suite: "Smoke", case: "widgets", index: 0, file: "/proj/api.yaml", hash: "survives" }];
  const removed = tab({ id: "a", occurrence: 0, siblings: 2, stepHash: "removed", dirty: true });
  const survivor = tab({ id: "b", index: 1, occurrence: 1, siblings: 2, stepHash: "survives", dirty: true });
  const plans = planDiskReload([removed, survivor], remaining, ["/proj/api.yaml"], files);
  assert.equal(plans[0].kind, "detach");
  assert.equal(plans[1].kind, "keep");
  assert.deepEqual(plans[1].at, { suite: "Smoke", caseName: "widgets", index: 0 });
  const targets = plans.filter((plan) => plan.at).map((plan) => plan.at.index);
  assert.equal(new Set(targets).size, targets.length);
});

test("a clean request reloads, follows a renamed index, or closes when the step is gone", () => {
  const clean = tab({ dirty: false, siblings: 2 });
  assert.equal(planDiskReload([clean], project, ["/proj/api.yaml"])[0].kind, "reload");
  const moved = tab({ dirty: false, index: 5, name: "create", occurrence: 0, siblings: 1 });
  assert.deepEqual(planDiskReload([moved], project, ["/proj/api.yaml"])[0].at, {
    suite: "Smoke", caseName: "widgets", index: 2,
  });
  const gone = tab({ dirty: false, name: "missing", occurrence: 0, siblings: 1 });
  assert.deepEqual(planDiskReload([gone], project, ["/proj/api.yaml"]), [{ kind: "close", name: "missing" }]);
  assert.equal(planDiskReload([gone], project, null)[0].kind, "keep");
  assert.deepEqual(findStepByIdentity(project, "Smoke", "widgets", "list", 1), {
    suite: "Smoke", caseName: "widgets", index: 1,
  });
  assert.equal(findStepByIdentity(project, "Smoke", "widgets", "list", 2), null);
});

test("keep my edits adopts the step hash, or the file hash when the step has none", () => {
  const at = { suite: "Smoke", caseName: "widgets", index: 0 };
  const cases = [
    {
      name: "hash present",
      stepFiles: [{ suite: "Smoke", case: "widgets", index: 0, file: "/proj/api.yaml", hash: "step-1" }],
      stepHash: "step-1",
      fileBaseline: "",
    },
    {
      name: "hash missing",
      stepFiles: [{ suite: "Smoke", case: "widgets", index: 0, file: "/proj/api.yaml" }],
      stepHash: "",
      fileBaseline: "file-1",
    },
  ];
  for (const item of cases) {
    const decision = keepEdits({
      path: "/proj/api.yaml",
      project,
      stepFiles: item.stepFiles,
      fileHashes: [{ path: "/proj/api.yaml", hash: "file-1" }],
    }, at);
    assert.equal(decision.stepHash, item.stepHash, item.name);
    assert.equal(decision.fileBaseline, item.fileBaseline, item.name);
    assert.equal(decision.file, "/proj/api.yaml", item.name);
    assert.equal(decision.occurrence, 0, item.name);
    assert.equal(decision.siblings, 2, item.name);
  }
  const windows = keepEdits({
    path: "C:/proj/api.yaml",
    project,
    stepFiles: [{ suite: "Smoke", case: "widgets", index: 0, file: "C:/proj/api.yaml" }],
    fileHashes: [{ path: "C:\\proj\\api.yaml", hash: "file-win" }],
  }, at);
  assert.equal(windows.stepHash, "");
  assert.equal(windows.fileBaseline, "file-win");
  assert.equal(fileHashOf({ fileHashes: [] }, "/proj/api.yaml"), "");
});

test("generation tokens and conflict errors", () => {
  let current = 0;
  const first = (current = nextGeneration(current));
  const second = (current = nextGeneration(current));
  assert.equal(isCurrentGeneration(first, current), false);
  assert.equal(isCurrentGeneration(second, current), true);
  assert.equal(isConflictError(new Error("conflict: this request changed on disk")), true);
  assert.equal(isConflictError({ code: "conflict", message: "no" }), true);
  assert.equal(isConflictError(new Error("validation_error: no")), false);
  assert.deepEqual(fileBaseline({ fileHashes: [{ path: "/p/a.yaml", hash: "abc" }] }), { "/p/a.yaml": "abc" });
  assert.equal(fileBaseline({}), undefined);
  assert.equal(stepBaseline({ stepFiles: [{ suite: "Smoke", case: "widgets", index: 0, hash: "h" }] }, "Smoke", "widgets", 0), "h");
});

test("branch label and changed count", () => {
  assert.equal(branchLabel({ branch: "main" }), "main");
  assert.equal(branchLabel({ detached: true, short: "abc1234" }), "detached abc1234");
  assert.equal(branchLabel({ detached: true }), "detached");
  assert.equal(changedCount({ files: [{}, {}] }), 2);
  assert.equal(changedCount({}), 0);
  assert.equal(partLabel({ part: "headers", key: "Accept" }), "headers[Accept]");
  assert.equal(partLabel({ part: "body" }), "body");
});
