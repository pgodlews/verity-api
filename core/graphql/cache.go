package graphql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// Cache stores schemas for one validate call or one run. Introspection is
// keyed by a hash of the URL and the auth identity that can change the
// schema. The key is not logged. One in-flight fetch is shared per key, so
// a slow host does not block a different key. A failed fetch is not cached.
type Cache struct {
	mu       sync.Mutex
	files    map[string]*loaded
	urls     map[string]*loaded
	inflight map[string]*flight
}

type flight struct {
	done   chan struct{}
	schema *ast.Schema
	sdl    string
	err    error
}

type loaded struct {
	schema *ast.Schema
	sdl    string
}

// NewCache returns an empty schema cache.
func NewCache() *Cache {
	return &Cache{}
}

// Resolve returns the schema source that applies to a step. A step source
// replaces the suite source, which replaces the project source.
func Resolve(project, suite, step *model.GraphQLSchema) *model.GraphQLSchema {
	if step != nil {
		return step
	}
	if suite != nil {
		return suite
	}
	return project
}

// LoadFile reads and parses an SDL file inside dir. The parsed schema is
// cached by its resolved path.
func (c *Cache) LoadFile(dir, path string) (*ast.Schema, string, error) {
	abs, body, err := OpenConfined(dir, path)
	if err != nil {
		return nil, "", err
	}
	if c != nil {
		c.mu.Lock()
		if hit, ok := c.files[abs]; ok {
			c.mu.Unlock()
			return hit.schema, hit.sdl, nil
		}
		c.mu.Unlock()
	}
	schema, err := ParseSDL(string(body), abs)
	if err != nil {
		return nil, "", err
	}
	sdl := string(body)
	if c != nil {
		c.mu.Lock()
		if c.files == nil {
			c.files = map[string]*loaded{}
		}
		if hit, ok := c.files[abs]; ok {
			c.mu.Unlock()
			return hit.schema, hit.sdl, nil
		}
		c.files[abs] = &loaded{schema: schema, sdl: sdl}
		c.mu.Unlock()
	}
	return schema, sdl, nil
}

// Signer re-signs a request whose body or URL changed (aws_sigv4, hmac). It
// may set or replace headers on req.
type Signer func(ctx context.Context, req *spi.Request) error

// LoadIntrospection is LoadIntrospectionSigned with no signer.
func (c *Cache) LoadIntrospection(ctx context.Context, client *http.Client, req *spi.Request, responders []spi.ChallengeResponder, authNames []string, red *secrets.Redactor) (*ast.Schema, string, error) {
	return c.LoadIntrospectionSigned(ctx, client, req, nil, responders, authNames, red)
}

// LoadIntrospectionSigned fetches a schema and caches it for endpoint. req carries
// the URL, headers, query, and cookies already produced by auth. Those
// headers are sent and never written into the error text. authNames are the
// auth definition names and are part of the cache identity, not the log.
// When sign is set, the introspection request is signed again for its own
// body, because a signature made for the step's operation does not cover it.
func (c *Cache) LoadIntrospectionSigned(ctx context.Context, client *http.Client, req *spi.Request, sign Signer, responders []spi.ChallengeResponder, authNames []string, red *secrets.Redactor) (*ast.Schema, string, error) {
	if req == nil || req.URL == "" {
		return nil, "", verr.New(verr.CodeValidation, "introspection requires a step URL")
	}
	endpoint, err := withQuery(req.URL, req.Query)
	if err != nil {
		return nil, "", verr.New(verr.CodeValidation, "introspection URL is invalid")
	}
	if c == nil {
		return introspect(ctx, client, endpoint, req, sign, responders, red)
	}
	key := introspectionKey(endpoint, req, authNames)
	c.mu.Lock()
	if hit, ok := c.urls[key]; ok {
		c.mu.Unlock()
		return hit.schema, hit.sdl, nil
	}
	if f, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		if ctx == nil {
			<-f.done
			return f.schema, f.sdl, f.err
		}
		select {
		case <-ctx.Done():
			return nil, "", verr.New(verr.CodeValidation, "introspection request failed")
		case <-f.done:
			return f.schema, f.sdl, f.err
		}
	}
	f := &flight{done: make(chan struct{})}
	if c.inflight == nil {
		c.inflight = map[string]*flight{}
	}
	c.inflight[key] = f
	c.mu.Unlock()

	schema, sdl, err := introspect(ctx, client, endpoint, req, sign, responders, red)

	c.mu.Lock()
	if err == nil {
		if c.urls == nil {
			c.urls = map[string]*loaded{}
		}
		c.urls[key] = &loaded{schema: schema, sdl: sdl}
	}
	delete(c.inflight, key)
	f.schema, f.sdl, f.err = schema, sdl, err
	close(f.done)
	c.mu.Unlock()
	return schema, sdl, err
}

