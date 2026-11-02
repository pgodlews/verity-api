# Authentication & client security

Auth in Verity is just another plugin: every provider implements the
`spi.AuthProvider` interface and is registered like any other extension. Two
layers compose, and providers attach at any scope with inheritance.

## Two composable layers

| Layer         | What it does                              | Built-in providers |
|---------------|-------------------------------------------|------------------|
| **Transport** | Configures TLS for the connection (mTLS, custom CA, TLS version/cipher control). | `mtls` |
| **Message**   | Mutates the outgoing request (headers, query, cookies, body). | `none`, `basic`, `digest`, `bearer`, `apikey`, `oauth2`, `aws_sigv4`, `hmac` |

Both can apply to a single call. The runner applies **transport providers
first** (they set up the TLS client), then **message providers** in declared
order. So a step can stack, e.g., `mtls` (transport) + `oauth2`
(message bearer).

## Declaring providers

Define named providers once under top-level `auth:`:

```yaml
auth:
  - name: client-cert
    type: mtls
    config:
      certFile: certs/client.crt
      keyFile:  certs/client.key
      caFile:   certs/ca.crt
  - name: api-oauth
    type: oauth2
    config:
      grant: client_credentials   # default; also password | refresh_token | authorization_code
      tokenUrl: "${BASE_URL}/oauth/token"
      clientId: "${env:OAUTH_CLIENT_ID}"
      clientSecret: "${env:OAUTH_CLIENT_SECRET}"
      scope: "orders:read"
```

## Attaching providers by scope

Reference providers by name with an `auth:` block at environment, suite, case,
or step scope:

```yaml
suites:
  - name: Secured
    auth:
      use: [client-cert, api-oauth]   # stack transport + message
    cases:
      - name: ...
        steps:
          - name: ...
            # inherits [client-cert, api-oauth] from the suite
```

### Inheritance & override rules

A scope's `auth` block has two fields: `use` (provider names) and `inherit`
(default `true`).

- **No `auth` block** → inherit the parent scope's effective providers unchanged.
- **`inherit: true`** (default) → **append** this scope's `use` to the inherited
  set (stacking).
- **`inherit: false`** → **replace** the inherited set with this scope's `use`
  (full override).
- **`inherit: false` with empty `use`** → **clear** auth ("no auth here"),
  useful to make one public endpoint inside an otherwise-secured suite.

Names are de-duplicated, preserving first-seen order.

Example: a mostly-secured suite with one public step.

```yaml
suites:
  - name: Mixed
    auth: { use: [api-oauth] }
    cases:
      - name: mixed
        steps:
          - name: secured call
            protocol: rest
            request: { url: "${BASE_URL}/me" }
          - name: public health check
            protocol: rest
            auth: { use: [], inherit: false }   # no auth on this step
            request: { url: "${BASE_URL}/health" }
```

## Provider configs

See `verity schema --kind auth` for the full JSON Schema. Summary:

### `none`
No configuration. Use to override an inherited provider with "no auth".

### `basic`
```yaml
config:
  username: "svc-account"
  password: "${env:SVC_PASSWORD}"
```

### `digest` (message)

```yaml
config:
  username: "${env:DIGEST_USERNAME}"
  password: "${env:DIGEST_PASSWORD}"
  # algorithm: SHA-256         # optional: pin the advertised algorithm
```

REST, SOAP, and GraphQL handle a Digest `WWW-Authenticate` challenge after a
401, then replay the request once. Request bodies, including uploads, are
replayed from the bytes already prepared for that request. A second 401 is a
normal response, so a status assertion can fail without turning it into a
transport error. The challenge replay is part of one step attempt and does
not consume the step retry budget. A later request reuses the cached challenge; `stale=true`
with a new nonce can trigger that request's single replay.

Supported algorithms are MD5, MD5-sess, SHA-256, and SHA-256-sess. The provider
prefers SHA-256 when offered alongside MD5, supports `qop=auth`, and supports
legacy challenges without qop. If a server offers both auth and auth-int,
Verity selects auth. An auth-int-only challenge, an unsupported algorithm,
or a challenge that conflicts with an algorithm pin returns `auth_error`.
Opaque is echoed. Nonce counts are synchronized across parallel cases and
cnonces use cryptographic randomness. Challenges stay in memory, scoped to
the provider and origin; passwords are registered literals. Digest parameters
are redacted with quoted-string-aware parsing. Each response hash is also held
only for its request attempt, so isolated echoes in captures and errors are
masked, then the hash is discarded instead of growing the run-wide redactor.

### `bearer`
```yaml
config:
  token: "${env:API_TOKEN}"
```

### `apikey`
```yaml
config:
  name: "X-API-Key"        # header/param/cookie name
  value: "${env:API_KEY}"
  in: header               # header (default) | query | cookie
```

