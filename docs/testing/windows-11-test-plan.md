# Windows 11 native desktop test plan

Prepared 6 November 2026. **Status: proposed scenarios; not executed in this work.**
Run the [shared desktop release plan](desktop-release-test-plan.md) as well as the
Windows cases below. Record each case independently; a passing macOS or browser
bridge scenario is not Windows evidence.

## Candidate and isolated setup

Use the Windows amd64 desktop archive produced by the release workflow. Record
the Windows 11 edition/build, x64 architecture, display scale, WebView2 Runtime,
Git version, candidate version/commit, archive name, checksum, and signature
status. Additional architectures are not implied supported by this plan. Record
the exact artifact and execution environment for every failure.

Verify the archive against the release's `SHA256SUMS` before extraction. In
PowerShell, paths are variables supplied by the tester, not personal paths in
the record:

```powershell
Get-FileHash -Algorithm SHA256 -LiteralPath $ArchivePath
Get-AuthenticodeSignature -LiteralPath $AppPath
```

Compare the hash with the published entry. Record unsigned-alpha status honestly;
do not mark it signed or automate bypassing SmartScreen. Keep any required
interactive installation decision with the tester.

Use a disposable standard-user profile, project directory, repository identity,
and local bare remote. Use an isolated test Credential Manager entry and a
controlled OAuth fixture only when those scenarios are run. Start bundled local
fixtures on an available loopback port, configure the disposable project's
`BASE_URL`, and supply its fixture token to the app process at runtime. Never
save a token in the project or `.env.example`; start the app from the prepared
process so it inherits the test environment. Stop fixtures and remove only
credentials and files created by this test.

Run one clean profile and one existing disposable profile. For source builds,
follow [the GUI prerequisites](../../gui/README.md); record Go, Node, Wails,
WebView2 and compiler versions separately from downloaded-artifact testing.

## P0 — every release candidate

| ID | Steps | Expected result |
| --- | --- | --- |
| W11-01 | Extract the verified archive; launch from Explorer as a standard user; close and relaunch | Correct native window and architecture; usable UI; expected platform prompts recorded |
| W11-02 | Open a valid YAML project with the native picker; cancel another picker; reopen from recents | Correct project/environment; cancel retains current state; recents work after restart |
| W11-03 | Open disposable paths containing spaces and non-ASCII characters, with nested includes | Correct files load and save; no mojibake or unrelated file edits |
| W11-04 | Send fixture REST and SOAP requests, then run the authenticated demo; repeat without the fixture token | Expected bodies/statuses/check counts; authenticated baseline is 3 cases/8 checks; missing-token 401 is a negative result |
| W11-05 | Change one URL, save, restart and reopen; compare decoded YAML and Git diff; repeat an environment save | Only intended values change; folded/literal strings, comments, numeric literals and markers retain their meaning |
| W11-06 | Open a clean repo; save a request; open Source control; uncheck it and refresh | Newly changed project file starts checked; explicit uncheck persists |
| W11-07 | Finish a bounded local load run; set virtual users to zero; repeat with invalid threshold and backend rejection | No rejected start sends traffic; previous report and Save JSON remain available |
| W11-08 | Save a load report through the native picker, cancel another save, and retry an unwritable destination | Valid redacted JSON on success; cancel/error preserves report and recovers |
| W11-09 | Put a runtime-generated fake credential in a disposable selected root/fragment; attempt commit | Refusal before commit; HEAD unchanged; prior index restored; no credential in UI/logs |
| W11-10 | Repeat Git recognition, scanning, dirty-switch and hook cases through a directory alias | Same protections as the ordinary path; hook stores a repository-relative project path |

## P1 — Windows paths, shell, persistence and credentials

