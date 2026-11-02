# Adding a custom auth provider

Auth providers implement `spi.AuthProvider` and are built by an
`spi.AuthFactory`:

```go
type AuthProvider interface {
    Type() string
    Layer() AuthLayer                                  // LayerMessage or LayerTransport
    Apply(ctx context.Context, req *Request) error     // mutate the request / set TLS
}

type AuthFactory func(config map[string]any, red *secrets.Redactor) (AuthProvider, error)
```

- **Message-layer** providers (`LayerMessage`) mutate `req.Headers`, `req.Query`,
  `req.Cookies`, or `req.Body`.
- **Transport-layer** providers (`LayerTransport`) set `req.TLS` (the runner
  merges it into the HTTP client).
- The factory receives the **interpolated** config (so `${env:...}` secrets are
  already resolved) plus `config["__dir"]` (the project directory, for
  file-based credentials).
- **Register every secret with `red.Add(...)`** so it is masked in all output.
- For token-based providers, **cache in memory** and refresh before expiry. The
  resolver shares one provider instance across steps, so your cache persists.

## HTTP challenge responders

An in-process message provider may additionally implement:

```go
type ChallengeResponder interface {
    RespondChallenge(ctx context.Context, req *http.Request, resp *http.Response) (bool, error)
}
```

The runner passes these optional providers through `ProtocolContext` to the
shared HTTP execution path used by REST, SOAP, and GraphQL. After a 401, the
provider inspects `WWW-Authenticate` and re-signs the exact wire request.
Return true for one replay, false to preserve the 401, or a structured
`verr.CodeAuth` error for an unsupported challenge. A second 401 is returned
normally. Bodies must be replayable through `http.Request.GetBody`; built-in
HTTP protocols prepare their bodies in memory. Cache challenges with a mutex,
limit cached credentials to the origin, and register secrets and computed
credentials with the redactor. See `core/auth/digest.go`.

The optional interface is not transmitted by VPP/1; existing auth plugins
continue to use `Apply`. Workspace OAuth sessions are explicitly injected
through `runner.Options.AuthSessions`, never through package globals.

## Reference: a custom HMAC request signer

Verity ships `type: hmac` (see `docs/auth.md`). The sketch below is the same
idea as an in-process provider, for when the built-in message template is not
enough.

This message-layer provider signs each request with an HMAC over the method +
path + timestamp and adds `X-Signature` / `X-Timestamp` headers.

```go
package hmacauth

import (
    "context"
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "net/http"
    "net/url"
    "strconv"
    "time"

    "github.com/pgodlews/verity-api/core/secrets"
    "github.com/pgodlews/verity-api/core/spi"
)

type signer struct{ keyID, secret string }

func New(config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
    s := &signer{
        keyID:  fmt.Sprint(config["keyId"]),
        secret: fmt.Sprint(config["secret"]),
    }
    if s.secret == "" {
        return nil, fmt.Errorf("hmac auth requires 'secret'")
    }
    red.Add(s.secret)            // ensure the signing key is redacted everywhere
    return s, nil
}

func (signer) Type() string        { return "hmac" }
func (signer) Layer() spi.AuthLayer { return spi.LayerMessage }

func (s *signer) Apply(_ context.Context, req *spi.Request) error {
    u, err := url.Parse(req.URL)
    if err != nil {
        return err
    }
    ts := strconv.FormatInt(time.Now().Unix(), 10)
    msg := req.Method + "\n" + u.Path + "\n" + ts
    mac := hmac.New(sha256.New, []byte(s.secret))
    mac.Write([]byte(msg))
    sig := hex.EncodeToString(mac.Sum(nil))

    if req.Headers == nil {
        req.Headers = http.Header{}
    }
    req.Headers.Set("X-Key-Id", s.keyID)
    req.Headers.Set("X-Timestamp", ts)
    req.Headers.Set("X-Signature", sig)
    return nil
}
```

Register in-process:

```go
reg.RegisterAuth("hmac", hmacauth.New)
```

Use in a project:

```yaml
auth:
  - name: signer
    type: hmac
    config:
      keyId: "my-key"
      secret: "${env:HMAC_SECRET}"
suites:
  - name: Signed
    auth: { use: [signer] }
    cases: [ ... ]
```

## As an out-of-process plugin

```bash
verity plugin new auth hmac
```

The scaffold's `main.go` calls
`sdk.ServeAuth("hmac", func(config map[string]any) (spi.AuthProvider, error) {...})`.
Note: over the wire, the host forwards `Layer()` and `Apply()`; the plugin
returns the mutated request and the host merges headers/query back.

## Transport-layer example

To contribute TLS (like mTLS), set `req.TLS` and return `LayerTransport` from
`Layer()`:

```go
func (p *myTLS) Layer() spi.AuthLayer { return spi.LayerTransport }
func (p *myTLS) Apply(_ context.Context, req *spi.Request) error {
    req.TLS = &spi.TLSConfig{
        ClientCertPEM: p.cert, ClientKeyPEM: p.key, CACertPEM: p.ca,
        MinVersion: tls.VersionTLS12,
    }
    return nil
}
```

## Stacking

Multiple providers compose on one call. The runner applies transport providers
first, then message providers in declared order. Example: `mtls` +
`oauth2` → mutual TLS connection carrying an OAuth bearer.

AWS SigV4 is built in as `type: aws_sigv4` (`docs/auth.md`,
`docs/cookbook/aws-sigv4.md`). NTLM/Kerberos (SPNEGO) and custom pre-request
hooks are still extension points on this same interface.