### `mtls` (transport)
```yaml
config:
  certFile: certs/client.crt     # or certPEM: "-----BEGIN CERTIFICATE-----..."
  keyFile:  certs/client.key     # or keyPEM
  caFile:   certs/ca.crt         # or caPEM (custom CA bundle)
  minVersion: "1.2"              # 1.0 | 1.1 | 1.2 | 1.3
  maxVersion: "1.3"
  # insecureSkipVerify: true     # test-only; strongly discouraged
```
File paths resolve relative to the project directory. The keypair is validated
at load time so misconfiguration fails fast.

### `oauth2` (message)

`grant` selects the token request. `oauth2_client_credentials` is a
backwards-compatible alias of `grant: client_credentials` (the default when
`grant` is omitted). Every grant needs `tokenUrl` and `clientId`. Client credentials, password,
and refresh-token grants also require `clientSecret`. Authorization Code
supports public clients (no secret) and confidential clients. Put secrets in
`${env:VAR}`.

Client credentials (default):

```yaml
config:
  grant: client_credentials      # optional; this is the default
  tokenUrl: "${env:OAUTH_TOKEN_URL}"
  clientId: "${env:OAUTH_CLIENT_ID}"
  clientSecret: "${env:OAUTH_CLIENT_SECRET}"
  scope: "orders:read"
  authStyle: header              # header (HTTP Basic, default) | body
  refreshSkewSeconds: 30         # refresh this long before expiry
```

Resource-owner password:

```yaml
config:
  grant: password
  tokenUrl: "${env:OAUTH_TOKEN_URL}"
  clientId: "${env:OAUTH_CLIENT_ID}"
  clientSecret: "${env:OAUTH_CLIENT_SECRET}"
  username: "${env:OAUTH_USERNAME}"
  password: "${env:OAUTH_PASSWORD}"
  scope: "orders:read"
```

Refresh token (a token obtained out of band — there is no browser login):

```yaml
config:
  grant: refresh_token
  tokenUrl: "${env:OAUTH_TOKEN_URL}"
  clientId: "${env:OAUTH_CLIENT_ID}"
  clientSecret: "${env:OAUTH_CLIENT_SECRET}"
  refreshToken: "${env:OAUTH_REFRESH_TOKEN}"
  scope: "orders:read"
```

Authorization Code with PKCE:

```yaml
config:
  grant: authorization_code
  authUrl: "${AUTH_URL}"
  tokenUrl: "${TOKEN_URL}"
  clientId: "${OAUTH_CLIENT_ID}"
  # clientSecret: "${env:OAUTH_CLIENT_SECRET}"  # confidential clients only
  scopes: [orders:read]                         # or scope: "orders:read"
  redirectPort: 0                               # random available port
  refreshToken: "${env:OAUTH_REFRESH_TOKEN}"    # for CLI/CI; optional in GUI
```

In the desktop request editor's **Auth** tab, choose **Sign in** for the
effective authorization-code provider. Verity opens the system browser and
uses an S256 PKCE proof, independent random state, and a loopback callback
bound only to the IPv4 loopback interface. Register the callback path
`/callback` with your identity provider. If the provider requires a fixed
callback port, set `redirectPort` to that registered port. The flow times out
after two minutes; a denied authorization returns an auth error. The callback
page contains no authorization code or tokens.

**Signed in · Sign out** controls a session for this project, environment,
and resolved client configuration. Desktop runs reuse it, including token
refresh and rotation across runs. Signing out removes it from subsequent
runs; already-started runs can finish. Closing the app discards all sessions.
No access token or refresh token is displayed, returned to the frontend, or
written to disk or the project. The plain-browser development bridge cannot
keep sessions across its separate CLI processes; mock mode fakes sign-in.

CLI/CI never opens a browser. With `grant: authorization_code`, it sends a
refresh-token grant using `refreshToken`; missing or unresolved refresh-token
references produce an actionable auth error. For CI, obtain a refresh token
out of band through your identity provider's approved authorization flow or
administrative tooling, requesting offline access/consent as required by that
provider. Transfer it directly into the CI secret store, expose it as
`OAUTH_REFRESH_TOKEN`, and reference `${env:OAUTH_REFRESH_TOKEN}` above.
Verity's GUI session is deliberately not an export mechanism. If your provider
rotates or expires refresh tokens, update the CI secret through its approved
process; Verity retains rotated values only for the current process.
OIDC providers can supply the OAuth endpoints; Verity uses access tokens and
does not validate or expose ID-token identity claims.

The access token is cached **in memory only**, keyed to its expiry, and
refreshed before expiry (within `refreshSkewSeconds`, default 30). One
provider instance is shared across every step that references the same named
auth, so the cache is reused. When `scope` is set it is sent on every token
request, including a later refresh.

