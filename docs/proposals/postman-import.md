# Proposal: Postman collection importer

Status: implemented in `core/importers/postman.go` (`verity import postman`).

## Goal

Let people leave Postman without rewriting their collections by hand. Convert
a Postman **Collection v2.1** (and v2.0) export, plus optional Postman
**environment** exports, into a Verity project that `verity validate`
accepts — and say plainly what did not convert.

The collection format is plain JSON and publicly documented. The importer is
written from the public format docs and hand-authored fixtures only; no
Postman code or sample collections are copied. "Postman" is used nominatively.

## Structural mapping

Postman runs a collection top to bottom in one variable scope, so chained
requests (log in, capture a token, use it) are the norm. Verity property
transfers are **case-scoped**, so the mapping keeps each folder's requests
together in one case:

| Postman                               | Verity                                         |
|---------------------------------------|------------------------------------------------|
| collection                            | project (`name` = `info.name`)                 |
| requests at the collection root       | suite named after the collection, one case     |
| top-level folder                      | suite                                          |
| a folder's direct requests            | one case (steps in collection order)           |
| nested folder `A/B`                   | its own case `A / B` inside suite `A`          |
| request                               | `rest` step (`graphql` step for GraphQL bodies)|
| collection `variable[]`               | `default` environment variables                |
| environment export(s)                 | one environment each (first is `default`)      |

Collection variables are merged into every imported environment;
environment values win, matching Postman's precedence.

## Requests

- `{{var}}` becomes `${var}` everywhere (URL, headers, bodies, auth, vars).
  Supported dynamic variables become interpolation functions: `{{$guid}}` and
  `{{$randomUUID}}` → `${fn:uuid}`, `{{$timestamp}}` → `${fn:timestamp}`,
  `{{$isoTimestamp}}` → `${fn:isoTimestamp}`, and `{{$randomInt}}` →
  `${fn:randomInt}`. Any other `$…` dynamic variable is left as-is and warned.
- URL: `url.raw` is used verbatim; when absent it is rebuilt from
  protocol/host/path/query. Path variables `:id` become `${id}`; their values
  from `url.variable[]` become step `vars`.
- Headers: enabled headers only. Duplicate names keep the last value (warned).
- Bodies: `raw` → `body` (adds `Content-Type: application/json` when the raw
  language is JSON and no header is set); `urlencoded` → `form`;
  `graphql` → a `graphql` step (`query` + `variables`); `formdata` → REST
  `multipart` (enabled text rows become `{name, value}`, file rows become
  `{name, file}` from `src`, with `contentType` when set; a `src` array
  becomes one part per path; disabled rows are omitted; a file row with no
  `src` is warned and omitted, and the step is `skip: true` only when no
  usable part remains); `file` → `bodyFile` when `src` is set, otherwise
  `skip: true` and a warning. Paths are kept as written and read from the
  project directory at send time.

## Auth

Postman resolves auth request → folder → collection (`noauth` stops the
chain). The importer resolves that chain itself, then emits deduplicated
`auth[]` definitions and references them at case scope (the folder's
effective auth) or step scope (a request that overrides its folder).

Verity resolves auth definitions once per run, before any transfer runs. So
when a bearer token or API key is `{{var}}` and some test script captures
`var`, the importer emits it as a per-step header (or query parameter) such as
`Authorization: Bearer ${token}` instead — the classic "log in, then call with
the token" collection keeps working.

| Postman                      | Verity                         |
|------------------------------|--------------------------------|
| `noauth`                     | no auth (`inherit: false` when overriding a folder) |
| `basic`                      | `basic`                        |
| `bearer`                     | `bearer`                       |
| `apikey` (header/query)      | `apikey`                       |
| `oauth2` client credentials  | `oauth2_client_credentials` (alias of `oauth2` grant `client_credentials`) |
| `oauth2` `password_credentials` or `password` | `oauth2` with `grant: password` (`clientSecret` and `password` become `${env:…}`) |
| `oauth2` other grants (`authorization_code`, `implicit`, …) | `bearer` over the stored access token (warned) |
| digest, awsv4, hawk, ntlm, oauth1, edgegrid, ... | not converted (warned) |

## Secrets (secure by default)

Verity never stores plaintext secrets in project files, and the importer keeps
that true:

- Auth credentials (password, token, API key value, client secret) that are
  literals are replaced with `${env:NAME}` and a warning names `NAME`.
  Template values (`{{token}}`) are kept as `${token}`.
- Variables are replaced with `${env:NAME}` when Postman marks them `secret`,
  their key looks sensitive (password/secret/token/apikey), or the value looks
  like a credential (`secrets.LooksLikeSecret`).
- Literal `Authorization`, `Cookie` and API-key headers, and sensitive query
  parameters, are replaced the same way.
- JSON bodies and form fields (`urlencoded` and `formdata` text) replace
  literal credentials with `${env:NAME}`. The warning names the variable and
  does not echo the value.

Warnings name the variable to export; they never echo the secret value.

## Test scripts (best effort, honest)

Postman tests are JavaScript, which Verity does not run. The importer
recognises the common one-line idioms and converts them; every other script
line is counted and reported.

| Script idiom                                              | Verity                     |
|-----------------------------------------------------------|----------------------------|
| `pm.response.to.have.status(201)`                         | `status` eq 201            |
| `pm.expect(pm.response.code).to.eql(200)`                 | `status` eq 200            |
| `pm.response.to.be.ok`                                    | `status` eq 200            |
| `pm.response.to.be.success`                               | `status` gte 200, lt 300   |
| `pm.expect(pm.response.responseTime).to.be.below(500)`    | `response_time` lt 500     |
| `pm.response.to.have.header("X")` / `("X", "v")`          | `header` exists / eq       |
| `pm.expect(pm.response.text()).to.include("x")`           | `contains`                 |
| `pm.expect(json.a.b).to.eql(1)` / `.to.exist`             | `jsonpath` eq / exists     |
| `pm.environment.set("t", json.token)` (and collectionVariables, globals, variables, legacy `postman.setEnvironmentVariable`) | `transfers` (jsonpath) |
| `...set("t", pm.response.headers.get("X"))`               | `transfers` (header)       |

`json` is any variable assigned from `pm.response.json()` or
`JSON.parse(responseBody)`. The enclosing `pm.test("name", ...)` label becomes
the assertion description. A request with no converted assertions gets the
same starter assertion as the OpenAPI importer (`status lt 400`).

## Lossy-conversion report

Every one of these produces a warning with the Postman path
(`Folder/Sub/Request`), never a silent drop:

- pre-request scripts; test-script lines that did not convert; collection- and
  folder-level scripts;
- unsupported auth types and OAuth2 grants other than client credentials and password;
- a `formdata` file part or a `file` body with no `src`; duplicate headers or form keys;
- unsupported dynamic variables; removed secrets;
- a case that references a variable captured only by a *later* case. A capture
  that a later case uses is imported with `scope: run` instead (no warning).

Saved example responses (`item.response[]`) are imported as top-level `mocks`
for `verity mock`. The path is the example's original request URL, with a
variable that occupies a whole path segment rewritten to `{name}`. Literal
credentials in the example body or headers are replaced with `REDACTED` and
named in a warning; they are not stored as `${env:…}` and the original value
is not written. Descriptions, collection runner settings, the cookie jar, and
proxy and certificate settings are not imported (not warned per item). Postman
Collection v1 is rejected with a request to re-export as v2.1.

## Usage

```bash
verity import postman My.postman_collection.json --out project.yaml
verity import postman My.postman_collection.json staging.postman_environment.json \
  prod.postman_environment.json --out project.yaml --json
```
