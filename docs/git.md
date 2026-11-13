# Git-friendly projects

Verity projects are designed to be reviewed in Git. Keep project YAML/JSON,
included fragments, feeds, upload fixtures, `.env.example`, and the project
JSON Schema references in version control. Do not commit real secret values,
generated reports, or machine-local state.

## Initialize a directory

```bash
verity init
# or initialize another directory
verity init path/to/project-dir
```

`init` creates or updates `.gitignore` with this marked block, leaving entries
outside the block untouched:

```gitignore
# >>> verity
.env
.env.*
!.env.example
reports/
.verity/local/
# <<< verity
```

Running `init` again replaces only that block. If no `project.yaml`,
`project.yml`, or `project.json` exists, it also creates a minimal valid
`project.yaml`. It writes `.env.example` with every `${env:NAME}` reference,
an empty value, and a comment describing where the name is used. An existing
`.env.example` is left alone unless `--force` is supplied.

Imports update the same `.gitignore` block in the output directory. They do not
create a starter project or overwrite `.env.example`.

## Environment values

Keep references in project files:

```yaml
variables:
  API_TOKEN: "${env:API_TOKEN}"
```

Commit `.env.example` with empty values as documentation. Keep actual values in
an ignored `.env` file or in the process environment:

```bash
verity run project.yaml --env-file .env
```

Later `--env-file` files override earlier ones. Existing process variables win
unless `--env-file-override` is set. Never copy real values back into
`.env.example`, a project file, or a report.

## Formatting

```bash
# Format a project and every included fragment, recursively.
verity fmt project.yaml

# Format several independent roots.
verity fmt project.yaml other/project.json

# CI/check mode: write nothing, list files that would change, exit 1 if any do.
verity fmt --check project.yaml
```

With no path, `fmt` uses `project.yaml` in the current directory. YAML and JSON
use two-space indentation, retain authored key order, and end with a newline.
YAML comments and existing document markers are preserved. Feeds and upload
files are not formatted. Formatting is idempotent and does not change loaded
project values. Saving steps or environments from the GUI uses the same
formatter, so those saves also normalize files to two spaces.

## Pre-commit hooks

Install from within a Git repository, naming the project to store in the hook:

```bash
verity hooks install project.yaml
# Also reject formatting drift.
verity hooks install --fmt project.yaml
```

The hook runs `verity scan-secrets --staged project.yaml` from the repository
root; `--fmt` adds `verity fmt --check --staged project.yaml`. Both read
what the commit records (the git index), so `git add -p` is judged by the
staged content. A scan finding, a staged `.env` file, a value in a staged
`.env.example`, or formatting drift blocks the commit. Symlinked project
files are refused in staged mode rather than scanned as link text. `verity` must be on `PATH` when Git runs
the hook. Git must also be installed when installing or uninstalling the hook.

Directory symlink aliases are resolved consistently for project recognition,
selected-file scans, dirty-file guards, and hook project paths. A selected commit
stops if its project file list cannot be read, so repair a missing or invalid
fragment before retrying. Tracked symlink files retain their Git entry identity
and staged project symlinks are refused.

Installation uses `git rev-parse --git-path hooks`, so `core.hooksPath` and
worktrees are honoured. It refuses to replace an unrelated `pre-commit` hook.
Use `--force` to move that hook to `pre-commit.verity-backup` first. Reinstalling
a Verity hook is idempotent. One hook can check several projects in the same
repository: installing it for another project adds that project (with its own
`--fmt` choice) and keeps the ones already in the hook. To regenerate a hook installed by the pre-release
CLI, re-run `verity hooks install [--fmt] project.yaml` so it calls `verity`.

```bash
verity hooks uninstall project.yaml
```

Uninstall removes that project from the hook and deletes the hook when no
project is left. It never touches a hook without Verity's marker. When a hook blocks a
commit, its final output line shows the exceptional bypass:
`git commit --no-verify`. Fix and commit normally whenever possible; the bypass
skips the secret and formatting checks.

## Desktop source control

The desktop app uses the installed `git` CLI (argument arrays, `--` before
paths). It does not link a Git library.

When `git` is missing, or the project directory is not inside a repository,
the status bar says the project is not in a git repository and Source control
stays empty.

When the project is in a repository the status bar shows the branch name, or
`detached` plus the short commit id, how many paths differ from HEAD, and
ahead/behind counts taken from the local upstream ref. Those counts change
when you fetch or pull. The collection tree marks a request that was added
(`A`) or modified (`M`) since the last commit. Removed requests are listed
only in Source control.

