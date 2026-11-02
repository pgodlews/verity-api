# Security policy

Verity is an alpha personal project maintained by Piotr Godlewski
(`@pgodlews`). Security reports are welcome, especially where Verity handles
credentials, untrusted projects, or repository content.

## Supported versions

During alpha, security fixes target only the latest released `0.x` minor
series, on its latest patch release. Older minor series and superseded patches
are unsupported. Please upgrade before checking whether a problem persists.

## Report a vulnerability privately

On the `pgodlews/verity-api` GitHub repository, open the **Security** tab and
choose **Report a vulnerability** to use GitHub private vulnerability
reporting. Never report a suspected vulnerability in a public issue or pull
request, or attach sensitive reproductions to public discussions.

Include, when possible:

- The affected Verity version, operating system, and component (CLI, desktop,
  core, or plugin).
- A description of the impact and the conditions needed to trigger it.
- Minimal reproduction steps and sanitized project or importer fixtures.
- Expected and actual behavior, with redacted diagnostics or report excerpts.
- Any suggested fix or mitigation, and whether you can help verify a patch.

Use `${env:NAME}` references and locally supplied values in reproductions.
Remove credentials, identifying server details, and personal filesystem paths
from attachments. Keep exploit details private until a fix and disclosure
plan have been agreed.

## Response expectations

The maintainer aims to acknowledge a report within one week. Investigation,
updates, fixes, and coordinated disclosure are best effort: this is a
one-person project without a dedicated security team or guaranteed response
or resolution time. If there is no reply after a week, follow up in the same
private report. Confirmed fixes will be documented in the changelog and, when
appropriate, a GitHub security advisory.

## Security scope

The following areas deserve particular scrutiny:

- **Secrets and redaction:** resolution of `${env:NAME}` and
  `${secret:provider:key}`, OS keychain storage and retrieval, and accidental
  disclosure through errors, logs, JSON/HTML/JUnit reports, load output, or
  the desktop GUI. Keychain account naming is not an access-control boundary.
- **Git integration:** repository-controlled programs such as filters, merge
  drivers, hooks, external diffs, and text conversion; selected-file commits;
  credential redaction; and safe handling of repository paths and refs.
  Verity must never force-push. See [Git integration](docs/git.md) for which
  Git programs are intentionally allowed and which are disabled or replaced.
- **Load testing targets:** `verity load` permits only loopback, private, and
  link-local destinations unless `--allow-remote` is explicitly set. Checks
  also apply when connections are dialed, including redirects, proxies, and
  dynamically resolved targets. Bypassing that default restriction is in scope.
- **Mock server exposure:** `verity mock` binds to loopback by default;
  listening on other interfaces requires an explicit `--host` choice.
- **Plugin process boundary:** plugins run as separate processes over VPP/1.
  That separation is not an OS sandbox; install only trusted plugins, which
  run with the user's permissions. Host-side protocol validation, lifecycle
  handling, and redaction failures are in scope.
- **Untrusted imports:** OpenAPI, WSDL, Postman, and SoapUI parsing, including
  resource exhaustion, unsafe file access, and credential disclosure in
  generated projects, saved mock examples, warnings, or errors.

See [authentication and secret handling](docs/auth.md) for the intended
credential behavior.

## Out of scope

- Running Verity against servers you do not own or have permission to test.
  Do not probe third-party systems to demonstrate a report.
- Issues in unpinned third-party services outside this repository's control.
