# Verity Apple-silicon desktop UI follow-up

> Historical observations against `cb8915c`, supplied with the original test materials.
> These findings are not a claim about the current release. See [fix tracking and required native retests](README.md#findings-and-retests).

**Date:** 6 November 2026\
**Platform:** macOS 27.2, Apple silicon\
**Revision:** `cb8915c`; source build with Go 1.26.10/Wails 2.12.0 plus published `v0.6.0-alpha.1` macOS archive.\
**Method:** Native Wails UI driven with computer control; disposable project copies, a local fixture API and a local bare Git remote. A fixture-only API token was supplied to the app process. No production account or real credential was used.

## Results

| Area | Observed result | Scope and limitations |
|---|---|---|
| Authenticated run | Pass | Fixture bearer token: full native collection passed 3 cases and 8 checks; individual secure request returned HTTP 200 and passed 2/2 checks. Wrong/missing token had previously produced the expected 401. Other auth types, keychain and OAuth are not covered. |
| OpenAPI import | Pass | Native file picker → preview → import created and opened Petstore with 3 requests. Conversion accuracy beyond the displayed sample and running imported calls were not independently checked. |
| Postman import | Pass with warnings | Collection preview showed 8 requests, unsupported script lines and 3 credentials to set; import opened the generated project. Three fake input credential literals were absent from the generated YAML. The separate Postman environment file and request execution were not tested. |
| WSDL import | Pass | Native preview reported 2 requests; import opened CalculatorService with two SOAP operations. Imported calls were not run against a WSDL-specific server. |
| SoapUI import | Pass with warnings | Preview reported 2 requests and listed unsupported mock, load, security and script features; import opened the generated project. The fake input password was absent from generated YAML. Imported calls were not run. |
| Environments | Pass, partial | Created `qa-test` by copying local values, added different `UI_TEST` values in two environments, and verified both persisted after app restart and project reopen. Rename, deletion, invalid inputs and external edits were not covered. |
| Credential editor | Pass, partial | A fake literal Authorization header triggered a warning and disabled Save. Replacing it with an environment reference retained the Bearer prefix and enabled saving. OS keychain and other credential fields were not covered. |
| Git | Pass, partial | In a disposable repository: stash, show diff, apply, select and commit a file, create/switch a branch, push and set upstream to a disposable local bare remote, fetch, and pull an up-to-date branch. The remote ref was independently verified. No conflict, divergence, real external remote, hook, pop/drop or cancellation scenario was tested through UI. |
| Load report | Pass, partial | Three REST iterations at one VU passed, with 3 requests and 0 failures; native Save JSON wrote a parseable report with 3 iterations and no fixture token. Other boundaries/cancellation are not covered. The previously reported report-loss-on-invalid-start bug remains. |
| Desktop basics and accessibility | Pass, partial | Command-K focused the labelled search field; Command-S saved a draft; switching tabs retained an unsaved draft; request filtering and light/dark theme switch worked. Controls and some status text were present in the macOS accessibility tree. This is not a full keyboard-only, VoiceOver, contrast, zoom, scaling or small-window audit. |
| Published macOS artifact | Pass, partial | Downloaded the GitHub `v0.6.0-alpha.1` universal ZIP and `SHA256SUMS`, verified the archive checksum, extracted the app, confirmed arm64 and x86_64 slices and an ad-hoc signature, launched on Apple silicon, reopened a recent project and ran the authenticated 8-check demo successfully. This was extracted under the workspace and downloaded with curl; it did not exercise Finder quarantine/Gatekeeper, copying to Applications, a clean profile, updates, or an Intel run. The local source-build bundle also passed `codesign --verify`. |

## Candidate issue: native confirmation is not visible under automation

With an unsaved request draft in the published macOS app, clicking the request's close-tab control left the tab and draft intact, but no confirmation dialog appeared in the captured accessibility tree or screenshot. Clicking **Close project and open another** while the same draft was dirty similarly left the project open with no visible dialog. The draft was not lost. In source, `gui/frontend/src/views/WorkspaceView.tsx:199–203` and `gui/frontend/src/App.tsx:286` both gate these actions on `window.confirm`. The macOS Wails delegate source in the installed v2.12.0 dependency has no obvious JavaScript-confirm-panel handler, but the cause is not established. A human-observed reproduction and checking the macOS Wails/WebKit confirmation behavior are still needed before treating this as a confirmed product defect; automation might affect native modal visibility.

## Existing confirmed issues

The previously reproduced YAML folded-description mutation on request save, loss of the previous load report on invalid start, and inconsistent source-control default selection are unchanged. The aliased-path Git secret-scan bypass was independently verified in a disposable repository with a fake credential. See [test report](findings-2026-11-06.md) and [fix tracking](README.md#findings-and-retests).

## Remaining macOS gaps

This follow-up reduces the major gaps but is **not a complete macOS release regression**. Remaining high-value work includes a real clean-profile install and upgrade using the official distribution path; Finder quarantine/signing prompts; end-to-end VoiceOver and keyboard-only accessibility; native OAuth/keychain flows using an isolated test account; successful runs of imported specs; complex Git conflicts/merge/abort and hook behavior; gRPC/GraphQL-specific flows; destructive/cancellation/error recovery cases; and larger window-size/scaling coverage. Windows and Linux are deliberately outside this pass.
