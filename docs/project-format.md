# Project format reference

A Verity project is a protocol-neutral, git-friendly description of API contract
tests. It is written in YAML or JSON and may be split across files. The
versioned JSON Schema is published via `verity schema` (and in
`core/schema/schema.go`); editors and AI agents can validate against it.

Use `verity fmt project.yaml` to normalize the root and all included fragments
to two-space indentation without sorting keys or changing values. GUI saves use
the same formatting. See [git.md](git.md) for check mode and Git automation.

## Top level

```yaml
schemaVersion: "1.0"        # required — pins the format version
name: My API Tests          # required
description: optional text
schema:                     # optional GraphQL SDL for every graphql step
  file: schema.graphql      # or introspect: true
include:                    # optional — merge fragment files (git-friendly split)
  - suites/users.yaml
  - suites/orders.yaml
environments: [ ... ]       # variable sets per deployment target
auth: [ ... ]               # named, reusable auth definitions
suites: [ ... ]             # required — the executable suites
mocks: [ ... ]              # optional — responses for `verity mock` only
```

### Model hierarchy

```
project
└── environments (dev/staging/prod variable sets)
└── auth (named providers)
└── mocks (optional responses for verity mock)
└── suites
    └── cases
        ├── feed (optional data-driven rows)
        └── steps
            ├── request (protocol-specific)
            ├── assertions
            └── transfers (capture values for later steps)
```

## Authentication configs

Named `auth` definitions include message providers `basic`, `digest`, `bearer`,
`apikey`, `oauth2`, `aws_sigv4`, and `hmac`, plus transport provider `mtls`.
OAuth2 grants are
`client_credentials` (default), `password`, `refresh_token`, and
`authorization_code`. The last uses PKCE for desktop sign-in and requires an
out-of-band `refreshToken` for CLI/CI. Desktop session tokens remain in memory
and are never part of this format. Digest handles one HTTP challenge replay,
including stale nonces on a later request. See [auth.md](auth.md) for fields,
algorithm support, inheritance, and secret references; `verity schema --kind
auth` publishes the configuration schema.

## Environments

```yaml
environments:
  - name: staging
    default: true            # used when --env is omitted
    variables:
      BASE_URL: "${env:BASE_URL}"
      TENANT: "acme"
```

Select at run time with `verity run project.yaml --env staging`. Override or
add variables with `--var KEY=VALUE` (repeatable). Variables interpolate as
`${NAME}` anywhere in request config and auth config.

### Variable precedence

When the same name is defined at more than one scope, later entries in this
list win over earlier ones:

1. Environment variables (`environments[].variables`)
   - then values captured with `scope: run` by cases that ran earlier
2. Suite variables (`suites[].vars`)
3. Case variables (`suites[].cases[].vars`)
4. Data-feed row columns (`feed`)
5. CLI `--var KEY=VALUE` overrides
6. Step-local variables (`steps[].vars`) and property transfers
   (`steps[].transfers`), applied as each step runs

So a CLI `--var` override always beats a value baked into the project file
(useful for pointing a run at a different endpoint in CI), but a step's own
`vars:` or a transfer captured from an earlier step's response can still
shadow it for that step onward, since those are the most specific, most
recently known values.

## Variables, interpolation, and secrets

- `${NAME}` — a plain variable (from environment/suite/case/step vars, feed
  columns, or property transfers). Falls back to an OS env var if unset.
- `${env:NAME}` — an OS environment variable, treated as **secret** (registered
  for redaction). Supply it through the process environment or
  `verity run --env-file FILE` (repeatable; later files win; a variable
  already set in the process wins unless `--env-file-override`). The file is
  not a project file and its values are never printed. See `docs/ci.md`.
- `${secret:provider:key}` — an external secret. Built-in providers: `env`
  (default), `file` (reads a file's contents, e.g. a Docker/K8s secret
  mount), and `keychain` (OS credential store, service `verity`).
  `${secret:keychain:project/NAME}` uses an explicit account-name scope; it
  is not derived from the project path. Keychain values are opaque, and a
  missing/locked/unavailable item is a `config_error`. See the CLI storage
  commands and platform requirements in `docs/auth.md`. Plugins can register more.
- `${fn:NAME}` — a dynamic, non-secret value generated at interpolation time.
  Every occurrence is evaluated independently, including occurrences nested in
  variable values. Unknown functions and invalid arguments are errors even in
  non-strict interpolation.