| ID | Steps | Expected result |
| --- | --- | --- |
| W11-11 | Use drive-letter case variants and a project subdirectory; select project and non-project changes | Stable project-relative paths, correct classification and selected-only commit contents |
| W11-12 | Use a disposable directory junction; use symbolic links when privileges permit | Consistent path identity and Git safeguards; record unavailable symlink privileges as blocked, never passed |
| W11-13 | Select a path on another drive or outside the repository for a Git operation | Clear refusal; no commit or writes outside the selected repository |
| W11-14 | Exercise CRLF and LF YAML/JSON projects with comments, includes and large numeric literals | Documented formatting changes only; decoded unrelated data remains equal |
| W11-15 | Remove a fragment, rename a request externally, make a file read-only, and hold an external edit against a dirty draft | Useful error/reload/conflict handling; no stale save or draft loss; repair and retry work |
| W11-16 | Close a dirty tab/project; operate every offered cancel/discard choice using mouse and keyboard | Confirmation is visible and accessible; cancel retains the exact draft; discard affects only the confirmed scope |
| W11-17 | Create, edit and reopen environments; test missing/present process variables after launching from the prepared shell | Correct values/defaults persist; environment references resolve according to the app process, not another shell |
| W11-18 | Store/retrieve/remove a generated value with an isolated Windows Credential Manager entry; test an existing test entry and failed store | Reference stored only after success; confirmation/error behavior is explicit; no real entries modified or identifiers leaked |
| W11-19 | Run fixture OAuth sign-in, cancel, invalid callback, expiry, sign-out and retry | Controlled browser/callback flow; no session credentials in UI, projects or reports |
| W11-20 | Native import preview/save/open for OpenAPI, WSDL, Postman plus environment, and SoapUI; cancel and invalid/unwritable output cases | Correct request counts and warnings; no preview/cancel writes; existing drafts retained; run a local imported case |
| W11-21 | Git create/switch/fetch/pull/push against a disposable local bare remote; no upstream, divergence, conflict, abort and cancellation | Correct prompts/progress/outcomes; no force-push; correct branch/index/draft protection |
| W11-22 | Selected-only commit, partially staged entries, failing hooks, missing identity, stash apply/pop/drop and conflicts | Exact committed blobs; restored index on refusal; stash confirmations/cancellation and immutable identities work |
| W11-23 | Run hook install/reinstall/uninstall with spaces/non-ASCII paths and a configured hooks directory | Relative project path quoted correctly; existing unmanaged hooks and backup protections retained |
| W11-24 | Open from Explorer and a prepared terminal with different PATH; test Git/plugin unavailable, installed and failing | Useful dependency diagnostics and recovery; absence never reported as success |
| W11-25 | Run collection retries/timeouts/cancellation; repeat start; switch views; inspect/save results | One active operation; accurate partial results; responsive cancellation; redacted output |

Use only a disposable sandbox share if network-share paths are intentionally in
scope. Record support and security restrictions; do not test a personal or
corporate share by default. Long-path and case-insensitive filesystem scenarios
must record the OS policy/filesystem settings rather than assume universal support.

## P2 — Windows accessibility, display and resilience

| ID | Steps | Expected result |
| --- | --- | --- |
| W11-26 | Keyboard-only navigation, Tab/Shift-Tab, Ctrl+S, Ctrl+K, Escape and native picker interaction | Visible focus, correct shortcuts, usable cancel and focus restoration |
| W11-27 | Inspect controls/results/errors with Windows Narrator and the accessibility tree | Useful names/roles, announced progress/errors, status understandable without color |
| W11-28 | Light/dark themes, 100/125/150/200% scale, narrow window, long labels and mixed-DPI monitors | Readable controls and dialogs; no clipping or unreachable actions |
| W11-29 | Sleep/resume or stop a local fixture during a run; test close/reopen after an interrupted operation | Clear failure/partial state, traffic stops, no corrupt project or locked index left behind |
| W11-30 | Upgrade/re-extract candidate using a backed-up disposable profile; reopen missing recent project | Documented settings migration, clear missing-file recovery; no claim of an updater unless provided |

## Evidence and exit criteria

Record results using [the shared evidence fields](README.md#evidence-and-privacy).
For screenshots redact user/profile names, title-bar paths, account names,
notifications, tokens and unrelated desktop content. Do not upload Credential
Manager exports or environment dumps. Run all applicable shared P0/P1 cases,
resolve release-blocking failures, document blocked/waived cases, and retest fixes
on the actual Windows candidate before declaring this platform ready. This plan
alone establishes no Windows release result.
