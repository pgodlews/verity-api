# Linux native desktop test plan

Prepared 6 November 2026. **Status: proposed scenarios; not executed in this work.**
Run the [shared desktop release plan](desktop-release-test-plan.md) together with
the Linux cases below. Report the distribution/session actually tested; a pass
on one Linux desktop does not establish compatibility with all distributions.

## Candidate and isolated setup

Use the Linux amd64 desktop archive from the release workflow. Record candidate
version/commit and checksum, distribution/release, x86-64 architecture, desktop
session, X11 or Wayland, display scale, GTK/WebKitGTK versions, Git, and Secret
Service provider where tested. The current release build uses WebKitGTK 4.1 and
`-tags webkit2_41`; see [the release workflow](../../.github/workflows/release.yml)
and [GUI prerequisites](../../gui/README.md). A build on a CI runner is not proof
of native compatibility on a target distribution.

Verify the archive against its entry in `SHA256SUMS` before extracting. For
example, with the archive path provided as a shell variable:

```sh
sha256sum -- "$ARCHIVE_PATH"
```

Record the distribution-specific runtime packages and their source. Follow the
release documentation and the target distribution's package guidance instead of
assuming one package command works everywhere. Source builds need the documented
Go/Node/Wails versions and GTK/WebKitGTK development dependencies; with WebKitGTK
4.1 use:

```sh
cd gui
wails build -tags webkit2_41 -o verity-desktop
```

Use a disposable non-root desktop user/session, project directory, repository
identity and local bare Git remote. Use an isolated test Secret Service entry
and controlled OAuth fixture. A headless shell, container or session without a
native window/credential service must be recorded as a limited environment,
not a native pass.

Start bundled local fixtures on an available loopback port, set the disposable
project's `BASE_URL`, and supply runtime-only fixture credentials to the app's
launch process. Keep `.env.example` values empty. Use ordinary user permissions;
stop fixtures and remove only files, stashes and credentials created by the test.
Run both a clean profile and an existing disposable profile.

## Session matrix

| Dimension | Required record |
| --- | --- |
| Primary release environment | Exact distro/release and documented GTK/WebKitGTK runtime combination |
| Display server | One native Wayland session and one native X11 session where supported/available; record each separately |
| Credential service | Secret Service available/unlocked, locked/unavailable, and no session bus as distinct outcomes |
| Filesystem | Ordinary project directory; directory symlink alias; permission failures; record case behavior |
| Profile | Clean launch and upgrade/reopen with backed-up disposable settings |

Additional distributions, architectures, sandboxed package formats, or desktop
sessions are exploratory unless included in the release support policy. Do not
infer support from this matrix or from a downloaded binary launching once.

## P0 — every release candidate

| ID | Steps | Expected result |
| --- | --- | --- |
| LNX-01 | Verify/extract candidate as an ordinary user; inspect executable permission; launch, quit and reopen natively | Correct architecture and usable window; missing runtime/permission diagnostics are explicit |
| LNX-02 | Open a project with native picker; cancel another picker; reopen from recents after restart | Correct project/environment; cancel retains current state; usable recent-project recovery |
| LNX-03 | Use spaces/non-ASCII project paths and nested include fragments | Correct files load and persist; no unintended edits or path corruption |
| LNX-04 | Send fixture REST and SOAP, run authenticated collection, then repeat without the fixture token | Correct response/check counts; authenticated baseline 3 cases/8 checks; missing-token 401 recorded as a negative case |
| LNX-05 | Save one request and one environment edit; reopen and compare decoded YAML, JSON numbers and Git diff | Intended values change; unrelated folded/literal text, comments, markers and numeric literals retain meaning |
| LNX-06 | Open clean repo, save a request, inspect initial checkbox, deliberately uncheck and refresh | Newly modified project file starts checked; explicit choice survives refresh |
| LNX-07 | Complete bounded local load run, then reject zero VUs, invalid threshold, refused target and backend start | No rejected start sends traffic; previous report/save control survive; opt-in behavior remains explicit |
| LNX-08 | Save redacted report through native picker; cancel and retry unwritable destination | Parseable report on success; cancel/error preserves result; UI recovers |
| LNX-09 | Attempt selected root/fragment commit with generated fake credential through ordinary and aliased directories | Refusal before commit; HEAD/index retained; no credential in errors/logs |
| LNX-10 | Exercise dirty-switch guard and install/reinstall/uninstall hook through directory symlink | Correct recognition and relative paths; protection equivalent to ordinary directory |

## P1 — Linux paths, services, permissions and recovery

