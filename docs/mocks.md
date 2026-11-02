# Mock services

`verity mock` stands up a fake HTTP API so a project can run before the real
service exists, or in CI without network access to it. The server lives in
`core/mock` and is shared by the CLI and tests. It binds IPv4 loopback unless
you pass an explicit `--host`.

```bash
verity mock examples/specs/petstore.openapi.yaml --port 0
# Copy the printed URL into BASE_URL in the other terminal.

# another terminal: point the imported project at the mock
verity import openapi examples/specs/petstore.openapi.yaml --out project.yaml
verity run project.yaml --var BASE_URL="$BASE_URL" --var petId=1
```

Ctrl-C stops the server and exits 0. `--port 0` asks the OS for a free port
and prints it in the URL. `--json` prints `{"ok":true,"url":"...","port":N,"kind":"..."}`
and then serves until interrupted.

Pass `--host` an IPv4 or IPv6 unspecified address to listen on all interfaces.
The process prints one warning on stderr; the listen line and JSON `url`
stay on a loopback address so the URL is dialable from the same machine.
Any other host is used as given. The default is IPv4 loopback. Read, write, and
idle timeouts are set on the listener. The read and write limits are the
longest `--delay` or `delayMs` plus 30 seconds.

Input kind is detected the same way an import source is recognized:

| Input | Detection |
|-------|-----------|
| OpenAPI 3.x JSON or YAML | top-level `openapi` starts with `3.` |
| WSDL 1.1 | XML root element `definitions` |
| Verity project | `schemaVersion`, `suites`, or `mocks` |

Swagger 2.0 is rejected. Give the server an OpenAPI 3 document instead.

`--delay MS` waits before every response, including a project mock's 404 and
413. A mock `delayMs` greater than zero replaces that wait for a matched
response only. `--seed N` fixes generated sample values (default `0`). The
same seed always produces the same body.

A file with `schemaVersion`, `name`, and `mocks`, and no suites, is a valid
`verity mock` input. `verity run` and `verity validate` still require at
least one suite.

## OpenAPI

Routes are the path templates in `paths`, plus the same paths under the first
server URL's path prefix (a `servers[0].url` whose path is `/v1` also
serves `/v1/pets` for `/pets`). A `{var}` in that URL is replaced with the
variable's `default` when it has one (`/v1/{version}` with default `2` also
serves `/v1/2/pets`). A static segment wins over a parameter when both match
(`/widgets/special` beats `/widgets/{id}`).

The status is the lowest exact `2xx` the operation defines, then a `2XX`
range (served as 200), then `default` (served as 200). `2XX`, `4XX`, and
`default` stay distinct from `200`, `400`, and `0`. `Prefer: code=N` selects
an exact code, then the matching range, then `default`. A document whose only
response is `default` answers 200, and `Prefer: code=200` selects that entry.
The body is the media type's `example`, or the first entry in `examples`
(document order). `Prefer: example=name` selects a named example. An
`externalValue` is read from a file next to the spec, and only from inside
that directory (no `..`, no symlink that leaves the directory, no URL).
Anything else is skipped with a warning on stderr and the server falls
through to `example` or a generated sample. When the spec has neither, the
server builds a sample from the response schema:

- types, formats, `enum`, `required`, `min`/`max`, `minLength`/`maxLength`
- `$ref`. At a cycle or the depth limit, optional properties are omitted,
  arrays are `[]`, and `null` is used only when the schema is nullable
- `allOf` is merged; `oneOf` and `anyOf` use the first alternative when
  generating. `--validate` requires `oneOf` to match exactly one branch and
  `anyOf` at least one
- arrays have 1 or 2 items, clamped by `minItems`/`maxItems` and by a hard
  cap of 32. Strings are capped at 1024 characters. Integer spans are clamped
  so extreme bounds cannot overflow
- property order does not affect the JSON (object keys are sorted)

The `Content-Type` is the spec's media type. `Accept` selects among them:
quality values are parsed, `q=0` is dropped, and the highest `q` wins (the
header order breaks ties). A response with no content is `{}` and
`application/json`.

An unknown method or path is `404` with `application/problem+json`.
`--validate` checks path, query, header, and cookie parameters and a JSON
body against the operation and returns `400` with an `errors` array. Query
arrays follow `style` and `explode` (the default is form + explode:
`?ids=1&ids=2`; a comma, space, or pipe split is used only when explode is
false). A body larger than 1MiB is `413`. An invalid `pattern` is a
validation error. A cyclic parameter schema fails validation instead of
crashing the process. Without `--validate` the server does not read or
check the request.

## WSDL

One endpoint serves every operation. Dispatch uses the `SOAPAction` header
(quotes stripped), or the SOAP 1.2 `action` parameter on `Content-Type`.
When that header is absent, the first child of `soap:Body` is matched by its
namespace and local name to the input message element, or by local name to
the operation.

The response is a SOAP envelope filled from the output message's XSD element
when the WSDL inlines a schema. Sample values are deterministic (`0` for
integers at `--seed 0`, `sample` for strings, the first enumeration). Repeated
elements follow `minOccurs` but are capped at 32 copies. At the nesting limit,
elements with `minOccurs="0"` are omitted and required elements are empty.
SOAP 1.1 answers `text/xml; charset=utf-8` with the SOAP 1.1 envelope
namespace. SOAP 1.2 (a `soap12:binding`, or a request whose `Content-Type` is
`application/soap+xml` when that binding exists) answers
`application/soap+xml; charset=utf-8` and the SOAP 1.2 envelope. When the
WSDL has both bindings, the request's content type selects which one matches.
An unknown operation is HTTP 500 and a SOAP Fault (`faultstring` `unknown
operation`, or a SOAP 1.2 `Code`/`Reason`).

## Project mocks

A project may declare responses the runner ignores and `verity mock` serves:

```yaml
mocks:
  - method: GET
    path: /widgets/{id}          # {id} matches one segment
    status: 200                  # omit for 200
    headers:
      Content-Type: application/json
    json:                        # or body: "raw text"; not both
      id: "1"
    delayMs: 20                  # overrides --delay when greater than zero
    match:                       # every set field must match
      query:
        active: "true"
      headers:
        X-Tenant: acme           # names are case-insensitive; values are exact
      bodyContains: widget
```

When several mocks match, the one with more static path segments wins, then
the one with more `match` fields. A tie keeps the earlier entry. A request
that matches nothing is a `404` problem document.

`verity validate` checks method, path, status, delay, and that `body` and
`json` are not both set. Included fragments append their `mocks` to the parent.

## Postman saved examples

`verity import postman` copies each `item.response[]` example into `mocks`.
The path comes from the example's original request URL. A path segment that
is only a `{{variable}}` or `:variable` becomes `{variable}`. Other variables
are rewritten to `${name}` the same way request URLs are.

Example bodies, headers, and path segments are scanned with the phase-1
secret scanner (`core/secretscan`), including URL userinfo and high-confidence
token prefixes inside free text. A literal credential is replaced with the
text `REDACTED` (not an `${env:…}` reference). A secret path segment becomes
the segment `REDACTED`. The import warning names the request item, not the
saved example's own name. The credential is not written into the project.
`REDACTED` itself is not reported as a secret on a later scan. `verity
scan-secrets` treats `mocks[].path` and header values as credential places,
so a value an import missed is still flagged.

## See also

- [Project format](project-format.md) — the `mocks` section in the schema.
- [CI](ci.md) — exit codes. A mock stopped with Ctrl-C exits 0, which is not
  the run interrupt code 5.
- [Import a Postman collection](cookbook/import-postman.md).
