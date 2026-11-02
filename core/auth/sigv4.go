package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// sigV4Provider signs the finished request with AWS Signature Version 4.
// Credentials are read when the provider is built. A missing accessKeyId or
// secretAccessKey falls back to AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY.
// A missing sessionToken falls back to AWS_SESSION_TOKEN. There is no
// shared-credentials file and no profile lookup.
type sigV4Provider struct {
	accessKey, secret, session string
	region, service            string
	unsigned                   bool
	red                        *secrets.Redactor
	now                        func() time.Time
}

func newSigV4(config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
	p := &sigV4Provider{
		accessKey: configOrEnv(config, "accessKeyId", "AWS_ACCESS_KEY_ID"),
		secret:    configOrEnv(config, "secretAccessKey", "AWS_SECRET_ACCESS_KEY"),
		session:   configOrEnv(config, "sessionToken", "AWS_SESSION_TOKEN"),
		region:    strings.TrimSpace(strConfig(config, "region")),
		service:   strings.TrimSpace(strConfig(config, "service")),
		red:       red,
		now:       time.Now,
	}
	unsigned, err := boolConfig(config, "unsignedPayload")
	if err != nil {
		return nil, err
	}
	p.unsigned = unsigned
	if p.accessKey == "" || p.secret == "" {
		return nil, verr.New(verr.CodeConfig, "aws_sigv4 requires accessKeyId and secretAccessKey, or AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY")
	}
	if p.region == "" || p.service == "" {
		return nil, verr.New(verr.CodeConfig, "aws_sigv4 requires region and service")
	}
	red.Add(p.secret)
	red.Add(p.session)
	return p, nil
}

func (*sigV4Provider) Type() string         { return "aws_sigv4" }
func (*sigV4Provider) Layer() spi.AuthLayer { return spi.LayerMessage }

// configOrEnv returns a config string when the key is present, including an
// empty string. An absent key is read from the named environment variable.
func configOrEnv(config map[string]any, key, env string) string {
	if _, ok := config[key]; ok {
		return strings.TrimSpace(strConfig(config, key))
	}
	return strings.TrimSpace(os.Getenv(env))
}

func boolConfig(config map[string]any, key string) (bool, error) {
	v, ok := config[key]
	if !ok || v == nil {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, verr.Newf(verr.CodeConfig, "aws_sigv4 %s must be a boolean", key)
	}
	return b, nil
}

func (p *sigV4Provider) Apply(_ context.Context, req *spi.Request) error {
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	u, err := url.Parse(spi.NormalizeTargetQuery(req.URL))
	if err != nil {
		return verr.New(verr.CodeAuth, "aws_sigv4 request URL is invalid")
	}
	host := u.Host
	if h := req.Headers.Get("Host"); h != "" {
		host = h
	}
	if host == "" {
		return verr.New(verr.CodeAuth, "aws_sigv4 request URL is missing a host")
	}
	req.Headers.Set("Host", host)
	amzDate := p.now().UTC().Format("20060102T150405Z")
	req.Headers.Set("X-Amz-Date", amzDate)
	req.Headers.Set("X-Amz-Content-Sha256", payloadHash(req.Body, p.unsigned))
	if p.session != "" {
		req.Headers.Set("X-Amz-Security-Token", p.session)
	}
	// Sign the path exactly as net/http will send it: EscapedPath keeps a
	// valid %HH from the template and escapes a raw space or non-ASCII byte,
	// so the canonical URI matches what the server sees on the wire.
	path, rawQuery := u.EscapedPath(), u.RawQuery
	signed, err := signV4(v4Request{
		method: req.Method, path: path, rawQuery: rawQuery, query: req.Query,
		headers: req.Headers, body: req.Body,
	}, v4Creds{
		accessKey: p.accessKey, secret: p.secret, region: p.region, service: p.service,
		unsigned: p.unsigned, amzDate: amzDate,
	})
	if err != nil {
		return err
	}
	req.Headers.Set("Authorization", signed.authorization)
	p.red.Add(signed.signature)
	return nil
}

type v4Request struct {
	method, path, rawQuery string
	query                  map[string]string
	headers                http.Header
	body                   []byte
}

type v4Creds struct {
	accessKey, secret, region, service string
	unsigned                           bool
	amzDate                            string
}

type v4Signed struct {
	canonical, stringToSign, signature, authorization, signedHeaders string
}

// signV4 builds the canonical request, string to sign, and Authorization
// value. headers must already contain host, x-amz-date, and, when the caller
// signs a payload hash, x-amz-content-sha256. service "s3" (any case) keeps
// a single URI encoding and does not remove dot segments. Every other service
// normalizes the path and encodes bytes that are already percent-encoded.
func signV4(req v4Request, creds v4Creds) (v4Signed, error) {
	if creds.amzDate == "" || len(creds.amzDate) < 8 {
		return v4Signed{}, verr.New(verr.CodeAuth, "aws_sigv4 requires x-amz-date")
	}
	if creds.region == "" || creds.service == "" || creds.accessKey == "" || creds.secret == "" {
		return v4Signed{}, verr.New(verr.CodeConfig, "aws_sigv4 requires accessKeyId, secretAccessKey, region, and service")
	}
	method := req.method
	if method == "" {
		method = http.MethodGet
	}
	query, err := canonicalQuery(req.rawQuery, req.query)
	if err != nil {
		return v4Signed{}, verr.Wrap(verr.CodeAuth, err, "aws_sigv4 query")
	}
	headerBlock, signedHeaders := canonicalHeaders(req.headers)
	canon := strings.Join([]string{
		method,
		canonicalURI(req.path, strings.EqualFold(creds.service, "s3")),
		query,
		headerBlock,
		signedHeaders,
		payloadHash(req.body, creds.unsigned),
	}, "\n")
	scope := creds.amzDate[:8] + "/" + creds.region + "/" + creds.service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		creds.amzDate,
		scope,
		sha256Hex([]byte(canon)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(creds.secret, creds.amzDate[:8], creds.region, creds.service), stringToSign))
	authorization := "AWS4-HMAC-SHA256 Credential=" + creds.accessKey + "/" + scope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	return v4Signed{
		canonical: canon, stringToSign: stringToSign, signature: signature,
		authorization: authorization, signedHeaders: signedHeaders,
	}, nil
}