| Function | Result |
|----------|--------|
| `${fn:uuid}` | Random UUID v4 |
| `${fn:timestamp}` | Current Unix time in seconds |
| `${fn:timestampMs}` | Current Unix time in milliseconds |
| `${fn:isoTimestamp}` | Current UTC time in RFC 3339 format with millisecond precision |
| `${fn:randomInt}` | Random integer from 0 through 1000 |
| `${fn:randomInt:MIN:MAX}` | Random integer in the inclusive range `MIN`–`MAX` |
| `${fn:randomHex:N}` | `N` lowercase hexadecimal characters (`N` must be positive) |

**Never commit plaintext secrets.** Validation warns when a credential field
looks like a hard-coded secret (JWT/long token/PEM). The desktop request
editor runs the same check on an unsaved step and will not save while a
header, query parameter, body field, form field, or step variable is still
a literal; store it as `${env:NAME}` and set `NAME` on the machine or in an
untracked `.env` file (`verity run --env-file .env`). Alternatively, use
**Store in OS keychain** to store the value and replace it with
`${secret:keychain:NAME}`; the project keeps only that reference.

## Suites, cases, steps

```yaml
suites:
  - name: Orders
    vars: { CURRENCY: "USD" }       # suite-scoped variables
    auth: { use: [token] }          # suite-scoped auth (see auth.md)
    parallel: 4                     # per-suite case parallelism (optional)
    setup:   { steps: [ ... ] }     # run before cases
    teardown:{ steps: [ ... ] }     # run after cases
    cases:
      - name: create then fetch
        vars: { ... }
        continueOnError: true       # default true; false stops at first failure
        steps:
          - name: create order
            protocol: rest
            request: { ... }
            assertions: [ ... ]
            transfers: [ ... ]
```

### Step

```yaml
- name: create order
  protocol: rest               # rest | soap | graphql | grpc | <plugin>
  vars: { tmp: "x" }           # step-local variables
  skip: false                  # mark as skipped (reported, not executed)
  timeoutMs: 5000              # per-attempt HTTP timeout; overrides --timeout
  retry:                       # optional; see below
    count: 3                   # extra attempts after the first (0–20)
    delayMs: 200               # pause between attempts
    until: passed              # passed | status
  auth: { use: [extra], inherit: true }
  request:                     # protocol-specific (see below)
    method: POST
    url: "${BASE_URL}/orders"
    json: { item: "widget", qty: 2 }
  assertions: [ ... ]
  transfers: [ ... ]
```

### Retry and timeout

`timeoutMs` is the HTTP client timeout for **each attempt** of this step, in
milliseconds. When it is omitted or `0`, the step uses the run timeout
(`verity run --timeout`, default 30s). A retry does not share one budget:
every attempt starts the timeout over.

`retry` re-executes the request and re-evaluates assertions.

| field | meaning |
|-------|---------|
| `until: passed` | Retry while any assertion fails, or the attempt ends in a transport error (connection failure, timeout). |
| `until: status` | Retry only on a transport error, HTTP 429, or HTTP 500–599. A 4xx other than 429 is not retried, even when an assertion fails. A 429 or 5xx is retried even when every assertion would pass. |
| `count` | Extra attempts after the first. Total attempts are `1 + count`. `count` must be 0 through 20; a larger value is a validation error (`validation_error`). |
| `delayMs` | Pause between attempts, in milliseconds (`>= 0`). The pause ends if the run is cancelled (Ctrl-C), and the step is then reported as `transport_error` with message `retry delay cancelled`. |

Config errors (a missing upload, a bad request, a gRPC descriptor problem),
auth errors, and `plugin_error` (including the gRPC stub, when the plugin is
not loaded) are not retried. A failure while building the HTTP client, such
as a bad client certificate, is not retried either. A gRPC dial failure is a
`transport_error` and is retried; a gRPC status from the server is a response,
so `until: status` does not treat it as a transport failure.

Transfers run only on the final attempt, so a captured variable is never taken
from an earlier try. `durationMs` on the step is the total time, including
every attempt and the delays between them. The request and response captures
are from the final attempt; the response capture's own `durationMs` is just
that attempt. `attempts` on the step result is how many times the request
executed (omitted when the step was skipped or failed before an attempt).

A step that passes on a later attempt is a pass. With `--bail`, that case does
not stop the run. A step that is still failing or errored after its last
attempt does.

Cancelling the run (Ctrl-C, SIGTERM, or Cancel in the GUI) does not drop the
rest of the project. A step that has started finishes or fails on its own.
A step, feed row, or case that has not started is reported with status
`aborted`, and `summary.aborted` is true. The pause between retries still
ends on cancel; that step has started, so it stays a `transport_error`
(`retry delay cancelled`), not `aborted`. See `docs/ci.md` for exit code 5.

