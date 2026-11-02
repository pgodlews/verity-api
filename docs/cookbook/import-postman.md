# Recipe: move a Postman collection to Verity

Export the collection from Postman as **Collection v2.1** (and, optionally, each
environment), then:

```bash
verity import postman Shop.postman_collection.json \
  staging.postman_environment.json prod.postman_environment.json \
  --out project.yaml
verity validate project.yaml
```

The first environment becomes the default; pick others with `--env`.

## What you get

Using the sample in `examples/specs/sample.postman_collection.json`:

```yaml
suites:
  - name: Auth                       # top-level folder -> suite
    cases:
      - name: Auth                   # the folder's requests -> one case, in order
        steps:
          - name: Login
            request:
              method: POST
              url: ${baseUrl}/login  # {{baseUrl}} -> ${baseUrl}
              body: '{"username": "${user}", "password": "${password}"}'
            assertions:
              - type: status         # pm.response.to.have.status(200)
                description: Status code is 200
                value: 200
            transfers:
              - name: token          # pm.environment.set("token", jsonData.access_token)
                from: jsonpath
                expr: $.access_token
          - name: Me
            request:
              headers:
                Authorization: Bearer ${token}   # collection bearer {{token}}
```

## Read the warnings

The importer never drops anything silently. Typical output:

```
warning: environment Staging variable "password": secret value not imported; set environment variable PASSWORD
warning: auth "basic": literal password not imported; set environment variable BASIC_PASSWORD
warning: Auth/Me: 1 test-script line(s) not converted (first: console.log(body);)
warning: Widgets/Get widget: pre-request script not converted (1 line(s))
```

What to do with each:

- **Secrets** — export the named variables before `verity run project.yaml`.
  Literal secrets
  are never written to the project file.
- **Unconverted script lines** — rewrite them as assertions or transfers
  (see `property-transfer.md`), or drop them if they only logged.
- **Pre-request scripts** — usually set variables; use `vars:` on the step or
  case, or a `--var` on the command line.
- **Captures used by other folders** — Postman variables are global to a run.
  A value captured in one folder and used by a later one is imported with
  `scope: run` (see `docs/project-format.md`), so no change is needed. A folder
  that uses a value captured only by a *later* folder gets a warning: move that
  request earlier or set the value in the environment.
- **File uploads** — a `formdata` body becomes `multipart`, and a `file`
  body becomes `bodyFile`. The sample's Upload image step is:

  ```yaml
  - name: Upload image
    request:
      method: POST
      url: ${baseUrl}/widgets/42/image
      multipart:
        - name: file
          file: widget.png
  ```

  Relative paths are read from the project directory when the step runs.
  A file part with no `src` is a warning (the step is `skip: true` only when
  nothing usable remains). A `file` body with no `src` stays `skip: true`.

The full mapping table and the list of recognised `pm.*` idioms is in
`docs/proposals/postman-import.md`.

## Saved examples

Each saved example response becomes a `mocks` entry (see `docs/mocks.md`).
Serve them with `verity mock project.yaml`. A path segment that is only
`{{id}}` or `:id` becomes `{id}`. Every string copied onto a mock (path
segments, header values, and body text, including a JSON `url` field) is
scanned for credentials: secret-named keys, URL userinfo and credential
query parameters, and high-confidence token prefixes (`AKIA`/`ASIA`, `ghp_`,
`github_pat_`, `sk_live_`/`rk_live_`, Slack `xox*-`, `AIza`, a JWT, a PEM
private key). A hit is replaced with `REDACTED` and reported as a warning
that names the request item, not the example's own name. The credential is
not written into the project.
