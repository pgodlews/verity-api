# Recipe: move a SoapUI project to Verity

Export is the project file itself (one XML document). Then:

```bash
verity import soapui examples/specs/sample.soapui.xml --out project.yaml
verity validate project.yaml
```

`soap` is the WSDL importer. A SoapUI project is `soapui` only.

Set `ENDPOINT` in the default environment to the service root before you run.
The sample's project property of that name is copied through as the variable.
Literal secrets are not copied: the sample's `password` property becomes
`${env:PASSWORD}`, and the warning tells you the name to export. It does not
print the value.

```bash
verity run project.yaml
```

## What you get

`examples/specs/sample.soapui.xml` is a hand-written fixture (not a SmartBear
sample). It becomes one suite and one case:

```yaml
environments:
  - name: default
    default: true
    variables:
      ENDPOINT: ""
      password: ${env:PASSWORD}
suites:
  - name: Smoke
    cases:
      - name: Lookup
        vars:
          sku: abc
        steps:
          - name: Get item
            protocol: soap
            request:
              url: ${ENDPOINT}/soap
              soapAction: urn:sample:getItem
              version: "1.1"
              body: <soap:Envelope ...><sku>${sku}</sku>...</soap:Envelope>
            assertions:
              - type: status
                op: eq
                value: 200
              - type: contains
                value: ok
              - type: contains
                op: not_exists
                value: ERROR
              - type: xpath
                expr: //id
                op: eq
                value: "42"
            transfers:
              - name: itemId
                from: xpath
                expr: //id
          - name: Fetch item
            protocol: rest
            request:
              method: GET
              url: ${ENDPOINT}/api/items/${itemId}
            assertions:
              - type: status
                op: eq
                value: 200
              - type: jsonpath
                expr: $.name
                op: eq
                value: widget
              - type: response_time
                op: lte
                value: 5000
```

`${#Project#name}`, `${#TestCase#name}`, and `${#TestSuite#name}` become
`${name}`. `${#Global#name}`, `${#System#name}`, and `${#Env#NAME}` also
become `${name}`. Those three are ordinary variables here, not process
environment variables, and the importer says so.

## Mapping

| SoapUI | Verity |
|---|---|
| test suite | suite |
| test case | case |
| SOAP `request` step | `protocol: soap` |
| `restrequest` step | `protocol: rest` |
| project property | default environment variable |
| interface endpoint | `ENDPOINT`, `ENDPOINT_1`, … |
| case / suite property | case / suite variable |
| properties step | variables on the next request. A properties step after the last request becomes case variables, and the warning names the earlier steps that will see them |
| property transfer | `transfers` on the source step. If a later request sits between the source and the transfer step, the warning names it: Verity applies the transfer when the source finishes |
| Valid HTTP Status Codes | `status` `eq`, or `matches` `^(200\|201)$` for a list |
| Invalid HTTP Status Codes | `status` `ne` (a list becomes one assertion per code) |
| Response SLA | `response_time` `lte` |
| XPath Match / JsonPath Match | `xpath` / `jsonpath` |
| Contains / Not Contains | `contains` / `contains` `not_exists` |
| Schema Compliance | `xsd` for SOAP (well-formedness), `json_schema` for REST when a schema is present. A file path is not copied; place it next to the generated project |
| basic / preemptive auth | `basic`, password as `${env:…}` |

A list of valid status codes uses `matches` because a status assertion cannot
say "one of" on its own. Several invalid codes become several `ne` assertions,
which is the same check, and a warning records the expansion.

## What does not convert

The importer prints a warning for each item, with the SoapUI path. Nothing in
this list is dropped quietly:

- Groovy and other script steps, JDBC and datasource steps, JMS, mock
  responses, delay steps, and any step type other than SOAP, REST, properties,
  or transfer
- setup and teardown scripts on the project, a suite, or a case
- project settings, request attachments, and request settings other than headers
- script assertions and every assertion type outside the table above
- mock services, load tests, and security tests
- OAuth profiles and WS-Security, when those containers have content
- auth other than basic / preemptive (Global HTTP Settings included)
- inline `${= ...}` Groovy, including nested braces and quoted text (removed,
  and not copied into the project or the warning)
- matrix parameters, properties or parameters with no name, and transfers
  whose source step is not in the same case
- REST parameters on an interface, resource, or method when that resource or
  method does not match the step. When it does match, the request's parameters
  override the method, which overrides the resource, which overrides the interface
- a Schema Compliance file path (the path is kept, the file is not copied)

A project file larger than 50 MB is rejected before it is parsed.

Literal passwords, secret-named fields, endpoint usernames, passwords, and
sensitive query values, credential-named XML and JSON fields (including
`access_token`, `refresh_token`, and `client_secret`), and credential-looking
assertion expected values become `${env:NAME}`. A value that mixes literal
text with a template is replaced wholesale. The warning names the variable
and does not print the value.

Disabled requests are kept with `skip: true`.

On the sample, expect warnings for the Groovy step, the mock service, the load
test, the security test, and `PASSWORD`.

`verity import soapui project.xml --json` puts the same strings in a
`warnings` array. The design and the clean-room boundary are in
`docs/proposals/soapui-import.md`.
