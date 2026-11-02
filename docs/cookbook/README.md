# Cookbook

Copy-paste, runnable recipes. Each is self-contained and optimized for reading
cold (including by AI agents).

- `add-a-protocol.md` — a working REST-like protocol from scratch.
- `add-an-assertion.md` — a custom assertion + how to test it.
- `add-a-reporter.md` — a Markdown summary reporter.
- `add-a-custom-auth.md` — a header-signing auth provider.
- `aws-sigv4.md` — built-in AWS SigV4 (`aws_sigv4`) usage.
- `data-driven.md` — CSV/JSON feeds and inline rows.
- `property-transfer.md` — chaining steps by capturing response values.
- `mtls-and-oauth.md` — stacking transport + message auth.
- `import-postman.md` — moving a Postman collection (and environments) across.
- `import-soapui.md` — moving a SoapUI project (SOAP and REST steps) across.

For local credential storage, see [OS keychain secrets](../auth.md#os-keychain-secrets):
`verity secret set NAME` reads stdin, and `${secret:keychain:NAME}` keeps the
value out of project files. CI can continue using `${env:NAME}`.

See also the per-extension-point guides under `docs/extending-*.md` and
`AGENTS.md` for programmatic driving.
