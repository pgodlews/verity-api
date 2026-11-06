# Verity desktop UI release test plan

Version: 1.1 · Updated 6 November 2026 · Original test baseline: `cb8915c`

Use this as a living checklist for the **native Wails desktop application**. A browser-bridge or mock-mode result is useful for frontend debugging but does not replace a native result. Check each item only when observed; record pass, fail, blocked or not applicable, build/commit, OS/architecture, date, tester role or pseudonym, steps and evidence. An unchecked item below is a proposed future test, not a claim that it failed.

## What has actually been tested

The list below records the initial exploratory pass. The Apple-silicon follow-up
at the end supersedes its earlier coverage gaps where explicitly observed. For
Windows 11 and Linux, also execute the linked platform plans in [the index](README.md).

Native macOS 27.2, Apple silicon, local source build:

- Opened a demo project through the native file picker and reopened a recent project.
- Sent a local REST request (HTTP 200, 4/4 checks passed) and a local SOAP request (HTTP 200, 2/2 checks passed).
- Ran the demo collection without its test token: REST and SOAP passed; the secured request returned the expected HTTP 401 and failed its assertions. This is a negative test, not a successful authenticated native UI test.
- Previewed the bundled OpenAPI specification: three requests, no warnings. The final import/save flow was not tested in the native app.
- Ran a one-second, one-user load test against the local REST fixture; it passed with no request failures. Entering zero users correctly produced a validation message but removed the previous report.
- Edited and saved a request in a disposable Git repository, inspected its source-control diff, and reopened it. These actions revealed unrelated folded-description changes and inconsistent default selection of changed files.
- The native app was built and launched; the first packaging attempt was constrained by the environment. A later build succeeded with an extra linker flag; this does not establish clean installation or release packaging behavior.

Separate non-UI checks: the frontend production build and 82 frontend tests passed; the CLI demo passed eight assertions with a test token; GUI and plugin Go module tests passed. Root Go tests exposed the aliased-path Git failure. The browser development bridge was also smoke-tested, but its results are not native UI coverage. The [test report](findings-2026-11-06.md) contains observations and limitations.

This was a **targeted smoke and exploratory test**, not a full regression pass. No defensible percentage of feature, platform, state, or code coverage can be calculated from it. Major gaps include native import completion, authenticated requests, editing beyond one URL, environments, complete Git operations, error recovery, accessibility, cross-platform testing, and installation/update behavior.

## Release procedure and test setup

- P0 = every release, before shipping. P1 = full release regression and whenever the relevant area changes. P2 = scheduled / risk-driven or when the relevant capability changes. These priorities are proposed, not existing project policy.
- Test the actual release candidate with its commit, version and checksum. Record packaging, signing and any local build flags; distinguish an unsigned alpha build from a signed production build. Test only supported platforms/architectures for the release: macOS universal on both Apple silicon and Intel when available, Windows amd64, and Linux amd64 with the documented WebKit dependencies. Record OS versions and display scaling. Other targets are not implied supported.
- Use disposable project copies and Git repositories, temporary branches, test-only credentials, local fixture services, a disposable local or explicitly authorized test remote, and an isolated test keychain/account. Never use personal or production credentials. Confirm local server ports before running. Stop test servers afterward. Avoid remote load traffic unless the owner explicitly authorizes it.
- Prepare: bundled `examples/local-demo` fixture and mockserver; Petstore OpenAPI, calculator WSDL, sample Postman collection/environment, and SoapUI sample; a GraphQL fixture/schema; a local gRPC test server and installed first-party plugin if supported by the build; valid and invalid YAML/JSON projects, nested include fragments, feeds, non-ASCII and space-containing paths, folded and literal YAML scalars, large numbers, and symlink aliases; a local bare Git remote with clean, modified, conflicted and stash scenarios; a controlled auth server and fake credentials.
- Use one clean install/profile and one existing profile with recent projects and settings. Save test artifacts to a designated temporary folder. For each failure capture visible behavior, expected result, exact steps and sanitized logs; verify a reproducible failure before filing. Do not include secrets in screenshots or reports.
- Before release, triage P0 failures and security/data-loss issues; track P1/P2 failures with an owner and release decision. Retest fixes and affected neighboring flows. Verify previously reported failures explicitly; mark blocked or waived with a reason, never as passed.

