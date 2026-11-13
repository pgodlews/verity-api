# Desktop functional regression suite

A short, repeatable check that a change has not broken the desktop app's core
features. It is written to be executed by an AI agent that can drive the native
app (screenshots, accessibility tree, mouse and keyboard) and run shell
commands, on macOS, Windows 11, and Linux. A person can follow it too.

**Scope.** Functional behavior of the running app only. Installation,
packaging, signing, OS security prompts, upgrade, screen readers, and visual
polish are out of scope; they stay in the [release plans](README.md). The suite
does not try to cover every path. Passing it means the main user journeys still
work, not that a release is ready.

**When to run.** After any major change to `core/`, `gui/`, or
`gui/frontend/`, before merging. Run the smoke set every time and add the
extended scenarios for the areas the change touched (see
[Which scenarios to run](#which-scenarios-to-run)). The smoke set should take
about 15 to 20 minutes per OS.

## Rules for the tester

1. Work only in the disposable workspace created below. Never open, edit, or
   commit in a real project or repository.
2. Use only the fixture token and runtime-generated fake values. Never type a
   real credential. Do not paste the fixture token into the results.
3. Report what you observed. Do not fix product code, retry until green, or
   change a scenario to make it pass. One retry is allowed for a step that
   failed for a reason unrelated to the app (for example a missed click);
   record that you retried.
4. A scenario is **pass** only if every expected result was observed in the
   native app. If you cannot observe a result (a native dialog is not visible
   to your tools, a control cannot be found), mark it **blocked** with the
   reason. Never infer a pass from the CLI, from the browser dev bridge, or
   from reading code.
5. Find controls by their visible text or accessible name as quoted in the
   steps. If a label has changed, use the obvious equivalent and note the new
   label in the result. If there is no obvious equivalent, mark the scenario
   blocked.
6. Native file pickers and confirmation dialogs may not appear in the
   accessibility tree. Take a screenshot before deciding one did not open.
7. Do not dismiss or bypass any OS security prompt (Gatekeeper, SmartScreen,
   keychain access). If one appears, stop and report it.
8. Where a step says *shell check*, run the command and compare its output.
   These checks are what make the result trustworthy; do not skip them.
9. Keep evidence free of usernames, home paths, and tokens. Refer to the
   workspace as `$WORK`.

Keyboard shortcuts are written as `Mod`, which is Command on macOS and Ctrl on
Windows and Linux.

## Setup

You are given: a checkout of the commit under test, and a way to start the
desktop app built from that commit (a built binary, or `wails dev` in `gui/`).
Building or installing the app is not part of this suite.

Record before starting: commit hash, OS and version, architecture, how the app
was started, and on Linux whether the session is X11 or Wayland.

Create the disposable workspace from the repository root.

macOS and Linux:

```bash
WORK=$(mktemp -d)
cp -R examples/local-demo "$WORK/demo"
cp -R examples/specs "$WORK/specs"
git -C "$WORK/demo" init -q
git -C "$WORK/demo" config user.name "Verity Tester"
git -C "$WORK/demo" config user.email "tester@example.invalid"
git -C "$WORK/demo" add -A
git -C "$WORK/demo" commit -qm "baseline"
go run ./examples/local-demo/mockserver --addr 127.0.0.1:8099 &
```

Windows (PowerShell):

```powershell
$Work = Join-Path $env:TEMP ("verity-regression-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory $Work | Out-Null
Copy-Item -Recurse examples\local-demo "$Work\demo"
Copy-Item -Recurse examples\specs "$Work\specs"
git -C "$Work\demo" init -q
git -C "$Work\demo" config user.name "Verity Tester"
git -C "$Work\demo" config user.email "tester@example.invalid"
git -C "$Work\demo" add -A
git -C "$Work\demo" commit -qm "baseline"
Start-Process go -ArgumentList "run","./examples/local-demo/mockserver","--addr","127.0.0.1:8099"
```

Then, in the same shell:

1. Set `API_TOKEN` to the test-only token defined in
   `examples/local-demo/mockserver/main.go`.
2. Start the desktop app **from this shell** so it inherits `API_TOKEN`. On
   macOS start the executable inside the app bundle (`Contents/MacOS/`), not
   through Finder or `open`.

*Shell check:* `curl -s http://127.0.0.1:8099/json` returns JSON containing
`"name":"widget"`. If port 8099 is taken, stop and report; the demo project
points at that port.

The project under test is `$WORK/demo/project.yaml`. It has three suites:
`REST` (case `get json`, step `fetch`), `Secured` (step `secure`, bearer auth
from `API_TOKEN`), and `SOAP` (case `add operation`, step `add`).

## Smoke set (run every time)

### RG-01 Launch and open a project

1. With the app started, confirm the welcome screen shows **Welcome to Verity**
   and an **Open project…** button.
2. Click **Open project…**, choose `$WORK/demo/project.yaml` in the native
   picker.

Expected:
- The header shows the project name **Local Demo** and the file name
  `project.yaml`.
- The environment picker shows `local`.
- The workspace lists suites `REST`, `Secured`, and `SOAP` with their requests.
- The status bar shows a branch name and `0 changed`.

### RG-02 Send a REST request

1. In the workspace open `REST` → `get json` → `fetch`.
2. Click **Send** (or press `Mod`+Enter).

Expected:
- The response pane shows HTTP status 200 and a JSON body containing
  `"name": "widget"` and `"id": 42`.
- The assertion results show four assertions, all passed.
- The captured values show `widgetName` = `widget`.
- No error banner appears.

### RG-03 Send a SOAP request

1. Open `SOAP` → `add operation` → `add` and click **Send**.

Expected:
- HTTP status 200 and an XML body containing `<Result>7</Result>`.
- Two assertions, both passed.

### RG-04 Run the collection with authentication and check redaction

1. Click **Run collection** in the header.
2. Wait for the **Runs** view to finish.

Expected:
- All three suites finish as passed; no step is failed or errored.
- The status bar reads `Last run (local)` with 8 checks passed, 0 failed,
  0 errors.
- Open the `secure` step detail. The fixture token does not appear anywhere in
  the Runs view (search the visible text for its first eight characters). The
  step detail does not list request headers, so there is no header value to
  inspect here.

### RG-05 Edit, save, and verify the file on disk

1. Open `REST` → `get json` → `fetch`.
2. Change the **Request URL** from `${BASE_URL}/json` to
   `${BASE_URL}/json?probe=1`.
3. Confirm the editor footer shows **Unsaved changes**.
4. Press `Mod`+S (or click **Save**). Confirm the footer returns to **Saved**.
5. Click **Send**.

Expected:
- The request still returns 200 with four passed assertions.
- *Shell check:* `git -C "$WORK/demo" diff --stat` lists only `project.yaml`.
  A save may also normalize formatting (quoting, blank lines, block style),
  so compare decoded values, not lines: load `git show HEAD:project.yaml` and
  the working file with a YAML parser and diff the two structures. The only
  difference must be the `url` of `fetch`.

### RG-06 Source control shows the change and commits it

1. Open **Source control** from the left rail.

Expected so far:
- `project.yaml` is listed as changed and is selected by default.
- The change is attributed to the `fetch` request.

2. Type the commit message `regression: probe` and click **Commit**.

Expected:
- The view reports the commit and the changed-file list is empty.
- The status bar shows `0 changed`.
- *Shell check:* `git -C "$WORK/demo" log -1 --format=%s` prints
  `regression: probe`, and `git -C "$WORK/demo" status --porcelain` prints
  nothing.

### RG-07 Plaintext credentials cannot be saved

1. Generate a fake value in the shell: 32 random letters and digits prefixed
   with `FAKE` (for example with `openssl rand -hex 16`). Do not reuse a real
   token.
2. Open `REST` → `get json` → `fetch`, add a header `Authorization` with the
   value `Bearer ` followed by the fake value.
3. Try to save.

Expected:
- The editor reports that the value "looks like a credential" and offers to
  store it as an environment variable or in the OS keychain.
- The step is not saved while the literal is present.
- *Shell check:* `git -C "$WORK/demo" status --porcelain` prints nothing, and
  searching `project.yaml` for the fake value finds nothing.

4. Remove the header again and save, so no draft is left unsaved. (An unsaved
   draft blocks closing the project in RG-08.)

### RG-08 Environments: add a variable and persist it

1. Open **Environments** from the left rail and select `local`.
2. Add a variable named `PROBE` with the value `one` and save.
3. Close the project (folder button in the header) and reopen it from
   **Recent projects**.

Expected:
- `PROBE` = `one` is still listed under `local`, and `BASE_URL` is unchanged.
- *Shell check:* `git -C "$WORK/demo" diff` shows `PROBE: one` added under the
  `local` environment and no other value changed (quoting may differ).

### RG-09 Load test: run, reject bad input, keep the report

1. Open **Load test** from the left rail.
2. Select only the `REST` suite first (the layout shifts when a suite is
   selected), then set **Virtual users** to 1, run mode **Duration**, and
   **Duration (s)** to 2. Take a fresh screenshot and click **Start load test**.

Expected:
- The target host list shows `127.0.0.1` and no refusal message.
- The run finishes with a report: requests greater than 0, 0 failures, and
  latency figures.

3. Set **Virtual users** to 0 and click **Start load test** again.

Expected:
- A validation message appears and no run starts.
- The previous report is still displayed and **Save JSON report** is still
  available.

### RG-10 Import an OpenAPI document

1. Open **Import** from the left rail.
2. Choose source **OpenAPI / Swagger**, pick `$WORK/specs/petstore.openapi.yaml`, and
   click **Preview**.

Expected:
- The preview lists the converted requests (three at the time of writing) and
  shows the generated YAML.
- *Shell check:* no new file has been created in `$WORK/specs` yet.

3. Set **Save as** to `petstore.yaml` (it may be pre-filled) and click the
   import button (**Import 3 requests** after a preview).

Expected:
- The app opens the imported project and lists its requests.
- *Shell check:* `$WORK/specs/petstore.yaml` exists and
  `verity validate "$WORK/specs/petstore.yaml"` exits 0 (skip this check and
  say so if no `verity` CLI built from the same commit is available).

4. Reopen `$WORK/demo/project.yaml` from **Recent projects** for the remaining
   scenarios.

## Extended scenarios (run when the area changed)

### RG-11 A failing request is reported, not swallowed

Area: runner, protocols, response pane.

1. Stop the fixture server.
2. Open `REST` → `get json` → `fetch` and click **Send**.

Expected:
- The step is reported as an error that names a connection problem. It is not
  shown as passed, and the app stays responsive.

3. Start the fixture server again and click **Send**.

Expected:
- The request returns 200 with four passed assertions.

### RG-12 Missing credentials fail only the secured request

Area: auth, secrets, runner.

1. Quit the app. Start it again from a shell where `API_TOKEN` is **not** set
   and open the demo project.
2. Click **Run collection**.

Expected:
- `REST` and `SOAP` pass. The `secure` step returns HTTP 401 and its
  assertions fail. The run is reported as failed overall.

3. Quit and restart the app with `API_TOKEN` set before continuing.

### RG-13 External file changes are detected and drafts are protected

Area: file watching, saves, editor.

1. With `fetch` open and no unsaved edits, change the file from the shell:
   replace `widget` with `gadget` in the `value:` line of the `$.name`
   assertion in `$WORK/demo/project.yaml`.

Expected:
- Within a few seconds the open request shows the new expected value without
  any prompt.

2. Start an edit in the app (change the URL, do not save). From the shell,
   change `gadget` back to `widget`.

Expected:
- The app reports that the request changed on disk and offers **Reload from
  disk** and **Keep my edits**. The draft is not discarded silently.

3. Choose **Reload from disk**.

Expected:
- The editor shows the on-disk content and is no longer marked unsaved.

### RG-14 Cancel a running load test

Area: load, runner, cancellation.

1. In **Load test** set **Duration (s)** to 20 and click **Start load test**.
2. After live statistics appear, click **Cancel**.

Expected:
- The run stops within a few seconds, the view returns to idle, and a partial
  report is shown and labeled as interrupted.
- **Start load test** can be clicked again.

### RG-15 Branches

Area: source control, Git layer.

1. In **Source control** create a new branch named `regression-probe` and
   switch to it.

Expected:
- The status bar shows `regression-probe`.
- *Shell check:* `git -C "$WORK/demo" branch --show-current` prints
  `regression-probe`.

### RG-16 Import the other formats

Area: importers.

Repeat RG-10 steps 1 to 3 for `calculator.wsdl` (source **WSDL**),
`sample.postman_collection.json` with `sample.postman_environment.json`
(source **Postman collection**), and `sample.soapui.xml` (source
**SoapUI project**), using a
different project file name each time.

Expected for each:
- Preview lists converted requests and any conversion warnings.
- Import creates the file and opens a project that lists its requests.

### RG-17 Theme and search

Area: app shell.

1. Toggle the theme with the header button; quit and restart the app.
2. Press `Mod`+K and type `secure`.

Expected:
- The chosen theme is kept after restart.
- The search field takes focus and the workspace shows only the matching
  request.

## Which scenarios to run

| Change touches | Run |
| --- | --- |
| Anything (default) | RG-01 to RG-10 |
| `core/runner`, `core/protocols`, `core/assertions` | add RG-11, RG-12 |
| `core/auth`, `core/secrets`, `core/secretscan` | add RG-12 |
| `core/workspace` saves, `core/watchfs`, request editor | add RG-13 |
| `core/load` | add RG-14 |
| `core/vcs`, `core/workspace` Git, Source control view | add RG-15 |
| `core/importers`, Import view | add RG-16 |
| `gui/app.go`, `gui/frontend` shell (`App.tsx`, `api.ts`) | add RG-17 |

When in doubt, run everything; the full suite is about 40 minutes per OS.

## Platform notes

- **macOS:** the app must be started from the shell for `API_TOKEN` to reach
  it. Controls inside the window cannot be clicked in the background, so the
  run needs full-screen control of a Mac nobody is using (about 15 minutes for
  the smoke set); any keyboard or mouse input interrupts it. Drive it from
  the session that holds screen control, not from a delegated sub-agent.
  Confirmation dialogs have not been observable; see the
  [13 November run](regression-results-2026-11-13-macos.md).
- **Windows 11:** paths use backslashes and a drive letter. Confirm that no
  console window flashes during RG-05, RG-06, and RG-15 (Git operations); a
  visible console window is a failure.
- **Linux:** record X11 or Wayland. If the native file picker cannot be driven
  in the session, mark RG-01 and RG-10 blocked rather than opening the project
  another way.

## Teardown

Stop the fixture server, quit the app, and delete `$WORK`. Remove nothing else.
Leave the checkout unchanged.

## Reporting

Fill in [the results template](regression-results-template.md), one row per
scenario you ran. For every **fail** or **blocked** row add the scenario ID,
the step number, what was expected, what was observed, and sanitized evidence
(a screenshot description or the shell output). State the overall verdict:

- **Pass:** every scenario run passed.
- **Fail:** at least one scenario failed. List them.
- **Incomplete:** nothing failed but one or more scenarios were blocked.

A run on one OS says nothing about the others; report each OS separately.