## Protocol request config

### REST (`protocol: rest`)
```yaml
request:
  method: GET|POST|PUT|DELETE|PATCH|...     # default GET
  url: "${BASE_URL}/path"
  headers: { Accept: application/json }
  query: { page: "2" }
  json: { ... }        # body marshaled as JSON (sets Content-Type)
  form: { a: "1" }     # url-encoded body
  body: "raw string"   # raw body
  # multipart and bodyFile are alternatives to json, form, and body. Use only one.
  multipart:           # ordered multipart/form-data parts, read when the request is sent
    - { name: note, value: "${NOTE}" }
    - name: file
      file: assets/widget.png   # relative to the project directory
      filename: widget.png      # optional; default is the file's base name
      contentType: image/png    # optional; default application/octet-stream
  bodyFile: payload.bin         # entire body is this file
```

In a URL query, spaces, controls, non-ASCII bytes, and `` `"<>\^`{|}` `` are
percent-encoded before sending and before any signer parses the URL. Existing
`%HH` escapes, `+`, `=`, `&`, `;`, and other bytes stay exactly as written.
Auth-added query parameters are merged and encoded as before; `aws_sigv4`,
`hmac`, and `digest` use the same escaped query that goes on the wire.

YAML `request.json` bodies and GraphQL `request.variables` retain JSON numeric
literals. YAML `<<` merge keys keep their normal semantics: explicit keys win,
and earlier maps in a merge sequence win over later ones. JSON project files
must contain exactly one JSON value; trailing content is an error.

Relative `file` and `bodyFile` paths resolve against the project directory.
Upload files must stay inside the project directory (checked after resolving
symlinks), so opening and running a downloaded project cannot send your local
files elsewhere; run with `--allow-external-files` to lift this for a project
you trust. A missing file fails the step with `config_error`. Text part values
never appear in reports — only part names and sizes. When `Content-Type` is absent, multipart requests get
`multipart/form-data` plus a boundary, and `bodyFile` gets
`application/octet-stream`. An explicit boundary is the one written into the
body; a multipart type that omits the boundary gets the generated boundary
added.

Captured requests do not contain file bytes or multipart text values: a part is
recorded as its field name, filename, and size. The upload body is built
before auth runs, so `aws_sigv4` and `hmac` sign the bytes that are sent.

### SOAP (`protocol: soap`)
```yaml
request:
  url: "${SERVICE_URL}"
  soapAction: "${SOAP_ACTION}"
  version: "1.1"        # or "1.2"
  # Either supply a full envelope:
  body: "<soap:Envelope>...</soap:Envelope>"
  # ...or have one generated from an operation + parameters:
  operation: Add
  namespace: "${SOAP_NAMESPACE}"
  parameters: { a: "3", b: "4" }
```

### GraphQL (`protocol: graphql`)
```yaml
request:
  url: "${BASE_URL}/graphql"
  query: "query($id: ID!){ user(id:$id){ name } }"
  variables: { id: "42" }
  operationName: "GetUser"   # optional
```

#### Schema

A GraphQL schema may be set on the project, a suite, or a step. A closer
scope replaces the parent. Set exactly one of `file` or `introspect`.

```yaml
schema:
  file: schema.graphql       # SDL, relative to the project directory
# schema:
#   introspect: true         # introspection query against the step URL
```

`file` is confined to the project directory the same way uploads are, including
symlinks, and `--allow-external-files` does not lift that. `verity validate --env NAME`
uses that environment (the default environment when `--env` is omitted). It
interpolates the query, `operationName`, and variables with the environment
and the suite, case, and step variables, the same non-strict way a run does,
then checks each graphql operation against the schema: unknown fields,
arguments, and fragments, wrong types, and missing required arguments. Text
that still contains `${...}` is left unchecked. The error names the step path
and says `line N, column M of the query` (positions in the query text). A
non-null variable with no default is reported on `request.variables` when
that key is absent. Other literal variable values are type-checked; a value
that still contains `${...}` is skipped. Introspection uses the step URL,
headers, and auth, including a desktop OAuth session. The result is cached
for that validate call or run, keyed by the URL and a hash of the auth
identity (Authorization, Cookie, auth query parameters, the client
certificate, and the auth provider names). The hash is not logged.
`verity graphql schema <project> --step NAME --env NAME --out schema.graphql`
writes the SDL so CI can pin a file and validate without the network.
Fetched SDL and introspection error text go through the redactor. Auth
headers are not written into errors.

```yaml
assertions:
  - type: graphql_schema
    config:
      allowErrors: false     # default: a non-empty errors array fails