If a token response includes `refresh_token`, that value replaces the in-memory
refresh token and the next fetch uses the refresh-token grant. The rotated
value is registered with the redactor and is never written back to the project
file. A `refreshToken` left on a `client_credentials` or `password` config is
not sent unless the token endpoint actually returns one (or `grant` is
`refresh_token`). The password, the refresh token, the access token, the
client secret, and the HTTP Basic encoding of the client credentials are all
registered for redaction.

### `aws_sigv4` (message)

Signs the request after interpolation and after the body is final, including
a `multipart` or `bodyFile` upload. A retry builds a new request and signs it
again, so `x-amz-date` and the signature are regenerated for every attempt. List this
provider last when it is stacked with another message provider, so headers
added earlier are included.

```yaml
config:
  accessKeyId: "${env:AWS_ACCESS_KEY_ID}"       # optional; see below
  secretAccessKey: "${env:AWS_SECRET_ACCESS_KEY}"
  sessionToken: "${env:AWS_SESSION_TOKEN}"      # optional
  region: "${AWS_REGION}"
  service: execute-api                          # s3 | execute-api | ...
  # unsignedPayload: true                       # sign UNSIGNED-PAYLOAD
```

`region` and `service` are required. `service: s3` (any case) uses a single
URI encoding and does not remove dot segments. Every other service removes
dot segments and encodes a `%` that is already in the path, so `%20` becomes
`%2520`. Query names and values are encoded once, sorted, and signed. Raw query
bytes use the same escaping as the HTTP wire target before canonicalization.
Signed headers include `host`, `x-amz-date`, `x-amz-content-sha256`, and
`x-amz-security-token` when a session token is present, plus the other
request headers except `Authorization`.

When `accessKeyId`, `secretAccessKey`, or `sessionToken` is omitted, Verity
reads `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, or `AWS_SESSION_TOKEN`
from the process environment at the moment the provider is built. An explicit
value wins, including an empty `sessionToken`. There is no shared-credentials
file and no named profile. The secret access key, the session token, and each
request signature are registered with the redactor.

### `hmac` (message)

Generic HMAC for webhooks and partner APIs. The default message is the raw
body. A template may use `${method}`, `${path}`, `${query}`, `${body}`,
`${timestamp}` (Unix seconds), and `${nonce}`. Those placeholders are filled
when the request is signed, after project interpolation. Interpolation is
lenient, so an unknown `${method}` stays in the template; a project variable
of the same name would be replaced first, so avoid those names.

```yaml
config:
  secret: "${env:HMAC_SECRET}"
  algorithm: sha256          # sha256 (default) | sha1 | sha512
  header: X-Signature        # default
  encoding: hex              # hex (default) | base64
  prefix: "sha256="          # optional, prepended to the encoded MAC
  timestampHeader: X-Timestamp
  nonceHeader: X-Nonce
  message: "${method}\n${path}\n${timestamp}\n${nonce}\n${body}"
