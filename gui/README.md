# Verity GUI

A [Wails v2](https://wails.io) desktop app — a thin shell over the same `core`
library the CLI uses. The Go backend (`app.go`) binds core operations
(validate, run, import, catalog) to a React + TypeScript frontend. It uses the
OS-native webview (not Electron), producing a single self-contained binary.

**No protocol/auth/test/reporting logic lives here.** Secret redaction,
assertions, and everything else come from core, so the GUI inherits the same
guarantees as the CLI — including that secrets shown in the inspector are masked
by core before they reach the frontend.

## Prerequisites

- Go 1.26+
- Node.js 20+ and npm
- The Wails CLI:
  ```bash
  go install github.com/wailsapp/wails/v2/cmd/wails@latest
  ```
  Plus platform webview deps (see the Wails docs): WebKitGTK on Linux, WebView2
  on Windows, nothing extra on macOS.

## Develop

```bash
cd gui
wails dev      # hot-reload: Go backend + Vite frontend
```

### In a plain browser (no Wails needed)

`npm run dev` also works on its own. Outside the Wails shell the frontend
calls a dev-only bridge (`frontend/vite.bridge.ts`) that runs
`verity gui-call <Method>`; that command and the desktop app (`app.go`)
both call `core/workspace`, so the browser gets the same behaviour and real,
redacted results. OAuth sign-in sessions require the desktop app because
the browser bridge starts a fresh CLI process per call. Mock mode simulates
sign-in and sign-out without opening a browser.

```bash
go build -o verity ./cmd/verity                 # at the repo root; or set VERITY_BIN
cd gui/frontend && npm ci && npm run dev  # open the URL printed by Vite
```

Add `?mock` to the URL to use built-in fixture data (`src/dev/`) instead of
the backend — handy for working on views. Screenshots of the UI live in
`docs/screenshots/`.

### Frontend layout

- `src/types.ts`, `src/api.ts` — the contract with the Go side (mirrors
  `core/workspace`, `core/model`, `core/spi`).
- `src/style.css` — design tokens (dark default, light via
  `data-theme="light"`) and shared primitives; `src/icons.tsx` — icons.
- `src/App.tsx` — shell: welcome, header, activity rail, status bar.
- `src/views/` — Workspace (request editor), Runner, Load test, Import, Environments, Changes.
- `src/components/`, `src/lib/` — Workspace parts and helpers
  (`node --test src/lib/workspace.test.mjs src/lib/stepSecrets.test.mjs`).

While a request has unsaved edits, the editor asks `CheckStep` whether any
header, query parameter, JSON or form field, or step variable is a plaintext
credential. The finding names the field and a suggested `${env:NAME}`; it
never includes the value. The field is marked in place, Save (and Ctrl/Cmd+S)
stays disabled, and **Store as ${env:NAME}** replaces the literal in the
draft. An Authorization header keeps its scheme (`Bearer ${env:NAME}`).
Choose an account name (optionally `project/NAME`) beside **Store in OS
keychain**. The action stores the credential under service `verity` and replaces
the draft with
`${secret:keychain:NAME}` only after a successful store. Existing items are
replaced. A failed/locked store leaves the draft intact. Authorization schemes
are preserved; credentials embedded in larger text still need manual edits.
Save afterward to persist the reference. `?mock` uses an in-memory fake without
OS or browser persistence. See `docs/auth.md` for platform requirements.
`SaveStep` still refuses a step the check missed. JSON request bodies and
GraphQL variables cross the desktop bridge as source text, so formatting or an
unrelated edit does not round large numbers, `1e400`, `-0`, or decimal literals.

Request and environment saves normalize YAML indentation while preserving comments,
document markers, and unrelated scalar values. Folded multiline text may use
literal style to preserve its decoded value across repeated saves.

With a project open, the status bar shows the git branch (or a detached short
commit), how many paths differ from HEAD, and ahead/behind when the branch
tracks a local upstream. The collection marks added and modified requests.
**Source control** (after Environments) compares the working tree with the
last commit: each request's changed parts (including order and a move's
previous suite and case), environment and auth edits, suite and case fields,
project metadata, and a count of other files in the repo. When the projects
differ and none of those parts were reported, it shows one modified-project
row. Newly changed project files start checked; other repo files are collapsed.
Explicit checkbox choices survive refreshes and files disappearing and returning,
until another project is opened. Commit
stages only the checked paths, refuses a staged plaintext secret even when
no hook is installed, then runs git's hooks. Pull is fast-forward only until
you choose merge. Push never force-pushes; a branch without an upstream asks
before `git push -u`. Conflicts open in your editor.