## P0 — launch, project, basic request and persistence

- [ ] Install/extract the actual release artifact using its documented steps; start, quit and reopen the app. Verify version/architecture and correct native window. Record any unsigned-build platform prompts; do not bypass OS security warnings during automated testing.
- [ ] First-launch welcome screen, theme switch, and navigation controls render and respond; reopen and verify settings persistence where promised.
- [ ] Open a valid YAML project via the native picker; close/reopen via recent projects. Cancel the picker; try missing, unreadable, malformed and unsupported files and verify a useful error and recovery.
- [ ] Open a project path with spaces/non-ASCII characters and project includes. Verify correct project, environment and collection hierarchy; no unrelated files are altered.
- [ ] Send fixture REST request; verify URL/method, HTTP 200, response body/headers, four checks and captured value. Open the checks and captured tabs and compare with the fixture.
- [ ] Send fixture SOAP request and verify action, response and its two checks. Run the collection using a fake token available to the app process; confirm all cases, counts and statuses. Repeat with absent/wrong token and verify 401 and failed checks are accurately reported.
- [ ] In a disposable project, edit one request, confirm unsaved indicator, save, reopen and verify the edited value and that unrelated fields, comments and decoded scalar values remain unchanged. Compare both decoded project values and a Git diff.
- [ ] In a clean Git project, save a request and check its changed-file selection and semantic diff. Verify the folded-description preservation and newly changed project-file selection regressions in the report.
- [ ] Run a minimal local load test and save its JSON report; check counts and threshold status. Try invalid virtual-user input after a completed run: show validation, do not start traffic, and preserve the prior unsaved report and save button.
- [ ] With a symlink-alias path such as `/var` versus `/private/var`, use a disposable Git repo and fake credential to verify secret scanning prevents a selected project-file commit, leaves HEAD/index as expected, and hook installation recognizes the repository. Verify non-aliased path too. Do not place real secrets in the repo.

## P1 — workspace and request editor

- [ ] Expand/collapse suites and cases; filter requests with matches, no matches and clear; select requests and verify tab labels and editor content.
- [ ] Open multiple requests; edit each, switch tabs and return. Confirm independent drafts; close a dirty tab/project and exercise save, discard and cancel choices if offered. Verify saved and unsaved state is clear and data is not lost.
- [ ] Edit URL, HTTP method, params and headers; add/remove/disable rows, duplicate keys and empty values; save, reopen and verify exact values and ordering.
- [ ] Exercise body modes offered in the UI: none, JSON, text, form and applicable file/multipart controls. Validate malformed JSON, unicode, escaping, large numeric literals, `-0`, exponent notation and round-trip formatting without unintended numeric rounding.
- [ ] Exercise REST methods, redirects and timeout settings against a controlled local server; verify request captures reflect what was sent, not just the draft.
- [ ] Edit SOAP URL, action, version and envelope; save/reopen, send and verify headers, body and error display.
- [ ] Exercise GraphQL query/variables, schema selection/introspection and query-template insertion where exposed; test malformed schema/query, unreachable endpoint and large numeric values. Confirm no draft loss.
- [ ] If exposed for the release, test gRPC unary and streaming requests with the first-party plugin installed, missing, and failing; verify unsupported cases and errors are explicit.
- [ ] Add/edit/remove supported assertions: status, body, headers, JSONPath, XPath and schema; exercise pass, fail and invalid expressions. Verify expected/actual values and navigation to the correct request.
- [ ] Add/edit/remove captures and transfers; run dependent steps in the correct order, including missing captures and failed prerequisites. Check response JSON path assert/capture actions and copying paths.
- [ ] Exercise request variables, inherited environments and overrides, feeds where available, retries, timeouts and settings; verify per-step scope, invalid input and persistence.
- [ ] Verify keyboard shortcuts (save, send, search), focus order and disabled actions; saving with unchanged content should not create unintended project changes.
- [ ] Verify external file edits: clean tab refresh, dirty tab warning, reload versus keep edits, deleted/renamed step, stale save conflict, multiple open tabs and correct per-tab resolution. Do not overwrite external edits silently.