| ID | Steps | Expected result |
| --- | --- | --- |
| LNX-11 | Repeat launch, native picker, save dialog and project close on each recorded X11/Wayland session | Correct window/dialog ownership, focus and cancel behavior; session-specific failures recorded |
| LNX-12 | Launch without an available display or required GTK/WebKitGTK runtime in an isolated test environment | Diagnosable failure; no false claim of successful native launch or saved changes |
| LNX-13 | Test case-distinct filenames where supported, root/fragment aliases and a deleted nested directory | Correct project classification and Git identity; no accidental duplicate edit or loss of deleted-path handling |
| LNX-14 | Select outside-repository path or a directory symlink leading outside; retain a tracked leaf symlink entry | Outside paths refused; tracked-entry identity retained; staged project symlinks are not scanned as ordinary content |
| LNX-15 | Mark disposable project/output read-only; remove a fragment; edit externally while draft is dirty | Useful error/conflict, preserved drafts and disk data; permissions retained; repair and retry work |
| LNX-16 | Close a dirty tab/project; use cancel/discard with mouse and keyboard | Visible accessible confirmation; cancel preserves exact draft; explicit discard affects only confirmed scope |
| LNX-17 | Launch from terminal with fixture variables, and separately from supported desktop entry/launcher | Variable availability follows actual launch environment; absent values yield clear diagnostics without leaking identities |
| LNX-18 | Use a generated isolated Secret Service entry; store, resolve, replace with confirmation and remove it | Project stores a reference only after success; no real keyring entries touched; secrets redacted |
| LNX-19 | Lock/unavailable keyring, missing D-Bus session, denied store and nonexistent test entry | Useful failure; draft intact; no insecure plaintext fallback or false success |
| LNX-20 | Controlled OAuth sign-in/cancel/expiry/sign-out with configured external browser | Correct browser/callback/session handling and redaction; headless/browser failure explicitly recorded |
| LNX-21 | Native imports for all bundled formats, plus preview/cancel/malformed file/output permission errors | Warnings complete; preview/cancel write nothing; imported project opens and a relevant local case executes |
| LNX-22 | Selected commits, existing index entries, hooks failure/cancel, missing identity, branch switch and merge conflict | Exact selected blobs; restored index after refusal; dirty/external drafts protected; no force-push |
| LNX-23 | Fetch/pull/push and upstream prompt against disposable local bare remote; stop a running operation | Accurate progress, divergence/conflict/abort/cancel handling; no persistent index lock |
| LNX-24 | Stash checked/all tracked/untracked paths; inspect/apply/pop/drop and conflict/cancel | Correct selection, redacted diff, confirmations, immutable stash identities and retained conflicting stash |
| LNX-25 | Hook paths with spaces/non-ASCII, configured hooks directory, linked worktree, unmanaged hook and existing backup | Correct quoting, permissions and idempotence; foreign hooks/backup protections retained |
| LNX-26 | Git and gRPC plugin absent, available and failing under the real app PATH | Correct capability/error reporting; no unsupported desktop feature represented as tested |
| LNX-27 | Collection/load timeout, retries, cancel, repeated start, view changes and fixture outage | One active operation, responsive UI, accurate partial reports and stopped traffic |

## P2 — Linux accessibility, display and packaging

| ID | Steps | Expected result |
| --- | --- | --- |
| LNX-28 | Keyboard-only navigation, Ctrl+S/Ctrl+K, Tab/Shift-Tab, Escape and picker cancel | Visible focus, documented shortcuts, dialog focus restoration |
| LNX-29 | Inspect controls/errors/progress with Orca/AT-SPI where available | Useful names/roles and announcements; color-independent status; unavailable integration recorded as blocked |
| LNX-30 | Light/dark, desktop theme variants, fractional/high-DPI scaling, narrow window and long labels | Readable controls/dialogs and reachable actions; record session-specific scaling limitations |
| LNX-31 | Launch on recorded software/hardware rendering setup and resume from sleep during a local run | No blank webview or unrecoverable state; diagnostics sanitized; cancellation and recovery work |
| LNX-32 | Upgrade/re-extract using backed-up disposable profile; stale recent path; unwritable settings/output | Clear recovery, preserved project data and documented settings behavior; do not imply an automatic updater |

Do not run the app as root to hide a permission or keyring problem, weaken system
policy to make a test pass, or use a production keyring/account. Record desktop
security-policy denials as evidence and investigate them in the supported setup.

## Evidence and exit criteria

Use [the shared record fields](README.md#evidence-and-privacy). Sanitize home and
runtime paths, usernames, D-Bus/keyring account identifiers, environment values,
private network details and unrelated desktop notifications before publishing.
No keyring export, process-environment dump or personal screenshot belongs in the
release evidence. Execute applicable shared P0/P1 scenarios, triage blockers and
waivers, and retest fixes on the actual Linux artifact/session before declaring
it ready. Authoring this plan establishes no native Linux pass.
