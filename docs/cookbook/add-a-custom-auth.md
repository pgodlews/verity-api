# Cookbook: add a custom auth provider

Full interface details: `docs/extending-auth.md`. For a real-world signer, see
`docs/cookbook/aws-sigv4.md`.

## Scaffold

```bash
verity plugin new auth myauth
cd myauth && go mod tidy && go test ./... && go build -o myauth .
```

The generated `main.go` adds a custom header from `config["value"]`:

```go
sdk.ServeAuth("myauth", func(config map[string]any) (spi.AuthProvider, error) {
    v, _ := config["value"].(string)
    return &MyauthAuth{value: v}, nil
})
```

## Use it

```yaml
auth:
  - name: custom
    type: myauth
    config:
      value: "${env:MY_SECRET}"     # resolved at run time, redacted everywhere
suites:
  - name: S
    auth: { use: [custom] }
    cases: [ ... ]
```

## Key rules

- Return `spi.LayerMessage` to mutate the request, or `spi.LayerTransport` to set
  `req.TLS`.
- Register secret material with the redactor (in-process factories receive the
  `*secrets.Redactor`; over the wire the host redacts resolved `${env:...}`
  values automatically).
- Cache tokens in memory and refresh before expiry; the resolver reuses one
  provider instance across steps.

## Stacking

Reference multiple providers: `auth: { use: [client-cert, custom] }`. Transport
providers apply before message providers. See
`docs/cookbook/mtls-and-oauth.md`.