Changes compares the working-tree project with `HEAD` by loading both sides
and matching requests on suite, case, and step name (the index breaks a tie
when names repeat). A rename of the root project file is followed: HEAD is
read from the path git reports as the rename source. Each modified request
lists the parts that differ — method, URL, one row per header or query key,
body, auth, assertions, transfers, variables, timeout, retry, skip, and a
within-case order change with the old and new index — with the value before
and after. A body change is also shown as a line-level text diff. A step that
keeps its name but changes method, URL, body, or protocol is removed and
added; a unique match on that identity in another suite or case is a move.
Crossed URLs pair with their own partners rather than by list order.
Environments
(including their auth), auth definitions, suite and case fields (variables,
auth, feed, continue-on-error, parallelism), setup and teardown steps, and
the project name, description, and schema version are grouped the same way.
When the two loaded projects differ and none of those parts were reported,
Changes shows one modified project row rather than an empty list. A
comment-only edit that still decodes to the same project is reported as
the file changed. A directory move that git reports as a delete plus an add
is still compared with the old blob when the file name is unique. A value
that the secret scan would flag — a URL with userinfo or a credential query,
a form, XML, or JSON body (including a number or a duplicate key), or a
nested auth setting — is shown as `***REDACTED***` for the whole part,
including one already committed before the scan existed. Paths in the
repository that are not the project file or a fragment are counted as other
changed files; their diff text is not shown.

