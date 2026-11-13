# Desktop regression results — macOS, 13 November 2026

First execution of [the regression suite](regression-suite.md). Smoke set only.

## Run

| Field | Value |
| --- | --- |
| Commit under test | `1d280ae` |
| Change being checked (branch or PR) | `fix/audit-findings` (PR #9) |
| OS and version | macOS 27.2.1 (26B131) |
| Architecture | arm64 |
| Linux session (X11 or Wayland) | not applicable |
| How the app was started | built binary (`wails build`), started from a shell with the fixture token set |
| Tester | AI agent with desktop control (full-screen), plus shell checks |
| Date | 13 November 2026 |
| Scenarios selected | smoke only (RG-01 to RG-10) |

## Results

| ID | Scenario | Status | Notes |
| --- | --- | --- | --- |
| RG-01 | Launch and open a project | pass | Header showed `Local Demo / project.yaml`, env `local`, three suites, `main 0 changed`. |
| RG-02 | Send a REST request | pass | 200 OK, body as expected, 4 of 4 checks passed, Captured count 1. The captured value itself was not opened in the response pane. |
| RG-03 | Send a SOAP request | pass | 200 OK, `<Result>7</Result>`, 2 of 2 checks passed. |
| RG-04 | Run collection, check redaction | pass, one expectation not observable | All three steps passed; status bar `8 checks passed · 0 failed · 0 errors`. The fixture token was not visible anywhere. The Runs step detail does not show request headers, so `Bearer ***REDACTED***` could not be observed there (suite corrected). |
| RG-05 | Edit, save, verify file on disk | pass, shell check corrected | Unsaved and Saved states shown; request returned 200 with 4 of 4 checks. Decoded project values differ only in the URL. The raw diff was larger than one line: the save also normalized quoting, blank lines, and the folded description style (see observation 3). |
| RG-06 | Source control change and commit | pass | `project.yaml` listed and selected by default, change attributed to `fetch`. After commit the list was empty. `git log -1` printed `regression: probe`; `git status --porcelain` printed nothing. |
| RG-07 | Plaintext credentials cannot be saved | pass | "Looks like a credential" panel with **Store as ${env:AUTHORIZATION}** and **Store in OS keychain**; Save disabled. Nothing written to disk; fake value absent from `project.yaml`. |
| RG-08 | Environment variable persists | pass | `PROBE = one` present after close and reopen from Recent projects; `BASE_URL` value unchanged. Closing the project needed a workaround (see observation 1). |
| RG-09 | Load test run, bad input, report kept | pass | 1 VU, 2 s, REST suite: 11550 requests, 0 failures, `p95<500ms` passed. With 0 VUs: validation message, no run, previous report and **Save JSON report** still shown. The target host list was not inspected. |
| RG-10 | Import OpenAPI | pass | Preview: 1 suite, 3 requests, no warnings; no file written by preview. Import created `petstore.yaml` and opened it; `verity validate` exited 0. |
| RG-11 to RG-17 | Extended scenarios | not run | |

## Observations (product)

None of these was compared against `main`, so they are not known to be
regressions from the change under test.

1. **Close project does nothing while a draft is unsaved.** With an unsaved
   request draft, clicking the close-project (folder) button had no visible
   effect: no confirmation dialog appeared in the window, in a screenshot, or
   in the app's window list, and the project stayed open. After saving the
   draft the same button closed the project. This matches the candidate issue
   in [the macOS follow-up](macos-ui-followup-2026-11-06.md).
2. **Adding then removing a header leaves the draft unsaved,** and saving it
   writes an empty `headers: {}` into the step.
3. **The first save of a request rewrites formatting elsewhere in the file:**
   quotes around some values are dropped, blank lines between suites are
   removed, and the folded `description: >` becomes a literal block. Decoded
   values were unchanged.

## Labels that differed from the suite

- **Unsaved changes** / **Saved** appear in the request editor footer, not the
  header.
- The import button reads **Import 3 requests** after a preview.
- The project file name field is labeled **Save as** and is pre-filled from
  the chosen file.

## Notes on running the suite on macOS

- Controls inside the app window cannot be clicked in the background; the run
  needs full-screen control of an unattended Mac.
- A delegated sub-agent could not hold full-screen control (every call was
  cancelled before its first action); the scenarios were driven from the lead
  session instead.

## Verdict

**Pass** — all ten smoke scenarios passed; one expectation (RG-04 header
redaction) could not be observed and the suite was corrected, and three
product observations are recorded above.