Source control supports Git stashes: save all tracked changes or checked files,
optionally include non-ignored untracked files, inspect a redacted semantic
diff, and apply/pop/drop by immutable id. Pop and drop ask for confirmation;
conflicts keep the stash for resolution in your editor. Unsaved drafts use the
same reload banner and save-baseline protection as pull. See
[`docs/git.md`](../docs/git.md#stashes). The `?mock` bindings include these actions.

A file saved by the app is not treated as an outside edit when its bytes
still match what was written. A pull, switch, merge abort, or stash rewrite is not that
save: the app reloads the project hashes, and a draft that was already open
keeps its edits and asks whether to reload. The question belongs to that
tab. The same banner appears when a save finds the file already changed
(`conflict`). Keep re-reads the step hash and clears the banner. When the
step has no hash, Keep still clears it and the save checks the file hash
instead. Unsaved drafts are also confirmed before pull, switch, or
abort merge. `?mock` supplies a sample branch, two changed requests, a
diverged pull, a merge conflict, a hook-refused commit, and push without an
upstream. **Simulate disk change** fires the reload path in the browser
and changes the sample step hashes, so Keep my edits adopts the new baseline.
The dev bridge answers the git calls and does not watch the filesystem or
cancel an in-flight operation. Omitting both the step hash and the file
hash still writes, which is what the bridge does. The desktop editor
requires one of them. Fetch, pull, and push in the desktop app emit
`git:progress` and `CancelGit` stops the one running git operation.

## Build a single binary

```bash
cd gui
wails build    # outputs build/bin/verity-desktop (self-contained)
```

Cross-platform builds use Wails' platform targets, e.g.:

```bash
wails build -platform windows/amd64
wails build -platform darwin/universal
wails build -platform linux/amd64
```

## Frontend only (verify TS/React)

```bash
cd gui/frontend
npm ci
npm test
npm run build      # tsc + vite build → frontend/dist
```

Wails embeds `frontend/dist` into the Go binary via `//go:embed` in `main.go`.

## How the binding works

`app.go` defines an `App` struct whose exported methods are bound by Wails and
become callable from TypeScript. The frontend wraps them in `src/api.ts`:

| Go method (`App`) | Frontend (`api.*`) | Mirrors CLI |
|-------------------|--------------------|-------------|
| `Catalog()` | `api.catalog()` | `verity list` |
| `ValidateProject(path, environment)` | `api.validate(path, environment)` | `verity validate --env` |
| `RunProject(opts)` | `api.run(...)` | `verity run` |
| `CancelRun()` | `api.cancel()` | Ctrl-C during `verity run` |
| `StartLoad(opts)` | `api.startLoad(...)` | `verity load` |
| `CancelLoad()` | `api.cancelLoad()` | Ctrl-C during `verity load` |
| `LoadTargets(opts)` | `api.loadTargets(...)` | `verity load` target screen |
| `SaveLoadReport(req)` | `api.saveLoadReport(...)` | `verity load --json` |
| `ImportSpec(kind, path)` | `api.importSpec(...)` | `verity import` |
| `LoadProjectJSON(path)` | `api.loadProjectJSON(path)` | — |

`RunProject` returns the same `spi.RunResult` the CLI's JSON reporter emits, so
the inspector renders identical, already-redacted data. While the call is in
flight it also emits Wails events named `run:event` (`case_started`,
`step_done` with the redacted step, `case_done`). A second `RunProject`
while one is running returns `busy`. `?mock` simulates the events with
timers. The dev bridge (`verity gui-call`) stays non-streaming.

`StartLoad` runs `core/load` over the selected suites and cases and returns the
same redacted `load.Report` as `verity load --json`. While running, it emits
`load:live` about once a second (`vus`, `rps`, `p95Ms`, `errors`, `requests`,
`elapsedMs`), and `CancelLoad` stops new iterations and returns the partial
report (`interrupted: true`). `LoadTargets` screens target hosts against the
private-address rule without sending traffic; **Allow remote targets** requires
an explicit per-run checkbox and is never remembered between runs. See
[`docs/load.md`](../docs/load.md).

## Note on this repo

`gui/` is a separate Go module (with a `replace` to local core) so the base CLI
binary never compiles Wails. Interactive OAuth Authorization Code + PKCE
sign-in runs in the desktop app; CLI/CI consume a pre-obtained refresh token.

## OAuth sign-in

In a request's Auth tab, an `oauth2` provider with
`grant: authorization_code` offers Sign in / Signed in · Sign out. The system
browser handles consent; core manages PKCE and a loopback callback. Sessions
last only for this desktop process and never expose tokens to React. See
[`docs/auth.md`](../docs/auth.md) for callback registration and CI refresh tokens.

## Release testing

Use the [desktop testing index](../docs/testing/README.md) for shared scenarios,
Windows 11 and Linux plans, historical macOS evidence, and the release result
record. Planned scenarios are separate from completed native testing.