Saving a request or an environment sends the hash of what was loaded (the
step as canonical JSON, or each environment file's bytes). Those hashes are
not the file hashes returned by a save. If the loaded step changed on disk,
the save writes nothing and returns a `conflict` error. The draft stays
open and the same banner offers **Reload from disk** or **Keep my edits**.
Keep re-reads the file and adopts that step hash; until then a save still
sends the hash from before the rewrite, so a pull or branch switch cannot
be overwritten by the old draft. When the step has no hash, Keep still
clears the banner and the next save sends that file's sha256 as
`fileBaseline`. `SaveStep` compares `fileBaseline` to the whole file only
when the step baseline is empty; a mismatch is a conflict and writes
nothing. The desktop editor requires a step hash or a file hash.
`workspace.SaveStep` and `SaveEnvironment` still accept an empty baseline
for CLI and dev-bridge callers that never recorded a hash. Saves and git writes
in one process take a per-repository lock so two of Verity's own
compare-and-rename operations cannot overlap. Two tabs that can no longer
be told apart (duplicate names whose contents no longer match a single
step) stay separate and are not saved onto one request. An in-place edit
of one duplicate stays on that occurrence.

The app watches the project files `model.Load` records, plus the worktree's
`HEAD`, index, current branch ref, upstream ref, `packed-refs`, `refs/stash`,
and `logs/refs/stash` (each resolved with `git rev-parse --git-path`, so a linked worktree is covered).
After a short debounce it reloads the project. If a reload cannot load the
project, the previous file set stays watched, together with the root, until
a later load succeeds. Every path a save writes is marked with the hash of
those bytes before the write; an event whose bytes still match is not an
outside edit, and an event with different bytes is delivered even when it
arrives in the same second. Git commands pass `--no-pager`, force
`core.fsmonitor` off, and set `LC_ALL=C.UTF-8` when that locale exists
(`C` otherwise) so non-ASCII paths are not recoded. Path lists use `-z`,
and `core.quotepath` stays false. Read commands also set
`GIT_NO_LAZY_FETCH=1` and override `filter.<name>.clean`, `smudge`, and
`process` for filters defined in the repository (`--local` and
`--worktree`). The driver name is everything between `filter.` and the
last dot. A name that cannot be passed safely fails the command instead
of leaving the filter enabled. Global and system filters, including a
git-lfs you installed yourself, still run, so an LFS file is not reported
as modified just because its clean filter was blanked. A clean filter
checked into the repository does not run while status hashes a dirty file.
`git add` leaves filters in place so the staged blob is the one your
filters would record. The commit then records those index blobs, not a
second hash of the worktree. Pull, switch, and merge do not smudge, so an
LFS pointer stays as a pointer until something outside that checkout
smudges it. Blob reads use `git cat-file` on the
object id from `ls-tree` with a `:(literal)` pathspec. Paths passed to
add, restore, and `ls-files` use the same prefix. `GIT_LITERAL_PATHSPECS`
is not set. If the open request has unsaved edits and its file changed on
disk, the draft is kept and a
banner offers **Reload from disk** or **Keep my edits**. That banner belongs
to the tab whose file changed. A clean request reloads in place when the
step is still there (same suite, case, and name, or the same content when
duplicate names shift); otherwise the tab closes.

Source control lists every changed path. Project files and fragments start
checked; other files in the repo are collapsed and unchecked. **Commit**
stages exactly the checked paths (`git add -- :(literal)…`, never
`git add -A`) and records only those scanned blobs. The commit is a
temporary index built from `HEAD` plus those index entries, so a worktree
edit between the scan and the commit cannot replace the blob. Other index
entries stay staged and are not part of the commit. The scan reads the
blobs that commit will record, not the rest of the index. A dotenv name
on either side of a staged rename that touches the selection is refused.
`.env.example` is still scanned for values. If the scan refuses, a hook
fails, or you cancel, the index entries from before that stage are put
back, including a different blob of the same path and unmerged stages.
The restore keeps going after cancel and retries briefly while git is
still releasing `index.lock`. Hooks still run. The commit message is
required. Verity does not pass `--no-verify`.
Hook output is shown when a hook refuses the commit. The author name and
email come from git config. If they are missing, the error tells you how
to set them; Verity does not set them. `commit.gpgsign` can wait on
pinentry; that prompt is git's, and a cancelled commit is given a short
grace period and then killed.

**Fetch** updates only `refs/remotes/<remote>/<upstream>` for the current
branch (`--no-tags`, `--refmap=`, and `-c fetch.followTags=false`). The
empty refmap ignores `remote.<name>.fetch`, so a configured
`+refs/heads/*:refs/heads/*` cannot move local branches. **Pull** is
fast-forward only, with `--no-edit`, `--refmap=`, `GIT_MERGE_AUTOEDIT=no`,
and the same follow-tags config, so a merge does not open an editor, does
not follow tags, and stores the upstream in its remote-tracking ref. `git fetch` and `git pull` on some Git releases do not accept
`--no-follow-tags` (push does, and push passes it). The config is that
switch. A fast-forward pull is **diverged** only when git refuses with
"Not possible to fast-forward". A network, authentication, or "local
changes would be overwritten" error stays an error. If the branches have
diverged that way, the screen offers **Pull with merge**. A conflict
is reported only when this pull created `MERGE_HEAD`. A merge that was
already open is an error that still lists the unmerged files. **Push** is
`git push --no-tags --no-follow-tags <remote> refs/heads/<local>:refs/heads/<upstream>`.
It never force-pushes, never follows `remote.<name>.push`, and never
pushes tags. A branch name that starts with `+` or `-` is rejected. A
branch with no upstream, including one whose upstream ref is gone, is not
pushed or fetched until you confirm set-upstream. The dialog names that
remote and branch. The status bar says "upstream gone" instead of `↑0 ↓0`.

The branch picker switches branches (`git switch`, Git 2.23 or newer; there
is no `checkout` fallback). If project files have uncommitted changes, the
switch stops and lists them. **Switch anyway** continues only when git
itself can keep those changes. Creating a branch uses
`git check-ref-format --branch` and does not rewrite files unless you also
switch to it. Fetch, pull, and push stay disabled while HEAD is detached.
Push stays available when the only gap is a missing upstream, so the
confirm dialog can run.

Before pull, switch, abort merge, or a stash operation that rewrites files,
unsaved request-editor drafts must be confirmed, because those operations
rewrite files on disk.

**Abort merge** is offered whenever `MERGE_HEAD` exists, including after
the conflicts have been staged. A rebase already in progress is named as
such and aborted with `git rebase --abort`. Resolve a conflict in your
editor and save; Verity reloads. The next Commit stages the files you
select. Verity does not edit conflict markers itself.

Fetch, pull, and push use `branch.<name>.remote` and `branch.<name>.merge`
(`refs/heads/…`). They do not split the abbreviated `@{upstream}` name, so
a remote whose name contains a slash is the remote that is fetched. Fetch,
pull, and push run one at a time. Cancel signals Git (SIGINT on Unix, CTRL_C
in an isolated console on Windows) and, after a short wait, kills it if needed.
The graceful signal lets Git remove `index.lock`. The kill is tied to that process and is
not delivered after git has exited. On Windows synchronous metadata polls run without a console. Cancellable
commands have a hidden private console and a job that ends child processes. A short-lived helper joins
that console and sends CTRL_C so Git can remove its locks, without changing the
CLI or desktop process's own console or signal handlers. If signalling fails,
cancellation falls back to terminating the job and process. `os.Interrupt` and
CTRL_BREAK are not treated as graceful Git cancellation on Windows.
The toolbar stays frozen while a confirmation dialog is open, and Cancel
and those dialogs stay up if you leave Source control. Progress lines are
git's own `--progress` text when it prints some, plus a start and finish
line. `GIT_TERMINAL_PROMPT=0` is set on every write, so a missing HTTP
credential fails instead of waiting at a prompt. When `GIT_SSH_COMMAND`
and `core.sshCommand` are both unset, Verity sets
`GIT_SSH_COMMAND=ssh -o BatchMode=yes`. It does not set
`StrictHostKeyChecking=accept-new`. SSH agents and git credential helpers
do the authentication. Verity never asks for or stores a git password,
token, or key.

Git's own error text is shown in a scrollable monospace box. A URL that
contains URL userinfo, a token in a remote URL query
string (`?token=` or `?access_token=`), or an echoed `http.extraHeader` is
masked first. `core.sshCommand` and credential helpers stay as you configured
them; Verity does not store the credentials they produce.

After a pull, merge, branch switch, merge abort, or stash rewrite, the app
reloads the project. A clean request takes the new text. A dirty draft keeps its edits
and its pre-rewrite step hash, and shows the disk-changed banner, until
you reload or keep those edits. A failed pull, switch, abort, or stash rewrite still
refreshes status. A file Verity saved is still ignored when the bytes
match that save; a rewrite git performed for Verity is not treated as
that save.

What Verity never does: force-push, `git add -A`, `--no-verify`, set
`user.name` / `user.email`, store git credentials, or resolve a merge
conflict inside the app.

### Stashes

Source control lists stashes with their index, branch, message, and date.
**Stash changes** saves all tracked changes in the repository, including staged
changes. Check **Include untracked files** to also save non-ignored new files
(`git stash push -u`). Ignored files are never included. **Only checked files**
limits the stash to the paths checked in Files, including both sides of a
selected rename. Unselected staged entries are temporarily unstaged and their
exact index entries restored afterwards, including on failure. Selected staged
deletions are supported. Git receives each path after `--` with a `:(literal)`
prefix. The stash message is optional. An empty selection or a selection with nothing eligible to save is refused with a clear
message. Submodule changes and untracked nested repositories are not eligible
on their own; Git does not save their contents.

Stash operations disable Git hooks, including `reference-transaction`. Stash
listing disables signature verification, and repository-local or worktree
custom merge drivers are replaced with a fixed `git merge-file` text merge
command for stash apply and pull. Empty merge-driver overrides are not used:
some Git versions treat them as an empty executable and omit conflict markers.
Stash diffs disable external diff and textconv programs and read blobs directly.

**Show diff** compares the saved project and fragments against the stash's
original base in the existing semantic diff view. Both sides are read from Git,
including the optional untracked tree. Secret-bearing values are masked by
the same scanner and redactor as working-tree changes before reaching the GUI.
Other files are counted, without showing their raw contents. Choose
**Working tree diff** to return to current changes.

**Apply** keeps the stash. **Pop** applies it and drops it only after a
successful apply. **Drop** deletes it without applying it. Pop and Drop always
ask for confirmation. Actions use the full object id captured when listing,
with a fresh check that it is still in the stash list. A stale id is refused;
shifting list positions do not select a different stash. Git accepts an object
id for apply; because drop requires a reflog selector, Verity resolves that
selector again from the id immediately before dropping, under its repository
write lock. As with other Git writes, that process lock does not lock out
external Git clients.

A conflicting apply or pop leaves the stash in place and lists the unmerged
files for **Open in editor**. Resolve and save in your editor, then select and
commit the resolved files. A stash conflict does not start a Git merge, so
**Abort merge** is available only when a merge is actually in progress.

Stash push, apply, and pop use the same project rewrite and reload flow as
pull. Unsaved drafts require confirmation and retain their original save
baseline; a changed file triggers the existing **Reload from disk** / **Keep
my edits** banner. A clean tab reloads. Stash metadata is watched too, so
changes made by another Git client refresh the list. The same runner disables
repository fsmonitor and local filters, prevents terminal credential prompts,
and handles cancellation for stash operations.
