# Desktop regression results — Windows 11, 17 November 2026

First Windows execution of [the regression suite](regression-suite.md): the
smoke set plus RG-12, RG-13, RG-14 and RG-15. The previous run is
[14 November 2026 on macOS](regression-results-2026-11-14-macos.md).

## Run

| Field | Value |
| --- | --- |
| Commit under test | `6472bf1` on `main` (fix(assertions): build a valid file URL for json_schema files on Windows) |
| Change being checked (branch or PR) | `main` after PR #11 (audit follow-ups) and PR #30 (Windows json_schema file URL) |
| OS and version | Windows 11 Pro 10.0.26300 |
| Architecture | x64 (amd64) |
| Linux session (X11 or Wayland) | not applicable |
| How the app was started | Built binary (`gui\build\bin\verity-desktop.exe`, `wails build` from `6472bf1`), started with `Start-Process` from a PowerShell process that had `API_TOKEN` set to the fixture token (unset for RG-12). The window was maximized (1280×720 logical). |
| Tester | AI agent with desktop control (full-screen), plus shell checks |
| Date | 17 November 2026 |
| Scenarios selected | Smoke (RG-01 to RG-10), plus RG-12, RG-13, RG-14 and RG-15 |

### Setup deviations

- **Mock server window.** The suite's `Start-Process go …` opens a console window, which stayed in front of the app and blocked clicks. Adding `-WindowStyle Minimized` did not help, because Windows Terminal is the default console host. I stopped that instance and ran `go run ./examples/local-demo/mockserver --addr 127.0.0.1:8099` as a background process with no console window. This had the side benefit that no console window existed that could be confused with one opened by the app. The shell check returned JSON with `"name":"widget"` before and after the restart.
- **Duplicate app instance.** The desktop-control tool's "open application" call started a second copy of the app, which did not inherit `API_TOKEN`. I closed that copy before any scenario ran. All scenarios ran in the instance started from the shell.
- **Task Manager masking.** An elevated Task Manager window sat behind the app, and the screenshot tool masks windows that are not granted, regardless of z-order. It was closed before RG-01. No scenario was affected.
- **Console-flash detection.** A screenshot cannot reliably catch a console window that flashes for a few milliseconds. For the whole run, I ran a background watcher that enumerated visible top-level windows every 40 ms and logged every new one, plus every new `git`, `conhost`, `cmd`, `OpenConsole` and `WindowsTerminal` process. A control test, launching a visible `cmd` window from the shell, was detected (`WindowsTerminal` / `CASCADIA_HOSTING_WINDOW_CLASS`). That confirms the watcher would have seen a console window.

## Results

