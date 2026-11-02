# Proposal: SoapUI project importer (M1)

Status: implemented in `core/importers/soapui.go` (`verity import soapui`).

## Goal

Convert one SoapUI project file into a Verity project that `verity validate`
accepts, covering SOAP steps, REST steps, property transfers, and the common
assertions. Say plainly what did not convert.

`soap` stays the WSDL importer. The SoapUI kind is `soapui` only.

## Legal and trademark

SoapUI OSS is EUPL-licensed (copyleft). "SoapUI" is a SmartBear trademark.
This importer is a clean-room, read-only converter.

- No SmartBear source, `config.xsd`, generated classes, jars, or sample
  projects are copied, vendored, or transcribed. Go structs use the publicly
  visible element and attribute names only. Fixtures, including
  `examples/specs/sample.soapui.xml`, are hand-written.
- "SoapUI" is used nominatively ("imports SoapUI projects"). The package,
  binary, and command are not named after it. Nothing here states or implies
  affiliation or endorsement.
- Provenance, on `core/importers/soapui*.go` and in this document: Independent
  clean-room importer. Not affiliated with or endorsed by SmartBear. Reads
  SoapUI OSS (EUPL) project files; contains no SoapUI code.

Risk: a later change that pastes SmartBear schema or source into the tree
would undo the clean-room boundary. Review import diffs for that. Element
names and the configuration namespace are facts about the file format, not an
expression of the EUPL program. The header-setting key is recognized by the
suffix `@request-headers`, which is enough to read the escaped header list
without carrying a Java package name in the converter.

## Structural mapping

| SoapUI | Verity |
|---|---|
| `soapui-project` name | project name |
| project `property` | `default` environment variable |
| interface endpoint | `ENDPOINT`, then `ENDPOINT_1`, `ENDPOINT_2`, … referenced as `${ENDPOINT}` |
| `testSuite` | suite |
| `testCase` | case |
| `request` step | `protocol: soap` (`url`, `body`, `soapAction`, `version`) |
| `restrequest` step | `protocol: rest` (method, url, headers, query, body) |
| case / suite `property` | case / suite `vars` |
| `properties` step | `vars` on the next request step (they stay visible to later steps). A properties step after the last request becomes case variables. The warning names the earlier steps, which will see those variables |
| `transfer` step | `transfers` on the source step. Verity reads that step's response, so a transfer cannot be delayed until the transfer step. When another request sits between the source and the transfer, the warning names it |

An endpoint that is already a property reference (`${#Project#ENDPOINT}` →
`${ENDPOINT}`) is left as that reference, so it does not alias itself.
A literal endpoint that matches an environment value reuses that variable.

REST URLs are the endpoint, the interface `basePath`, and the resource path.
`{id}` in the path becomes the parameter value, or `${id}` when the request
does not supply one. Parameters declared on the interface, the resource, and
the method are included. A request parameter with the same name replaces the
inherited one, and an empty request value replaces it too. An inherited value
that is empty uses the parameter's default. The HTTP method comes from the
interface method, then from a method name that is already a verb, and
otherwise GET with a warning. If the resource or method does not match, the
inherited parameters are not applied and the warning says so.

SOAP `soapAction` comes from the step, then the addressing action, then the
interface operation's `action`. `soapVersion` `1_1` / `1_2` becomes `1.1` /
`1.2`.

Basic and preemptive credentials become a `basic` auth definition. The
password is always `${env:NAME}`. Other auth types, including Global HTTP
Settings, are not imported.

## Assertions

| SoapUI | Verity |
|---|---|
| Valid HTTP Status Codes (one code) | `status` `eq` |
| Valid HTTP Status Codes (a list) | `status` `matches` `^(200\|201)$` |
| Invalid HTTP Status Codes (one code) | `status` `ne` |
| Invalid HTTP Status Codes (a list) | one `status` `ne` per code, plus a warning |
| Response SLA | `response_time` `lte` (milliseconds) |
| XPath Match | `xpath` `eq`, or `exists` when the expected content is empty |
| JsonPath Match | `jsonpath` `eq`, or `exists` when the expected content is empty |
| Contains | `contains` |
| Not Contains | `contains` `not_exists` |
| Schema Compliance on SOAP | `xsd` (well-formedness only) |
| Schema Compliance on REST | `json_schema` when the definition is JSON or a file path. The file is not copied; the warning says to place it next to the generated project |

