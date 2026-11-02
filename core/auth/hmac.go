package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // configured algorithm; sha256 is the default
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// hmacProvider signs the finished request body, or a message template, with
// HMAC. ${method}, ${path}, ${query}, ${body}, ${timestamp}, and ${nonce} in
// the template are filled here, after project interpolation. Each Apply mints
// a new timestamp and nonce, so a retry signs again.
type hmacProvider struct {
	secret, algorithm, header, encoding, prefix string
	template, timestampHeader, nonceHeader      string
	hash                                        func() hash.Hash
	red                                         *secrets.Redactor
	now                                         func() time.Time
	nonce                                       func() (string, error)
}

func newHMAC(config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
	p := &hmacProvider{
		secret:          strConfig(config, "secret"), // an HMAC key is bytes; spaces can be part of it
		algorithm:       strings.ToLower(strings.TrimSpace(strConfig(config, "algorithm"))),
		header:          strings.TrimSpace(strConfig(config, "header")),
		encoding:        strings.ToLower(strings.TrimSpace(strConfig(config, "encoding"))),
		prefix:          strConfig(config, "prefix"),
		template:        strConfig(config, "message"),
		timestampHeader: strings.TrimSpace(strConfig(config, "timestampHeader")),
		nonceHeader:     strings.TrimSpace(strConfig(config, "nonceHeader")),
		red:             red,
		now:             time.Now,
		nonce:           randomNonce,
	}
	if p.secret == "" {
		return nil, verr.New(verr.CodeConfig, "hmac requires 'secret'")
	}
	if p.algorithm == "" {
		p.algorithm = "sha256"
	}
	switch p.algorithm {
	case "sha1":
		p.hash = sha1.New
	case "sha256":
		p.hash = sha256.New
	case "sha512":
		p.hash = sha512.New
	default:
		return nil, verr.New(verr.CodeConfig, "hmac algorithm must be sha1, sha256, or sha512")
	}
	if p.header == "" {
		p.header = "X-Signature"
	}
	if !validHeaderName(p.header) || (p.timestampHeader != "" && !validHeaderName(p.timestampHeader)) || (p.nonceHeader != "" && !validHeaderName(p.nonceHeader)) {
		return nil, verr.New(verr.CodeConfig, "hmac header names must be a single token")
	}
	if p.encoding == "" {
		p.encoding = "hex"
	}
	switch p.encoding {
	case "hex", "base64":
	default:
		return nil, verr.New(verr.CodeConfig, "hmac encoding must be hex or base64")
	}
	if strings.ContainsAny(p.prefix, "\r\n") {
		return nil, verr.New(verr.CodeConfig, "hmac prefix must be a single line")
	}
	red.Add(p.secret)
	return p, nil
}

func (*hmacProvider) Type() string         { return "hmac" }
func (*hmacProvider) Layer() spi.AuthLayer { return spi.LayerMessage }

// validHeaderName reports whether s is an HTTP field name (an RFC 9110 token).
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0 {
			continue
		}
		return false
	}
	return true
}

func randomNonce() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func (p *hmacProvider) Apply(_ context.Context, req *spi.Request) error {
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	ts := ""
	if p.timestampHeader != "" || strings.Contains(p.template, "${timestamp}") {
		ts = strconv.FormatInt(p.now().UTC().Unix(), 10)
		if p.timestampHeader != "" {
			req.Headers.Set(p.timestampHeader, ts)
		}
	}
	nonce := ""
	if p.nonceHeader != "" || strings.Contains(p.template, "${nonce}") {
		n, err := p.nonce()
		if err != nil {
			return verr.Wrap(verr.CodeAuth, err, "hmac nonce")
		}
		nonce = n
		if p.nonceHeader != "" {
			req.Headers.Set(p.nonceHeader, nonce)
		}
	}
	msg, err := p.message(req, ts, nonce)
	if err != nil {
		return err
	}
	mac := hmac.New(p.hash, []byte(p.secret))
	mac.Write([]byte(msg))
	sum := mac.Sum(nil)
	var encoded string
	switch p.encoding {
	case "base64":
		encoded = base64.StdEncoding.EncodeToString(sum)
	default:
		encoded = hex.EncodeToString(sum)
	}
	value := p.prefix + encoded
	req.Headers.Set(p.header, value)
	p.red.Add(encoded)
	p.red.Add(value)
	return nil
}

func (p *hmacProvider) message(req *spi.Request, ts, nonce string) (string, error) {
	if p.template == "" {
		return string(req.Body), nil
	}
	u, err := url.Parse(spi.NormalizeTargetQuery(req.URL))
	if err != nil {
		return "", verr.New(verr.CodeAuth, "hmac request URL is invalid")
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return strings.NewReplacer(
		"${method}", method,
		"${path}", path,
		"${query}", hmacQuery(u, req.Query),
		"${body}", string(req.Body),
		"${timestamp}", ts,
		"${nonce}", nonce,
	).Replace(p.template), nil
}

// hmacQuery is the query exactly as it is sent, without '?'.
func hmacQuery(u *url.URL, extra map[string]string) string {
	return spi.EscapeQuery(spi.MergeQuery(u.RawQuery, extra))
}