| ID | Scenario | Status | Notes |
| --- | --- | --- | --- |
| RG-01 | Launch and open a project | pass | Welcome screen and **Open project…** shown. Native picker: path typed into **File name**. Header showed `Local Demo / project.yaml`, env `local`, suites REST/Secured/SOAP. Status bar showed `master 0 changed` (it appeared about 2 s after the project opened) and `1 of 1 secrets set`. |
| RG-02 | Send a REST request | pass | 200 OK, body with `"id": 42` and `"name": "widget"`. Checks 4/4 passed. Captured `widgetName` = `widget`. No error banner. |
| RG-03 | Send a SOAP request | pass | 200 OK, `<Result>7</Result>`, 2 of 2 checks passed. |
| RG-04 | Run collection, check redaction | pass | PASSED: All 3, Failed 0, Errors 0. Status bar: `Last run (local): 8 checks passed · 0 failed · 0 errors`. The `secure` detail (GET `http://127.0.0.1:8099/secure`, 2 checks, response `{"ok": true, "user": "demo"}`) and the `fetch` detail did not show the fixture token. I inspected the visible text by eye; the webview has no text search. |
| RG-05 | Edit, save, verify file on disk | pass | **Unsaved changes** shown, then **Saved** after Ctrl+S ("Saved fetch."). Send after save: 200 OK, 4 of 4. `git diff --stat` lists only `project.yaml`. The structural YAML diff (gopkg.in/yaml.v3, HEAD against the working file) shows exactly one difference: `suites[0].cases[0].steps[0].request.url`. Two notes: the raw diff is not one line (see observation 1), and no console window appeared. |
| RG-06 | Source control change and commit | pass | `project.yaml` was listed as MODIFIED and checked by default. The change was attributed to REST › get json › `fetch` (`url` before/after). After **Commit** the view showed `[master 2f68405] regression: probe` and "No changes since the last commit", and the status bar showed `master 0 changed`. Shell: `log -1` prints `regression: probe` and `status --porcelain` is empty. No console window appeared. |
| RG-07 | Plaintext credentials cannot be saved | pass | With `Authorization: Bearer FAKE…` the editor said "Looks like a credential…" and offered **Store as ${env:AUTHORIZATION}** and **Store in OS keychain**, with the footer "1 value looks like a credential — store it in the OS keychain or as an environment variable to save." Ctrl+S did not save. Shell: `status --porcelain` was empty and the fake value was found nowhere under `$WORK`. After removing the header and saving: "Saved fetch.", `0 changed`, worktree clean. One missed input: the new header row starts with a literal `key` in the key field, so my first typing produced `keyAuthorization`. I corrected it to `Authorization` before the save attempt that counts (see observation 4). |
| RG-08 | Environment variable persists | pass | Added `PROBE` = `one` under `local`, and the view said "Saved". After closing the project and reopening it from **Recent projects**, `PROBE` = `one` was still listed and `BASE_URL` was unchanged. `git diff` showed exactly one added line, `PROBE: one`, under `local`. |
| RG-09 | Load test run, bad input, report kept | pass | REST only, 1 VU, Duration, 2 s. Target hosts: `127.0.0.1 (private)`, with no refusal. Report: PASSED, 13477 requests, 0 failures, 0 errors, mean 0.14 ms, p95 0.53 ms. With VUs = 0: banner "Correct the traffic profile fields below before starting." and field error "Virtual users must be an integer from 1 to 1000." No run started, and the previous report and **Save JSON report** stayed visible. Retried once: setting VUs to 0 moved **Start load test**, so my first click hit empty space. |
| RG-10 | Import OpenAPI | pass | Preview: 1 suite, 3 requests, 3 checks, 3 cases, 1 environment, "No warnings". **Preview YAML** showed the generated YAML. Shell: no new file in `$WORK/specs` after the preview. **Save as** was pre-filled with `petstore.yaml`. **Import 3 requests** opened the Petstore project with `listPets`, `createPet` and `getPetById`. Shell: `$WORK/specs/petstore.yaml` exists, and `verity validate` (CLI built from `6472bf1`) printed `OK … is valid` with exit code 0. The demo project was then reopened from **Recent projects**. |
| RG-12 | Missing credentials fail only secured request | **fail** | Expected drift, not a regression; see below. With `API_TOKEN` unset (status bar: `0 of 1 secrets set`), REST and SOAP passed. `secure` was reported as an **Error** with `auth_error`, and the request was not sent, so there was no HTTP 401 and there were no assertion failures. The run was reported as `ERROR` overall: Failed 0, Errors 1, Passed 2, and the status bar read "6 checks passed · 0 failed · 1 errors". After a restart with the token set, the status bar showed `1 of 1 secrets set`. |
| RG-13 | External changes and draft protection | pass | Step 1: within about 3 s the `$.name` assertion showed `"gadget"` with no prompt. Step 2: after an unsaved URL edit and the external change back to `widget`, the banner "This request changed on disk (branch switch or pull?)" appeared with **Reload from disk** and **Keep my edits**, and the draft was kept. Step 3: **Reload from disk** showed `"widget"` and the original URL, and the header returned to **Saved**. |
| RG-14 | Cancel a running load test | pass | Duration 20 s, 1 VU. Live stats appeared (Active VUs, RPS, p95, Errors). **Cancel** stopped the run about 5.4 s in: the view showed `INTERRUPTED` with a partial report (36163 requests) and **Start load test** returned. Clicking it started a new run, which I cancelled again (`INTERRUPTED`, 4169 ms). Each cancelled run counted one error, `context canceled` (see observation 5). |
| RG-15 | Branches | pass | **New branch** → `regression-probe` with **Switch to it** checked → **Create**. Because RG-08 had left `project.yaml` modified, the app first stopped: "These project files have uncommitted changes, so the switch stopped: project.yaml … Switch anyway only if git itself can keep the changes." I chose **Switch anyway**. The view said "Switched". The branch picker and status bar changed to `regression-probe (current)` / `regression-probe 1 changed` after roughly 5 to 10 s; for the first few seconds they still showed `master`. Shell: `branch --show-current` prints `regression-probe`, and the modification was kept. No console window appeared. |
| RG-11, RG-16, RG-17 | Extended scenarios | not run | Not selected. |

## Windows-specific check: no console window during Git operations

**Pass for RG-05, RG-06 and RG-15.** The window watcher logged no new console window class (`ConsoleWindowClass`, `CASCADIA_HOSTING_WINDOW_CLASS`, `PseudoConsoleWindow`) and no new `OpenConsole` or `WindowsTerminal` process at any time after the control test. The only new top-level windows during the run were two WebView2 tooltip popups (`msedgewebview2`, `Chrome_WidgetWin_1`, at the moment I clicked **Commit**), the app's own **Choose an OpenAPI spec** dialog, and the app's main window on each restart. Screenshots taken right after save, commit and branch switch also showed no console window. The app's `git` children each get a hidden `conhost`, which is consistent with them being started without a console window.

