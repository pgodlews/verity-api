# Desktop regression results — macOS, 14 November 2026

Second execution of [the regression suite](regression-suite.md): the smoke set
plus RG-13, after the audit follow-up batch (PR #11). The previous run is
[13 November 2026](regression-results-2026-11-13-macos.md).

## Run

| Field | Value |
| --- | --- |
| Commit under test | `e24ae12` (the later `36c5b40` on `main` changes CI configuration only) |
| Change being checked (branch or PR) | `fix/audit-followups` (PR #11), after merge |
| OS and version | macOS 27.2.1 (26B131) |
| Architecture | arm64 |
| Linux session (X11 or Wayland) | not applicable |
| How the app was started | built binary (`wails build`), started from a shell with the fixture token set |
| Tester | AI agent with desktop control (full-screen), plus shell checks |
| Date | 14 November 2026 |
| Scenarios selected | smoke (RG-01 to RG-10) and RG-13 |

## Results

| ID | Scenario | Status | Notes |
| --- | --- | --- | --- |
| RG-01 | Launch and open a project | pass | |
| RG-02 | Send a REST request | pass | 200 OK, 4 of 4 checks passed, `widgetName` captured. |
| RG-03 | Send a SOAP request | pass | 200 OK, `<Result>7</Result>`, 2 of 2 checks passed. |
| RG-04 | Run collection, check redaction | pass | 8 checks passed, 0 failed, 0 errors. The fixture token was not visible in the Runs view. |
| RG-05 | Edit, save, verify file on disk | pass | The raw diff is exactly one line (the URL). The formatting rewrite seen on 13 November is gone. |
| RG-06 | Source control change and commit | pass | |
| RG-07 | Plaintext credentials cannot be saved | pass | Credential panel shown, Save disabled, nothing written to disk. |
| RG-08 | Environment variable persists | pass | The save added only the `PROBE: one` line to the file. |
| RG-09 | Load test run, bad input, report kept | pass | 30870 requests, 0 failures. With 0 VUs: validation message, no run, previous report kept. |
| RG-10 | Import OpenAPI | pass | Preview listed 3 requests; the generated YAML is behind a **Preview YAML** button (suite corrected). The imported file validated with the CLI (exit 0). |
| RG-11, RG-12 | Extended scenarios | not run | |
| RG-13 | External change and draft protection | pass | Without a draft the request reloaded silently; with a draft the app offered to reload or keep the edits. |
| RG-14 to RG-17 | Extended scenarios | not run | |

## Status of the 13 November observations

1. **Close project with an unsaved draft:** not re-tested in this run.
2. **Adding then removing a header left the draft unsaved:** fixed. The draft
   returns to **Saved** and nothing is written.
3. **First save rewrote formatting elsewhere in the file:** fixed. Saves in
   RG-05 and RG-08 each changed exactly one line.

## Observations (product)

1. **An imported project's `description` contains the absolute path of the
   source document.** The behavior is not new in this change, but it puts a
   local path into a file that is normally committed.
2. **Recent projects did not carry over from the previous build.** The app's
   bundle identifier changed with this build, so its stored state starts empty.

## Not covered

- The desktop error for files outside the project directory (new in PR #11).
- Windows and Linux.

## Verdict

**Pass** — all ten smoke scenarios and RG-13 passed; two of the three
observations from 13 November are confirmed fixed, and two new observations
are recorded above. The app log contained no panic or fatal error.