func payloadHash(body []byte, unsigned bool) string {
	if unsigned {
		return "UNSIGNED-PAYLOAD"
	}
	return sha256Hex(body)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func signingKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

// transportOwned headers are set or dropped by net/http from the request's
// own fields, so the value in Header is not what is sent. An empty
// User-Agent suppresses the header and is skipped too.
var transportOwned = map[string]bool{
	"content-length": true, "transfer-encoding": true, "trailer": true,
	"connection": true, "keep-alive": true, "te": true, "upgrade": true,
	"proxy-connection": true,
}

func canonicalHeaders(h http.Header) (string, string) {
	names := make([]string, 0, len(h))
	vals := make(map[string]string, len(h))
	for k, vs := range h {
		name := strings.ToLower(k)
		if name == "authorization" || transportOwned[name] || (name == "user-agent" && strings.Join(vs, "") == "") {
			continue
		}
		names = append(names, name)
		vals[name] = canonicalHeaderValue(vs)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(vals[name])
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

func canonicalHeaderValue(vals []string) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(parts, ",")
}

func canonicalQuery(raw string, extra map[string]string) (string, error) {
	// Sign the query the sender puts on the wire, then read it the way AWS
	// does: pairs split on '&' and the first '=', percent-decoded with '+'
	// and ';' kept literal, encoded again, and sorted.
	wire := spi.EscapeQuery(spi.MergeQuery(raw, extra))
	type pair struct{ key, val string }
	var pairs []pair
	for _, part := range strings.Split(wire, "&") {
		if part == "" {
			continue
		}
		key, val, _ := strings.Cut(part, "=")
		pairs = append(pairs, pair{encodeAWS(awsUnescape(key), true), encodeAWS(awsUnescape(val), true)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key != pairs[j].key {
			return pairs[i].key < pairs[j].key
		}
		return pairs[i].val < pairs[j].val
	})
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.key)
		b.WriteByte('=')
		b.WriteString(p.val)
	}
	return b.String(), nil
}

// awsUnescape decodes %HH sequences and leaves everything else, including
// '+', as written. An invalid escape is kept literally.
func awsUnescape(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	if out, err := url.PathUnescape(s); err == nil {
		return out
	}
	return s
}

// canonicalURI encodes the path as it appeared in the request target.
// S3 leaves dot segments in place and does not encode an existing %HH
// sequence. Other services remove dot segments, then encode every byte that
// is not unreserved, including '%' so an already-encoded octet is encoded again.
func canonicalURI(path string, s3 bool) string {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var encoded string
	if s3 {
		encoded = encodeS3Path(path)
	} else {
		encoded = encodeAWS(removeDotSegments(path), false)
	}
	if encoded == "" {
		return "/"
	}
	return encoded
}

func encodeAWS(s string, encodeSlash bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) || (c == '/' && !encodeSlash) {
			b.WriteByte(c)
			continue
		}
		writePct(&b, c)
	}
	return b.String()
}

func encodeS3Path(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte('%')
			b.WriteByte(upperHex(s[i+1]))
			b.WriteByte(upperHex(s[i+2]))
			i += 2
			continue
		}
		if isUnreserved(c) || c == '/' {
			b.WriteByte(c)
			continue
		}
		writePct(&b, c)
	}
	return b.String()
}

func writePct(b *strings.Builder, c byte) {
	const hexdigits = "0123456789ABCDEF"
	b.WriteByte('%')
	b.WriteByte(hexdigits[c>>4])
	b.WriteByte(hexdigits[c&0x0F])
}

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '~'
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func upperHex(c byte) byte {
	if c >= 'a' && c <= 'f' {
		return c - 'a' + 'A'
	}
	return c
}

// removeDotSegments applies RFC 3986 section 5.2.4. Empty segments ("//") stay.
func removeDotSegments(path string) string {
	var out []byte
	in := path
	for len(in) > 0 {
		switch {
		case strings.HasPrefix(in, "../"):
			in = in[3:]
		case strings.HasPrefix(in, "./"):
			in = in[2:]
		case strings.HasPrefix(in, "/./"):
			in = in[2:]
		case in == "/.":
			in = "/"
		case strings.HasPrefix(in, "/../"):
			in = in[3:]
			out = popSegment(out)
		case in == "/..":
			in = "/"
			out = popSegment(out)
		case in == "." || in == "..":
			in = ""
		default:
			if in[0] == '/' {
				rest := in[1:]
				idx := strings.IndexByte(rest, '/')
				if idx < 0 {
					out = append(out, in...)
					in = ""
				} else {
					out = append(out, in[:idx+1]...)
					in = in[idx+1:]
				}
				continue
			}
			idx := strings.IndexByte(in, '/')
			if idx < 0 {
				out = append(out, in...)
				in = ""
			} else {
				out = append(out, in[:idx]...)
				in = in[idx:]
			}
		}
	}
	if len(out) == 0 {
		return "/"
	}
	return string(out)
}

func popSegment(out []byte) []byte {
	if len(out) == 0 {
		return out
	}
	i := len(out) - 1
	if out[i] == '/' && i > 0 {
		i--
	}
	for i > 0 && out[i] != '/' {
		i--
	}
	return out[:i]
}