```

`graphql_schema` checks that `data` matches the operation's selection set and
the schema types (nullability, lists, and scalars; a custom scalar accepts
any JSON value). `Int` must be an integer in the signed 32-bit range. A
numeric `ID` must be an integer of any size, kept exact. `Float` is any
finite number. A string `ID` is unchanged. Messages name a JSON path such as
`$.data.user.id`. `@skip` and `@include` are not evaluated, so a selected
field is always checked. An extra `__typename` is allowed. When the value is
an interface or union and `__typename` is absent, a selection that uses
member fragments fails with `__typename` needed to check an abstract type,
unless the object matches exactly one possible member (every non-null
selected field of that member is present and well typed, and every key
belongs to it). Fields selected on the interface itself are always checked.

`verity graphql template schema.graphql --operation Query.user --depth 2`
prints a query that selects scalar fields down to that depth and a variables
JSON skeleton. Required arguments become variables. A union or interface
field selects `__typename` and an inline fragment for each possible type,
up to 10 members; further members are omitted and the selection ends with
`# truncated`. The desktop GraphQL body tab offers the same operations as
**Insert from schema** when a schema is configured. Insert asks before
replacing a query or variables editor that already differs, keeps variable
values whose names match, and offers **Undo insert** for a few seconds.
`--watch` re-runs when a schema file changes.

### gRPC (`protocol: grpc`)

Unary and streaming calls need the first-party plugin `plugins/grpc` (see
`examples/grpc`). Without it, core returns `plugin_error`. The descriptor
(reflection v1/v1alpha or `descriptorSet`) determines the streaming kind.

```yaml
request:
  target: "${GRPC_TARGET}"             # host:port, or a unix socket URL
  method: "pkg.Service/Method"         # package.Service/Method
  message: { name: "widget" }          # JSON object, or a JSON string
  metadata: { authorization: "${env:API_TOKEN}" }
  plaintext: true                      # unencrypted; omit this and set tls for TLS
  # tls:
  #   insecure: false                  # true skips certificate verification
  #   caFile: certs/ca.pem             # PEM bundle inside the project directory
  #   serverName: name-on-the-certificate
  # descriptorSet: echo.pb             # FileDescriptorSet; else server reflection
```

`timeoutMs` is the deadline for the attempt. The reply body is JSON (proto
field names). `status` assertions compare the gRPC code number (`0` is OK, not
HTTP 200). `Grpc-Status` is that number and `Grpc-Message` is the status text;
response headers and trailers are headers too. A non-OK code from the server
is still a response. Dial failures are `transport_error`. A bad
`descriptorSet`, an unknown method, or the wrong message shape is
`config_error`. Metadata may hold tokens; it is captured with the request and redacted with
everything else. The plugin does not log it.

For server-streaming, keep `message` as above. Client-streaming and bidi use
`messages`, a list of JSON objects (empty is allowed), instead of `message`:

```yaml
request:
  target: "${GRPC_TARGET}"
  method: "pkg.Service/Collect"
  messages:
    - { name: first }
    - { name: second }
  stream:
    maxMessages: 1000                  # positive integer; default 1000
    idleTimeoutMs: 5000                # default 0 (off); receive inactivity
    sendIntervalMs: 10                 # default 0; delay between sends
    maxBytes: 16777216                 # default 16 MiB; total collected JSON
  metadata: { authorization: "${env:API_TOKEN}" }
assertions:
  - { type: status, value: 0 }
  - { type: jsonpath, expr: "$[0].name", value: first }
  - { type: header, expr: messageCount, value: "2" }
  - { type: header, expr: truncated, value: "false" }
```

Bidi sends the list in order, closes send, and collects responses in receive
order. Sending and receiving run concurrently for flow control; there is no
interactive scripting. An early server status can stop further sends.
`sendIntervalMs` delays between messages, never before the first. Setting both
`message` and `messages`, or using `messages` for unary/server-streaming, is a
`config_error`. Client-streaming and bidi require `messages`.

Every streaming body is a JSON array, including client-streaming's single
response or `[]` for no responses. JSONPath selects it directly, for example
`$[0].name`. The built-in JSONPath evaluator does not implement `$.length()`;
count with a `header` assertion on `messageCount`, or an installed array-count
assertion plugin. The synthetic response headers `messageCount` (retained
array length) and `truncated` (string `"true"`/`"false"`) survive VPP/1 and
report capture, overriding server metadata with those names.