## P1 — environments, authentication and secret handling

- [ ] Create, edit, select and delete disposable environments and variables; validate duplicates, empty and invalid names; reopen and verify default selection, scoping and changes shown in Git.
- [ ] Verify literal credential detection for headers, query parameters, form/JSON fields, variables and auth settings. Save must be blocked with a useful field-level message; the value must not appear in errors.
- [ ] Replace a fake literal with an environment reference, preserving an authorization scheme, then save/reopen. Test absent and present test environment variables.
- [ ] Store a fake value in an isolated OS keychain, replace it with a reference, send a request, then remove the test entry. Verify invalid account, unavailable/locked keychain, store failure and existing-item replacement; no literal value is written into project files. Do not modify the user's existing keychain items.
- [ ] Test the auth types exposed by this release against local fixtures: none, basic, bearer, API key, Digest, mTLS, OAuth2, AWS SigV4 and HMAC where implemented. Verify inherited and overridden scopes, invalid settings and unsupported combinations. For mTLS and signatures use disposable certificates/keys only.
- [ ] In an isolated OAuth test setup, test sign-in, cancellation, invalid callback, expiry and sign-out; verify session is not shown to the frontend, persists only as documented, and reauthentication works. Do not approve real third-party access during a regression run.
- [ ] Verify secret redaction across request/response inspector, errors, run and load reports, captures, logs, Git diffs, clipboard actions and exports. Use only synthetic secret markers and confirm neither values nor sensitive account identifiers leak.

## P1 — collection runs and reports

- [ ] Run all, a selected suite/case and a single request; verify setup hooks, sequencing, counts, timing and the environment reflected in UI and emitted command.
- [ ] Cover assertion failure, transport error, timeout, malformed response, skipped prerequisite, retries, partial success and empty suite; distinguish failure from error and skip.
- [ ] Start a long local run and cancel; verify no duplicate run starts, state returns to idle, partial results are retained and accurately labeled. Test navigation while running and closing the app/project where supported.
- [ ] Filter all/passed/failed/error/skipped/aborted; expand results, inspect details, rerun failed, and open the right source request. Verify multiple failures and duplicate request names.
- [ ] Save/export every report format the desktop actually exposes; test file picker cancel, overwrite handling, invalid/unwritable path, successful export, re-opened contents, counts and redaction. Compare with CLI output where formats overlap.
- [ ] Test accept-actual-as-expected on a disposable project, including invalid/no actual value, save and diff; ensure unrelated assertions and files remain unchanged.

## P1 — imports

- [ ] OpenAPI 3.x/Swagger 2, WSDL, Postman collection plus environment, and SoapUI fixtures: choose through native picker, preview, inspect converted suites/requests/assertions/auth, warnings and YAML; import, choose output name/path, open the resulting project and run a relevant local case.
- [ ] For each importer, cover unsupported fields/features and invalid/partial documents; verify conversion warnings are complete and understandable, no unsupported behavior silently disappears, and no files are written by preview/cancel.
- [ ] Test empty/missing/unreadable files, wrong file type, invalid name, existing output and overwrite/cancel, folders with spaces/non-ASCII, and directory permission failures. Verify prior project and unsaved drafts remain intact.
- [ ] Import sample secrets; verify literals are not persisted, warnings/reference substitutions are correct and imported env values are not leaked.

## P1 — load testing

- [ ] Select all, one suite, one case and combinations; verify displayed CLI command and actual target selection. Exercise duration and iteration modes, boundary VU counts, ramp-up, think time and maximum durations.
- [ ] Check empty, zero, negative, fractional, malformed and over-limit values for each field; distinguish client validation from backend errors. The previously completed report must remain accessible when any preflight/start failure occurs.
- [ ] Test latency thresholds, multiple step-specific thresholds, percentile/operator/unit choices, invalid thresholds and error-rate bounds. Verify passing/failing results and summary match exported JSON.
- [ ] Exercise local/private target acceptance and public/redirect/proxy/other target refusal where applicable. Remote-target opt-in must be explicit per run and reset afterward. Use a controlled, authorized test service; never load test an arbitrary public address.
- [ ] Exercise live stats, cancel, partial report, run error, repeated start, navigation and app shutdown; confirm UI returns to idle and traffic stops. Save/reopen JSON and validate values/redaction.

