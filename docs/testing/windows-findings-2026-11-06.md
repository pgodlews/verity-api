# Windows 11 findings — 6 November 2026

Evidence: supplied native desktop reports for published `v0.6.0-alpha.1`
(`bcb9656`) and a source build of `3f40d10`, Windows 11 x64 at 150% scale,
Wails v2.12.0 and WebView2 156.0.4314.58. This is a targeted pass, not completion
of the [Windows release plan](windows-11-test-plan.md). Raw logs, local paths,
profiles, environment values, and machine identifiers are not copied here.

| Finding | Native result before this change | Disposition |
| --- | --- | --- |
| Request save changed unrelated folded YAML text | Failed on published alpha; passed on main | Existing PR #1 fix needs release-candidate retest |
| Invalid load users erased the completed report | Failed on published alpha; passed on main, including export of the retained report | Existing PR #2 fix needs the remaining rejection variants tested |
| Git subprocess consoles repeatedly appeared and disturbed focus | Observed on both builds | Hide console windows while keeping a console available for graceful Git cancellation |
| Focus errors exited the process | Recorded on both builds; one during report export and one outside dialogs | Patch the pinned WebView2 focus path to ignore lifecycle-inappropriate focus and report recoverable HRESULTs without exiting |
| Git/workspace automated checks failed | Recorded on the Windows test machine | Correct drive-letter file URLs and platform-dependent fixture paths, line endings, modes, and Git commands; retain security and cancellation assertions |

The relationship between Git console activity and the focus error is unknown.
Console suppression alone is not evidence of a focus-crash fix. The local
[WebView2 compatibility patch](../../gui/third_party/go-webview2/VERITY.md)
handles the failing call independently and leaves fatal initialisation errors
unchanged. Its fake-COM tests do not establish native dialog or focus coverage.

## Required native retest

- Open a disposable Git-backed project, wait for status, edit/save a request,
  and confirm no console windows appear and state updates remain responsive.
- With unsaved edits, repeat Alt+Tab, minimise/restore, and native open/save
  dialogs, including cancel. Verify the process and edits survive.
- Cancel staging while a controlled slow clean filter holds the index lock;
  verify Git/filter processes exit, the lock is removed, and the index is restored.
- Open a changed file through the system handler from a path containing spaces
  and non-ASCII characters.
- Repeat the remaining security, credentials, import, Git, accessibility, DPI,
  and interruption scenarios in the shared and Windows plans. Previously
  unexecuted cases remain untested.

CI adds Windows Git/workspace and desktop backend checks, a GUI-subsystem process
launcher regression, focus recovery tests, and a desktop build. CI is automated
evidence and does not replace this native retest or grant release approval.