Collection stops immediately at `maxMessages`, sets `truncated` to `"true"`,
and cancels the stream with status `0`. No extra receive probes for EOF, so
an exact-size reply also reaches the cap. Trailers are available only when
received before stopping. Assert `truncated` is `"false"` to require a complete
stream. A server error retains messages already collected and its gRPC code
and `Grpc-Message`.

The step deadline (`timeoutMs`) and idle timeout stop the RPC with code `4`
(`DEADLINE_EXCEEDED`) and the partial array. Idle timeout starts when the
stream opens and resets on each received response; sends do not reset it.
Context cancellation closes blocked sends/receives promptly; Ctrl-C remains
an interrupted run. Both millisecond options require nonnegative integers.
`maxBytes` must be at least `2` and caps the serialized array including commas
and brackets; exceeding it returns `transport_error` without a partial
response. The existing 32 MiB per-message receive limit also applies.

## Assertions

Each assertion has a `type` and type-specific fields. Common fields: `expr` (a
selector), `op` (comparison), `value` (expected), `description`.

| type            | expr            | op (default)     | notes |
|-----------------|-----------------|------------------|-------|
| `status`        | —               | `eq`             | HTTP status code; for gRPC, the gRPC code (`0` = OK) |
| `response_time` | —               | `lte`            | duration in ms |
| `jsonpath`      | JSONPath        | `eq`/`exists`    | JSON body |
| `xpath`         | XPath           | `eq`/`exists`    | XML body |
| `header`        | header name     | `eq`/`exists`    | response header |
| `contains`      | (or `value`)    | —                | substring in body |
| `regex`         | pattern         | —                | regex match on body |
| `json_schema`   | file path       | —                | validate body vs JSON Schema (`config.schema` for inline) |
| `graphql_schema`| —               | —                | response `data` matches the GraphQL selection set; `config.allowErrors` allows an `errors` array |
| `xsd`           | —               | —                | XML well-formedness (+ optional `config.rootElement`) |

Operators: `eq`, `ne`, `lt`, `lte`, `gt`, `gte`, `contains`, `matches`,
`exists`, `not_exists`.

`eq` and `ne` compare numerically only when at least one side is a number (a
JSON/YAML number, the status code, or `response_time`) and the other side
parses as one; two strings compare as text, so `"1.10"` is not `eq` `"1.1"`
and `"007"` is not `eq` `"7"`. `lt`, `lte`, `gt`, and `gte` also accept
numeric strings. Numbers compare exactly: response JSON keeps integers beyond
2^53 intact, so `9007199254740993` is not `eq` `9007199254740992`.

```yaml
assertions:
  - type: status
    value: 200
  - type: jsonpath
    expr: "$.items[0].id"
    op: exists
  - type: response_time
    op: lt
    value: 800
  - type: json_schema
    config:
      schema: { type: object, required: [id] }
```

## Property transfer

Capture a value from a response into a variable usable by later steps.

```yaml
transfers:
  - name: orderId         # variable name
    from: jsonpath        # jsonpath | xpath | header | status | body | regex
    expr: "$.id"
    default: "none"       # optional: used when extraction yields nothing
    scope: case           # case (default) | run
```

Then reference `${orderId}` in a subsequent step.

A transfer whose `jsonpath`, `xpath`, `header`, or `regex` source matches
nothing fails the step (a failed `transfer` result) unless it has a
non-empty `default`; an empty or omitted `default` means the value is
required. A source that matches an empty value captures `""` (or the default,
when one is set). JSON numbers are captured exactly as written in the
response, so a 64-bit id such as `9007199254740993` is not rounded; an object
or array is captured as compact JSON.

By default a captured value is visible to the later steps of the same case
only. With `scope: run` it is also visible to every case that **starts after
the capture**, in any suite, so one login suite can feed the rest of the run:

```yaml
suites:
  - name: login
    cases:
      - name: login
        steps:
          - name: token
            protocol: rest
            request: { method: POST, url: "${BASE_URL}/login" }
            transfers:
              - { name: token, from: jsonpath, expr: "$.access_token", scope: run }
  - name: orders            # suites run in order, so the token is known here
    cases:
      - name: list
        steps:
          - name: list
            protocol: rest
            request:
              url: "${BASE_URL}/orders"
              headers: { Authorization: "Bearer ${token}" }
```

