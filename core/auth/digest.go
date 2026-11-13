package auth

import (
	"context"
	"crypto/md5" // RFC 2617 compatibility.
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

type digestChallenge struct {
	realm, nonce, opaque, algorithm, qop string
	hasOpaque                            bool
	nc                                   uint32
}

// maxDigestNonces bounds the remembered nonce history per provider.
const maxDigestNonces = 256

type digestNonceKey struct {
	origin, realm, nonce, algorithm string
}

type digestProvider struct {
	username, password, algorithm string
	red                           *secrets.Redactor
	mu                            sync.Mutex
	challenges                    map[string]*digestChallenge
	nonces                        map[digestNonceKey]*digestChallenge
	// nonceOrder lists nonces oldest first so the map can be bounded.
	nonceOrder []digestNonceKey
}

func newDigest(config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
	p := &digestProvider{
		username:   strConfig(config, "username"),
		password:   strConfig(config, "password"),
		algorithm:  strings.ToUpper(strConfig(config, "algorithm")),
		red:        red,
		challenges: map[string]*digestChallenge{},
		nonces:     map[digestNonceKey]*digestChallenge{},
	}
	red.Add(p.password)
	if p.username == "" || p.password == "" {
		return nil, verr.New(verr.CodeConfig, "digest requires 'username' and 'password'")
	}
	if p.algorithm != "" && !digestAlgorithm(p.algorithm) {
		return nil, verr.New(verr.CodeConfig, "digest algorithm must be MD5|MD5-sess|SHA-256|SHA-256-sess")
	}
	return p, nil
}

func (*digestProvider) Type() string         { return "digest" }
func (*digestProvider) Layer() spi.AuthLayer { return spi.LayerMessage }

func digestAlgorithm(a string) bool {
	switch strings.ToUpper(a) {
	case "MD5", "MD5-SESS", "SHA-256", "SHA-256-SESS":
		return true
	}
	return false
}

func digestOrigin(u *url.URL) string {
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

func (p *digestProvider) Apply(ctx context.Context, req *spi.Request) error {
	u, err := url.Parse(spi.NormalizeTargetQuery(req.URL))
	if err != nil {
		return verr.New(verr.CodeAuth, "digest request URL is invalid")
	}
	u.RawQuery = spi.EscapeQuery(spi.MergeQuery(u.RawQuery, req.Query))
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.challenges[digestOrigin(u)]
	if c == nil {
		return nil
	}
	header, err := p.sign(ctx, c, method, u.RequestURI())
	if err != nil {
		return err
	}
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	req.Headers.Set("Authorization", header)
	return nil
}

// RespondChallenge caches a supported challenge and signs the exact wire URI.
// The HTTP protocol bounds this exchange to one retry, even for stale nonces.
func (p *digestProvider) RespondChallenge(ctx context.Context, req *http.Request, resp *http.Response) (bool, error) {
	if resp.StatusCode != http.StatusUnauthorized {
		return false, nil
	}
	var chosen *digestChallenge
	found := false
	for _, header := range resp.Header.Values("WWW-Authenticate") {
		for _, params := range digestChallenges(header) {
			found = true
			a := strings.ToUpper(params["algorithm"])
			if a == "" {
				a = "MD5"
			}
			if !digestAlgorithm(a) || (p.algorithm != "" && p.algorithm != a) {
				continue
			}
			qop := ""
			if params["qop"] != "" {
				for _, q := range strings.Split(params["qop"], ",") {
					if strings.TrimSpace(q) == "auth" {
						qop = "auth"
					}
				}
				if qop == "" {
					continue
				}
			}
			if _, ok := params["realm"]; !ok || params["nonce"] == "" {
				continue
			}
			opaque, hasOpaque := params["opaque"]
			c := &digestChallenge{
				realm: params["realm"], nonce: params["nonce"], opaque: opaque,
				algorithm: a, qop: qop, hasOpaque: hasOpaque,
			}
			if chosen == nil || strings.HasPrefix(a, "SHA-256") {
				chosen = c
			}
		}
	}
	if !found {
		return false, nil
	}
	if chosen == nil {
		return false, verr.New(verr.CodeAuth, "digest challenge requires a supported algorithm matching the configured override and qop=auth (auth-int is unsupported)")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	origin := digestOrigin(req.URL)
	key := digestNonceKey{origin, chosen.realm, chosen.nonce, chosen.algorithm}
	if previous := p.nonces[key]; previous != nil {
		chosen.nc = previous.nc
	}
	if _, known := p.nonces[key]; !known {
		p.nonceOrder = append(p.nonceOrder, key)
		// Keep the nonce counts of recent nonces (a server may return to
		// an earlier nonce, whose count must continue) but bound the
		// history so a long run does not keep every nonce ever issued.
		for len(p.nonceOrder) > maxDigestNonces {
			delete(p.nonces, p.nonceOrder[0])
			p.nonceOrder = p.nonceOrder[1:]
		}
	}
	p.nonces[key] = chosen
	p.challenges[origin] = chosen
	header, err := p.sign(ctx, chosen, req.Method, req.URL.RequestURI())
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", header)
	return true, nil
}

// sign is called with p.mu held. The attempt-scoped literal also masks an
// isolated echo of the response hash; structural masking covers the header.
func (p *digestProvider) sign(ctx context.Context, c *digestChallenge, method, uri string) (string, error) {
	if c.nc == ^uint32(0) {
		return "", verr.New(verr.CodeAuth, "digest nonce count exhausted")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", verr.New(verr.CodeAuth, "digest cnonce generation failed")
	}
	cnonce := hex.EncodeToString(b)
	c.nc++
	nc := fmt.Sprintf("%08x", c.nc)
	hash := func(s string) string {
		if strings.HasPrefix(c.algorithm, "SHA-256") {
			v := sha256.Sum256([]byte(s))
			return hex.EncodeToString(v[:])
		}
		v := md5.Sum([]byte(s))
		return hex.EncodeToString(v[:])
	}
	ha1 := hash(p.username + ":" + c.realm + ":" + p.password)
	if strings.HasSuffix(c.algorithm, "-SESS") {
		ha1 = hash(ha1 + ":" + c.nonce + ":" + cnonce)
	}
	ha2 := hash(method + ":" + uri)
	response := hash(ha1 + ":" + c.nonce + ":" + ha2)
	if c.qop != "" {
		response = hash(ha1 + ":" + c.nonce + ":" + nc + ":" + cnonce + ":" + c.qop + ":" + ha2)
	}
	p.red.AddTemporary(ctx, response)
	quote := func(s string) string {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	algorithm := strings.Replace(c.algorithm, "-SESS", "-sess", 1)
	header := "Digest username=" + quote(p.username) + ", realm=" + quote(c.realm) +
		", nonce=" + quote(c.nonce) + ", uri=" + quote(uri) + ", response=" + quote(response) + ", algorithm=" + algorithm
	if c.hasOpaque {
		header += ", opaque=" + quote(c.opaque)
	}
	if c.qop != "" {
		header += ", qop=auth, nc=" + nc
	}
	if c.qop != "" || strings.HasSuffix(c.algorithm, "-SESS") {
		header += ", cnonce=" + quote(cnonce)
	}
	return header, nil
}

// digestChallenges parses auth parameters, including quoted commas and escapes,
// and tolerates other authentication schemes on the same header line.
func digestChallenges(header string) []map[string]string {
	var out []map[string]string
	var current map[string]string
	for len(header) > 0 {
		header = strings.TrimLeft(header, " ,\t")
		i := strings.IndexAny(header, " =,\t")
		if i < 0 {
			break
		}
		key := header[:i]
		header = strings.TrimLeft(header[i:], " \t")
		if !strings.HasPrefix(header, "=") {
			current = nil
			if strings.EqualFold(key, "Digest") {
				current = map[string]string{}
				out = append(out, current)
			}
			continue
		}
		header = strings.TrimLeft(header[1:], " \t")
		var value strings.Builder
		if strings.HasPrefix(header, `"`) {
			header = header[1:]
			for len(header) > 0 {
				ch := header[0]
				header = header[1:]
				if ch == '"' {
					break
				}
				if ch == '\\' && len(header) > 0 {
					ch = header[0]
					header = header[1:]
				}
				value.WriteByte(ch)
			}
		} else {
			j := strings.IndexAny(header, ", \t")
			if j < 0 {
				j = len(header)
			}
			value.WriteString(header[:j])
			header = header[j:]
		}
		if current != nil {
			current[strings.ToLower(key)] = value.String()
		}
	}
	return out
}