// authIdentity keeps the stable part of an Authorization value. A SigV4
// header changes with every request (date and signature), so only its
// credential scope identifies the caller; anything else is used whole.
func authIdentity(v string) string {
	if strings.HasPrefix(v, "AWS4-HMAC-SHA256 ") {
		if i := strings.Index(v, ", SignedHeaders="); i > 0 {
			return v[:i]
		}
	}
	return v
}

// introspectionKey hashes the URL and the auth material that can change the
// schema. The digest is the map key; the raw token is not stored.
func introspectionKey(endpoint string, req *spi.Request, authNames []string) string {
	h := sha256.New()
	write := func(s string) {
		_, _ = io.WriteString(h, s)
		_, _ = h.Write([]byte{0})
	}
	write(endpoint)
	if req != nil {
		if req.Headers != nil {
			write(authIdentity(req.Headers.Get("Authorization")))
			for _, v := range req.Headers.Values("Cookie") {
				write(v)
			}
		}
		if len(req.Cookies) > 0 {
			keys := make([]string, 0, len(req.Cookies))
			for k := range req.Cookies {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				write(k)
				write(req.Cookies[k])
			}
		}
		if len(req.Query) > 0 {
			keys := make([]string, 0, len(req.Query))
			for k := range req.Query {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				write(k)
				write(req.Query[k])
			}
		}
		if req.TLS != nil && len(req.TLS.ClientCertPEM) > 0 {
			sum := sha256.Sum256(req.TLS.ClientCertPEM)
			write(hex.EncodeToString(sum[:]))
		}
	}
	for _, name := range authNames {
		write(name)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ParseSDL parses an SDL document. name is used only in parser errors.
func ParseSDL(sdl, name string) (*ast.Schema, error) {
	if strings.TrimSpace(sdl) == "" {
		return nil, verr.New(verr.CodeValidation, "GraphQL schema is empty")
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: name, Input: sdl})
	if err != nil {
		return nil, verr.New(verr.CodeValidation, "invalid GraphQL schema: "+firstLine(err.Error()))
	}
	if schema == nil || (schema.Query == nil && schema.Mutation == nil && schema.Subscription == nil) {
		return nil, verr.New(verr.CodeValidation, "GraphQL schema has no query, mutation, or subscription type")
	}
	return schema, nil
}

// LoadExternal parses an SDL file the user named, or introspects an http(s)
// URL with no project auth. Project schema files use LoadFile instead, which
// stays inside the project directory.
func LoadExternal(ctx context.Context, ref string) (*ast.Schema, error) {
	lower := strings.ToLower(ref)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if ctx == nil {
			ctx = context.Background()
		}
		schema, _, err := introspect(ctx, &http.Client{Timeout: 30 * time.Second}, ref, &spi.Request{URL: ref, Method: http.MethodPost}, nil, nil, secrets.NewRedactor())
		return schema, err
	}
	if strings.Contains(ref, "://") {
		return nil, verr.New(verr.CodeValidation, "template source must be an SDL file or an http(s) URL")
	}
	body, err := os.ReadFile(ref)
	if err != nil {
		return nil, verr.New(verr.CodeValidation, "schema file cannot be read")
	}
	if len(body) > maxSchemaBytes {
		return nil, verr.New(verr.CodeValidation, "schema file is too large")
	}
	return ParseSDL(string(body), ref)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