Rules:
- Suites run one after another, so a capture in an earlier suite (or in a
  suite `setup` hook) is always seen by later suites. Between cases of a
  parallel suite the order is not fixed: capture in an earlier suite instead.
- In a later case the value sits between the environment and suite variables
  (see [Variable precedence](#variable-precedence)), so suite/case vars and
  `--var` overrides win over it.
- A value that looks like a credential is masked in output exactly as a
  case-scoped capture is.
- `--suite`/`--case` filters that leave out the capturing suite leave the
  value unset; pass it with `--var` or keep the login suite in the filter.

## Data-driven feeds

Repeat a case once per data row; each row's columns become variables.

```yaml
cases:
  - name: create many
    feed:
      type: csv           # csv | json | <plugin>
      path: data/widgets.csv
      # or inline:
      # inline:
      #   - { name: a, qty: "1" }
      #   - { name: b, qty: "2" }
    steps:
      - name: create
        protocol: rest
        request:
          method: POST
          url: "${BASE_URL}/widgets"
          json: { name: "${name}", qty: "${qty}" }
        assertions:
          - type: status
            value: 201
```

CSV: first row is the header by default (`config.header: false` to disable).
JSON: an array of objects; values are coerced to strings (`null` becomes an
empty string; nested objects and arrays become compact JSON text).

## Git-friendly split

Keep one root file with `include:` pointing at suite fragments. Fragments are
merged (their `suites`, `environments`, and `auth` are appended). This keeps
diffs small and avoids one giant file.

## Auth definitions

Named providers live under top-level `auth:` and are referenced by name
(`auth: { use: [name] }`). Built-in types: `none`, `basic`, `digest`, `bearer`,
`apikey`, `mtls`, `oauth2`, `aws_sigv4`, and `hmac`. `oauth2` takes
`grant: client_credentials` (the default), `password`, `refresh_token`, or
`authorization_code`. `oauth2_client_credentials` is an alias of the
client-credentials grant. `aws_sigv4` and `hmac` sign the finished request
and sign again on each retry. Field lists, caching, and secret rules are in
`docs/auth.md`. `verity schema --kind auth` prints the JSON Schema.

## Mocks

`mocks` is optional and is not executed by `verity run`. `verity mock
project.yaml` serves it. A file that has mocks and no suites is still
servable by that command; `verity run` and `verity validate` require
suites. Each entry is one response:

| Field | Required | Meaning |
|-------|----------|---------|
| `method` | yes | `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD`, or `OPTIONS` |
| `path` | yes | Path template. `{name}` matches one segment. Must start with `/`. |
| `status` | no | HTTP status 100–599. Omitted means 200. |
| `headers` | no | Response headers. Values are strings. |
| `body` | no | Raw response body. |
| `json` | no | JSON response body. Do not set `body` as well. |
| `delayMs` | no | Per-response delay. When greater than zero, overrides `verity mock --delay`. |
| `match` | no | Extra constraints. `query` and `headers` are string maps; `bodyContains` is a substring. Header names are case-insensitive. |

A static segment outranks a parameter. Among equal paths, the entry with more
`match` fields wins, then the earlier one. See [mocks.md](mocks.md).

Postman import fills `mocks` from saved example responses. A literal
credential in an example becomes the placeholder `REDACTED` and a warning,
never an `${env:…}` reference and never the original value.

## Importing

`verity import` builds a project from a contract or from another tool's file.
`soap` is an alias of `wsdl`, not a SoapUI project.

| Command | Source |
|---------|--------|
| `openapi`, `rest` | OpenAPI 3 or Swagger 2 |
| `wsdl`, `soap` | WSDL 1.1, one SOAP case per operation |
| `postman` | Postman Collection v2.0 / v2.1, plus optional environment files |
| `soapui` | SoapUI project XML: SOAP and REST steps, property transfers, common assertions |

The SoapUI importer is a clean-room reader. It is not affiliated with or
endorsed by SmartBear. It rewrites `${#Scope#name}` to `${name}`, replaces
literal secrets (including mixed literal/template values, endpoint user-info,
and credential-looking assertion values) with `${env:NAME}`, and lists
everything it did not convert. Project files larger than 50 MB are rejected.
See `docs/cookbook/import-soapui.md` and `docs/proposals/soapui-import.md`.

## See also

- `docs/auth.md` — the two-layer auth model, scopes, secret handling.
- `docs/ci.md` — running in CI, exit codes, Docker, GitHub Action.
- `AGENTS.md` — authoring/driving Verity programmatically.