```

`${timestamp}` is the Unix time in seconds. It is also sent as
`timestampHeader` when that field is set. `${nonce}` is 16 random bytes,
hex-encoded, and is sent as `nonceHeader` when that field is set. Each
attempt, including a retry, uses a new timestamp and nonce and a new
signature. The secret and the signature are registered with the redactor.
`${query}` is the query exactly as it is sent, without `?`. Spaces, controls,
non-ASCII bytes, and other disallowed raw bytes are escaped first. When auth
adds query parameters, the whole query is re-encoded once, sorted by key, with
spaces as `%20`.

### Signed requests and redirects

A request signed by `aws_sigv4` or `hmac` does not follow redirects: the
signature covers the original URL and body, so the `3xx` response is the
step's result. Assert on it, or point the step at the final URL. GraphQL
introspection is signed again for its own body.

For every auth type, headers that auth adds (an API key, a bearer token, a
session token) are removed when a redirect leaves the original origin
(scheme, host and port), so they are never sent to another server.
Unknown keys under a built-in signer's `config` are rejected, so a misspelt
field cannot silently fall back to the environment.

## Roadmap (same interface)

- **Tier 2:** Digest and Authorization Code + PKCE, alongside password and
  refresh-token grants, are implemented above.
- **Tier 3:** AWS SigV4 (`aws_sigv4`) and generic HMAC (`hmac`) are
  implemented above. NTLM/Kerberos (SPNEGO) and custom pre-request hooks
  remain extension points. See `docs/extending-auth.md`.
- **Deferred:** all SOAP/WS-Security auth.

## Secret handling (required behavior)

1. **No plaintext secrets in project files.** Credential fields accept
   `${env:VAR}` and `${secret:provider:key}` references resolved at run time.
   Validation **warns** when a value looks like a committed secret. The
   desktop request editor marks a literal in a step being edited and refuses
   Save until it is an `${env:NAME}` or `${secret:keychain:NAME}` reference
   (`verity scan-secrets` checks the same rule before a commit).
2. **Redaction everywhere via one utility.** `core/secrets.Redactor` is the
   single authority. Every output path — console, logs, JSON/HTML/JUnit reports,
   and the GUI inspector — runs text through it. It masks:
   - registered secret literals (env-resolved values, OAuth tokens fetched at
     run time, configured passwords/keys),
   - `Authorization` header values, common secret JSON fields
     (`password`, `token`, `client_secret`, `api_key`, …), `Set-Cookie`/`Cookie`
     values, and PEM private-key blocks.
3. **Resolved values stay in memory.** OAuth tokens and resolved secrets are
   never written to project files or reports. Explicit keychain storage persists
   the value only in the OS credential store (see below).
4. **Process environment, including dotenv files.** `${env:NAME}` is read from
   the process environment at run time. `verity run --env-file FILE` loads a
   dotenv file into that environment before the run (repeatable; later files
   win). A variable that is already set in the process wins, including one set
   to empty, unless `--env-file-override` is given — a CI secret injected by
   the runner is not replaced by a file. Values are never printed and never
   written into the project or the report. A malformed line fails the run
   with exit code 3 and a `config_error` at `file:line`. See `docs/ci.md`.

A dedicated test (`core/runner` → `TestRedactionInReporters`) asserts that a
secret used for auth never appears in **any** reporter's output.

### OS keychain secrets

`${secret:keychain:NAME}` reads an account named `NAME` under the fixed service
`verity`. Use `${secret:keychain:project/NAME}` for an explicit project scope:
the slash is part of the account name, with no automatic path-derived scope.
This convention avoids name collisions; it is not an access-control boundary.
Names are 1–200 ASCII letters, digits, underscores, dots, or hyphens, optionally
separated by slashes. Each segment starts with a letter, digit, or underscore.
Stored values are opaque: `${...}` inside a keychain value is not expanded.
Resolved values are registered with the same redactor as environment secrets.
Successful reads are cached in process memory per account for 60 seconds, with
concurrent misses sharing one OS read. Set and delete invalidate that account;
errors are never cached. External changes become visible after cache expiry.

```bash
# Read an existing process variable through stdin; no value argument.
printf '%s' "$API_TOKEN" | verity secret set project/API_TOKEN --json
verity secret get project/API_TOKEN --json          # masked
verity secret get project/API_TOKEN --reveal        # plaintext; warns on stderr
verity secret rm project/API_TOKEN --json
verity secret list --json                          # unsupported; config error
```

`set` refuses an existing item unless `--force` is supplied. It accepts piped
or redirected stdin only, rejects input over 1 MiB, removes one trailing LF or
CRLF, then trims leading/trailing whitespace and refuses an empty result.
The desktop store applies the same trimming rule. Keychain values cannot start
or end with whitespace; whitespace within a multiline value is preserved.
There is no echoing terminal prompt and no secret value argument. `get` verifies
the item exists and prints `***REDACTED***` by default. `--reveal` deliberately bypasses masking
for that value, including with `--json`; its warning goes to stderr. Avoid it
in captured terminals or CI logs. `set` and `rm` output only the name/status.
The portable go-keyring API cannot list names; `list` reports unsupported
with `config_error` and exit 3 rather than pretending the store is empty.
There is no separate file containing an index of keychain items.

The backend uses macOS Keychain through `/usr/bin/security`, Linux Secret
Service over the session D-Bus, or Windows Credential Manager, without cgo.
On macOS, go-keyring v0.2.8 passes the password to `security -i` over stdin,
not in command-line arguments.
A missing item or a locked/unavailable store is a `config_error` naming the
key. Raw OS diagnostics and credential values are excluded from errors.
Credential-management commands exit 0 on success, 2 on usage errors, and 3
on configuration/store errors. Headless Linux and CI often have no store;
use `${env:NAME}` or `${secret:file:...}` there. No environment fallback is
silently applied to a keychain reference.

In the desktop request editor, choose an account name beside **Store in OS
keychain**. The action stores the credential, then rewrites the draft to its
reference.
An Authorization header keeps its scheme. A failed store leaves the draft
unchanged; save the request afterward to persist the reference. Existing items
with the chosen name require inline confirmation:
`Replace keychain item "project/TOKEN"?` with **Replace** / **Cancel** before storage.
Embedded credentials in URLs or XML/form text still require manual replacement.
`?mock` keeps its fake keychain only in memory and never accesses the OS store or browser storage.