## Failures and blocked scenarios

- **ID and step:** RG-12, step 2.
- **Expected (suite at the time of the run):** `REST` and `SOAP` pass. The `secure` step returns HTTP 401 and its assertions fail. The run is reported as failed overall.
- **Observed:** `REST` and `SOAP` passed. The app did not send `secure`, and reported it as an **Error** with the message `resolve auth: auth_error: auth "bearer": config "token" references ${env:API_TOKEN}, which is not defined when this step runs (environment variables, --var, ${env:NAME}, secrets, and values captured by earlier steps of the case are)` and code `auth_error`. The run header read `ERROR` (Failed 0, Errors 1, Passed 2), and the button changed to **Re-run failed**.
- **Evidence (sanitized):** Runs view screenshot described above. Status bar: `Last run (local): 6 checks passed · 0 failed · 1 errors · local · 2 variables · 0 of 1 secrets set`.
- **Retried:** no.
- **Assessment:** This is expectation drift from PR #11, not a regression. The CLI also ends `secure` with `auth_error` without sending when `API_TOKEN` is unset. The scenario's intent holds: missing credentials fail only the secured request, nothing is sent with an empty token, and the overall run is not reported as passed. RG-12's expected result in the suite is updated in the same change as this record.

## Labels that differed from the suite

- RG-04 / RG-12: the overall run state is labeled **PASSED** / **ERROR**. The suite said "failed overall", and the app reports a missing credential as an error, not a failure.
- RG-07: the offered actions are **Store as ${env:AUTHORIZATION}** and **Store in OS keychain**.
- RG-15: the create button is **Create**, under a **Switch to it** checkbox. With uncommitted changes, an extra confirmation, **Switch anyway** / **Cancel**, appears; the suite doesn't mention it.
- RG-10: the import button read **Import 3 requests** and **Preview YAML** is a separate button, as the suite describes.

## Observations (product)

1. **Saves normalize YAML formatting on Windows.** RG-05's first save changed the header's `description: >` to `description: |` and removed four blank lines between top-level sections and suites, in addition to the URL. The decoded structure differs only in the URL, so RG-05 passes as written. The [14 November macOS run](regression-results-2026-11-14-macos.md) reported this rewrite as fixed, with a one-line diff, so the rewrite is back, or it happens only on Windows, perhaps with CRLF checkouts. Git warned that `project.yaml` LF would be replaced by CRLF. RG-08's later save changed exactly one line.
2. **A burst of short-lived `git` processes on save and on commit.** The watcher logged about 200 new `git.exe` processes, each with a hidden `conhost`, within about 4 s of the RG-05 save, and about 260 around the RG-06 commit. In total it logged 673 `git` processes in about 7 minutes, a handful of them from my own shell checks. None showed a window, and the app stayed responsive. The processes exited too quickly for their parent process to be recorded, so this rests on timing. It may be worth checking whether per-request change attribution runs one `git` call per request or per field.
3. **The branch indicator lags after a switch.** In RG-15 the view said "Switched" while the branch picker and status bar still showed `master` for several seconds before updating to `regression-probe`.
4. **A new header row starts with a literal `key` value.** Typing into the key field of a freshly added header row appended to `key` (the result was `keyAuthorization`) instead of replacing it. I did not check whether this is a placeholder that fails to clear on Windows or a real default value.
5. **Cancelling a load test counts the in-flight request as an error.** Each cancelled run showed "1 request failures/errors (0 assertion failures, 1 errors)", with `request failed: … context canceled` under **Recorded failure messages**.
6. **Load-test latency shows `0.00 ms` for min and p50**, while the mean is 0.14 ms and p95 is 0.53 ms. This may be coarse timer resolution on Windows.
7. **An imported project's `description` still contains the absolute path of the source spec**, as noted on macOS on 14 November.

## Not covered

- RG-11, RG-16 and RG-17 were not selected.
- I could not search the Runs view as text in RG-04, so the redaction check is a visual inspection of all visible text.
- The app log: the Windows build writes no log file. The only files under its data folder are WebView2 diagnostics. I could not do the "no panic or fatal" log check that the macOS run did.

## Teardown

The app was quit, the mock server stopped (port 8099 no longer listening), and `$WORK` deleted. The checkout is unchanged: `git status --porcelain` is empty and HEAD is still `6472bf1`. Nothing was committed or pushed in the repository.

## Verdict

**Fail.** The only failing row is RG-12, and it fails solely because of the known expectation drift (`auth_error` without sending, instead of HTTP 401). Every other scenario run (RG-01 to RG-10, RG-13, RG-14, RG-15) passed, and no console window appeared during Git operations. Against RG-12's updated expected result, this run would be a **Pass**.