Verity's status assertion has no membership operator, so a list of acceptable
codes is a status `matches` against `^(200|201)$`. That pattern is evaluated
by the existing status assertion.

Contains with `useRegEx` becomes `regex`. Not Contains with `useRegEx` stays
a literal `not_exists` and warns. `ignoreCase`, wildcards, and
namespace-insensitive XPath are warned and not applied.

Anything else (script, JDBC, SOAP Response, …) is a warning. When none of a
step's assertions convert, the step is still imported.

## Expressions

`rewriteExpr` rewrites:

| SoapUI | Verity | Warning |
|---|---|---|
| `${#Project#name}` | `${name}` | no |
| `${#TestCase#name}` | `${name}` | no |
| `${#TestSuite#name}` | `${name}` | no |
| `${#Global#name}` | `${name}` | global properties become ordinary variables |
| `${#System#name}` | `${name}` | system properties become ordinary variables |
| `${#Env#NAME}` | `${NAME}` | not a process environment variable |
| any other `${#Scope#name}` | `${name}` | scope has no Verity equivalent |
| `${= ...}` | removed, including nested braces, quotes, and an unclosed tail | inline Groovy is not converted, and the text is not copied |

Property transfers read the source step's response. An XPath-shaped source
path (anything that is not empty and does not start with `$`) becomes
`from: xpath`. A path starting with `$` becomes `from: jsonpath`. An empty
path becomes `from: body`. The target property name is the variable. Targets
of `#TestSuite#`, `#Project#` or `#Global#` get `scope: run`, so later cases
see them. A transfer that would patch another request body becomes a variable
instead, with a warning. Source steps in another case are not linked.

## Secrets

Literal passwords, secret-named properties, values that look like credentials,
sensitive headers, sensitive query parameters, endpoint usernames and
passwords, sensitive endpoint query values, credential-looking assertion
expected values, and credential-shaped JSON or XML body fields become
`${env:NAME}`. XML element and attribute names cover the same set as JSON
(`password`, `secret`, `token`, `access_token`, `refresh_token`,
`client_secret`, `api_key`, and the same words with a hyphen or in camel
case). A value that still has literal text outside a `${...}` template,
including text that starts with `$` or `{`, is replaced wholesale. The
warning names `NAME` and does not echo the value. A value that is only a
template, such as `${#TestCase#token}`, stays `${token}`.

## What does not convert

Each of these is a warning with a SoapUI path (`suite/case/step` or
`project`), never a silent drop:

- Groovy, script, JDBC, datasource, JMS, mock-response, delay, and any other
  step type that is not a SOAP request, REST request, properties step, or
  transfer
- unsupported assertions
- mock services, load tests, security tests
- OAuth profiles and WS-Security configuration, when the container is non-empty
- auth types other than basic / preemptive
- matrix parameters, unresolved template parameters, and duplicate names
- setup and teardown scripts (project, suite, and case) and project settings
- request attachments and request settings other than headers
- properties and parameters that have a value and no name
- REST parameters inherited from the interface when the resource or method
  does not match
- Schema Compliance files (the path is kept; the file is not copied)

Disabled request steps are imported with `skip: true`. A project with no SOAP
or REST request is an `import_error`. A project file larger than 50 MB is an
`import_error` and is not parsed.

## Usage

```bash
verity import soapui project.soapui.xml --out project.yaml
verity import soapui project.soapui.xml --out project.yaml --json
```

`--json` includes the `warnings` array. The same converter backs the GUI
Import view and `verity gui-call ImportPreview`.
