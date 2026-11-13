# Desktop release testing

Two kinds of testing live here. The [functional regression suite](regression-suite.md)
is a short check of core features to run after any major change. The release
plans below are the fuller checklists for a release candidate, including
packaging and platform integration.

These plans describe native desktop coverage. Automated Go/frontend checks and
browser bridge smoke tests supplement the plans; they do not establish native
Windows, Linux, or macOS results. Use the exact release candidate, record its
commit and artifact checksum, and mark each scenario **pass**, **fail**,
**blocked**, or **not applicable** with a reason. Unchecked scenarios are untested.

| Document | Purpose | Evidence status |
| --- | --- | --- |
| [Functional regression suite](regression-suite.md) | Short agent-executable check of core features on macOS, Windows 11, and Linux; run after major changes | Authored 13 November 2026; smoke set executed once on macOS, not on Windows or Linux |
| [macOS regression run, 13 November 2026](regression-results-2026-11-13-macos.md) | First smoke-set run, against PR #9 | Smoke set passed; three product observations |
| [Regression results template](regression-results-template.md) | Record for one regression run on one OS | Template |
| [Shared release plan](desktop-release-test-plan.md) | P0/P1/P2 scenarios across desktop features | Living checklist; includes historical macOS observations |
| [Windows 11 plan](windows-11-test-plan.md) | Shared scenarios plus Windows packaging, paths, WebView2, credentials, and accessibility | Authored 6 November 2026; native scenarios not executed in this work |
| [Windows 11 findings](windows-findings-2026-11-06.md) | Supplied native observations, fix scope, and required retests | Targeted published-alpha/main testing; patched native retest outstanding |
| [Linux plan](linux-test-plan.md) | Shared scenarios plus distribution/session, WebKitGTK, permissions, Secret Service, and accessibility | Authored 6 November 2026; native scenarios not executed in this work |
| [macOS follow-up](macos-ui-followup-2026-11-06.md) | Observed Apple-silicon native scenarios and gaps | Historical testing of `cb8915c` and `v0.6.0-alpha.1` |
| [Original findings](findings-2026-11-06.md) | Reproductions and limitations behind the fixes | Historical observations, not a current pass/fail record |

The original shared plan, findings, and macOS follow-up were supplied as testing
materials. They are preserved as historical evidence with updated relative links
and explicit baseline labels. The Windows 11 and Linux plans add proposed tests;
they do not invent test results or extend the project's supported platforms.

## Findings and retests

| Finding | Fix tracking | Required release retest |
| --- | --- | --- |
| Folded YAML values changed during request saves | [PR #1](https://github.com/pgodlews/verity-api/pull/1) | Save requests and environments; compare decoded values, comments, and markers |
| Rejected load start erased the previous report | [PR #2](https://github.com/pgodlews/verity-api/pull/2) | Keep and export the prior report after profile, threshold, target, or backend rejection |
| Newly changed project files were unchecked | [PR #3](https://github.com/pgodlews/verity-api/pull/3) | Clean-to-modified transition, explicit choices, disappearing files, and project switches |
| Directory aliases bypassed project recognition and commit safeguards | [PR #4](https://github.com/pgodlews/verity-api/pull/4) | Root/fragments through aliases, refusal with synthetic credentials, HEAD/index preservation, dirty switch, and hook lifecycle |

Automated regression coverage accompanies the fixes. Native retests on the
release candidate remain required. The historical native-confirmation observation
needs human confirmation; the material does not establish a general dialog bug.
Historical build observations are environment-specific. A subsequent local Wails
2.12.0 / Go 1.26.2 macOS build completed without the extra linker flag; that is
build evidence, not native workflow or clean-install coverage.

## Evidence and privacy

Use disposable projects, repositories, local fixture services, test-only accounts,
and runtime-generated credential markers. Keep `.env.example` values empty and
supply values to the test process; see [authentication](../auth.md). Use a tester
role or pseudonym instead of a personal name. Record versions and configuration
without copying usernames, personal paths, private hostnames, addresses, account
identities, or tokens. Review screenshots, logs, report JSON, diffs, and clipboard
captures before attaching them. No raw screenshots, logs, credential files, or
user profiles are included here.

For each result record scenario ID, candidate version/commit, artifact checksum,
OS/architecture, relevant tool versions, setup, steps, expected and observed result,
status, sanitized evidence, issue/fix link, tester role, and date. Record a waiver
or unsupported scenario explicitly. Never promote a CLI/mock result to a native
pass or infer a coverage percentage from a smoke test.

See [release packaging](../releasing.md), [desktop development](../../gui/README.md),
and [contributor checks](../../CONTRIBUTING.md) for the implemented build commands.