## P1 — source control and hooks

- [ ] Open non-repo, clean repo, dirty repo, repo with includes, and project outside root. Verify branch/status, project versus other files, semantic and raw file changes, renamed/moved steps, comment-only changes and correct default selections.
- [ ] Select/deselect individual files and intentionally select none; refresh, add a new changed project file, remove/reappear a file, switch projects, and return. User choices must survive without suppressing intended defaults for new files.
- [ ] Test commit with selected project-only files and selected non-project files using disposable identity and repo; verify actual commit contents, index preservation and no changes to unselected paths. Exercise empty message/no selection, missing Git/identity, hooks and hook failure.
- [ ] Test secret scanning of selected staged data, includes, `.env` and `.env.example`, rename, symlink alias, partially staged files, unmerged entries and a scanner error. Fake secrets must never reach HEAD or logs; check index restoration after refusal.
- [ ] Test branch creation, switching, dirty and unsaved-draft warnings, detached HEAD and cancel. On a disposable local remote test fetch, fast-forward pull, diverged pull/merge, conflict, merge abort, push without upstream and its confirmation. Verify no force-push; test cancellation and progress.
- [ ] Create stashes for tracked changes, checked files and untracked files; inspect redacted diffs; apply, pop and drop with confirmation/cancel, conflicts and immutable IDs. Verify dirty draft protection and refresh of the correct tabs.
- [ ] Test hook install, re-install, unmanaged existing hook and backup protection, `core.hooksPath`, linked worktree, project fragments and symlinked paths. Test hook success/failure using fake values in a disposable repo.

## P2 — accessibility, visual quality and resilience

- [ ] Test keyboard-only navigation, focus visibility/order, dialogs and focus restoration, accessible names/roles for controls, screen-reader announcement of errors/progress/results and color-independent status indicators.
- [ ] Verify dark/light themes, standard and narrow/minimum window sizes, high-DPI scaling, long filenames/error messages, large collections, zoom and text clipping. Check native window controls and menu/keyboard behavior on each supported OS.
- [ ] Test app restart with recent projects/settings, a missing recently opened file, app closure during dirty drafts or running operations, failed local server, timeouts and recovery; verify no corrupt files and clear user feedback.
- [ ] Test large but reasonable projects/responses, many requests and long runs for responsiveness and cancellation. Record measurements and environment; don't infer production limits from a smoke test.
- [ ] Verify release-specific platform packaging, file picker, keychain/credential store, Git detection/PATH, file permissions and update/migration behavior on each supported OS. Test upgrade with a backed-up disposable profile and rollback where supported.
- [ ] Check that features described in the current release notes/docs actually exist in the desktop build; proposals or CLI-only capabilities must not be counted as tested desktop features.

## Suggested automation and release record

- Put deterministic domain, security, path, redaction and serialization regressions in Go/frontend tests; exercise the native shell and high-value user journeys through desktop UI automation. Keep a small local fixture with predictable responses and no real credentials.
- For each release record: version and commit; artifact/checksum; OS/arch; fixture and tool versions; pass/fail/blocked/not-applicable per scenario; bug links and disposition; security/data-loss decisions; known limitations; retest evidence; tester role or pseudonym and date.
- Keep PR validation separate from release evidence: a unit test or a passing mock bridge is not proof of a native desktop workflow. Re-run impacted scenarios after fixes and update this checklist when features change.

## Apple-silicon follow-up (6 November 2026)

A further native pass covered the major areas described in [the Apple-silicon follow-up report](macos-ui-followup-2026-11-06.md): authenticated demo, completed sample imports for OpenAPI/Postman/WSDL/SoapUI, environment creation and persistence, selected local Git workflows, bounded load-report export, basic keyboard/accessibility-tree checks, and launch plus smoke test of the checksum-verified published macOS alpha. These are partial scenario results; individual unchecked cases above remain untested unless explicitly listed as observed in that report. Windows and Linux remain separate passes.
