# Cookbook: stacking mTLS + OAuth

A single call can carry **mutual TLS** (transport layer) **and** an **OAuth
bearer token** (message layer). Declare both providers and reference both.

```yaml
schemaVersion: "1.0"
name: secure-api
environments:
  - name: prod
    default: true
    variables:
      BASE_URL: "${env:BASE_URL}"

auth:
  - name: client-cert        # transport layer
    type: mtls
    config:
      certFile: certs/client.crt
      keyFile:  certs/client.key
      caFile:   certs/ca.crt
      minVersion: "1.2"
  - name: api-oauth          # message layer
    type: oauth2             # oauth2_client_credentials is the same grant
    config:
      grant: client_credentials
      tokenUrl: "${env:OAUTH_TOKEN_URL}"
      clientId: "${env:OAUTH_CLIENT_ID}"
      clientSecret: "${env:OAUTH_CLIENT_SECRET}"
      scope: "orders:read"

suites:
  - name: Secured
    auth:
      use: [client-cert, api-oauth]   # both apply; transport runs first
    cases:
      - name: list orders
        steps:
          - name: get
            protocol: rest
            request:
              url: "${BASE_URL}/orders"
            assertions:
              - type: status
                value: 200
```

## What happens at run time

1. The `mtls` provider sets the request's TLS config (client cert/key + custom
   CA). The runner builds an HTTP client with that TLS.
2. The `oauth2` provider fetches a token (cached in memory, refreshed before
   expiry) and sets `Authorization: Bearer …`.
3. The call goes out over the mutually-authenticated TLS connection carrying the
   bearer token.

Both the OAuth token and the client private key are redacted in every report
and in the GUI inspector.

## Password and refresh-token grants

The same `oauth2` provider accepts `grant: password` and
`grant: refresh_token`. Stack either one with `mtls` the same way (`use` lists
both names). Password needs `username` and `password`; refresh-token needs a
`refreshToken` you already hold (no interactive login). If the token endpoint
returns a new `refresh_token`, the next fetch uses it. That rotation stays in
memory.

```yaml
auth:
  - name: api-password
    type: oauth2
    config:
      grant: password
      tokenUrl: "${env:OAUTH_TOKEN_URL}"
      clientId: "${env:OAUTH_CLIENT_ID}"
      clientSecret: "${env:OAUTH_CLIENT_SECRET}"
      username: "${env:OAUTH_USERNAME}"
      password: "${env:OAUTH_PASSWORD}"
  - name: api-refresh
    type: oauth2
    config:
      grant: refresh_token
      tokenUrl: "${env:OAUTH_TOKEN_URL}"
      clientId: "${env:OAUTH_CLIENT_ID}"
      clientSecret: "${env:OAUTH_CLIENT_SECRET}"
      refreshToken: "${env:OAUTH_REFRESH_TOKEN}"
```

## Overriding per step

To drop OAuth on one public endpoint while keeping mTLS:

```yaml
steps:
  - name: public health
    auth: { use: [client-cert], inherit: false }   # replace suite set
    request: { url: "${BASE_URL}/health" }
```
