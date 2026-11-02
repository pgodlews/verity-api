package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// Vectors from the AWS Signature Version 4 test suite
// (get-vanilla, get-vanilla-query-order-key-case, post-x-www-form-urlencoded,
// get-utf8, get-space, post-header-key-sort). Each directory entry is the
// suite request plus the published canonical request, string to sign, and
// signature for access key AKIDEXAMPLE, the documentation example secret,
// region us-east-1, service name "service", and X-Amz-Date 20150830T123600Z.
// get-vanilla's signature is the suite's published authorization value.
// The example key is not a live credential.
const (
	suiteAccessKey = "AKIDEXAMPLE"
	suiteSecret    = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	suiteRegion    = "us-east-1"
	suiteService   = "service"
	suiteDate      = "20150830T123600Z"
)

func TestSigV4Suite(t *testing.T) {
	names := []string{
		"get-vanilla",
		"get-vanilla-query-order-key-case",
		"post-x-www-form-urlencoded",
		"get-utf8",
		"get-space",
		"post-header-key-sort",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join("testdata", "awssigv4")
			method, path, rawQuery, headers, body := parseSuiteRequest(t, filepath.Join(dir, name+".req"))
			wantCanon := strings.TrimRight(readText(t, filepath.Join(dir, name+".creq")), "\n")
			wantSTS := strings.TrimRight(readText(t, filepath.Join(dir, name+".sts")), "\n")
			wantAuth := strings.TrimRight(readText(t, filepath.Join(dir, name+".authz")), "\n")
			sum := sha256.Sum256([]byte(wantCanon))
			if got := hex.EncodeToString(sum[:]); got != lastLine(wantSTS) {
				t.Fatalf("embedded string to sign does not hash the canonical request: %s", got)
			}
			signed, err := signV4(v4Request{
				method: method, path: path, rawQuery: rawQuery, headers: headers, body: body,
			}, v4Creds{
				accessKey: suiteAccessKey, secret: suiteSecret, region: suiteRegion,
				service: suiteService, amzDate: suiteDate,
			})
			if err != nil {
				t.Fatal(err)
			}
			if signed.canonical != wantCanon {
				t.Fatalf("canonical request\n got:\n%s\nwant:\n%s", signed.canonical, wantCanon)
			}
			if signed.stringToSign != wantSTS {
				t.Fatalf("string to sign\n got:\n%s\nwant:\n%s", signed.stringToSign, wantSTS)
			}
			if signed.authorization != wantAuth {
				t.Fatalf("authorization\n got: %s\nwant: %s", signed.authorization, wantAuth)
			}
		})
	}
}

// The IAM signing example from the AWS Signature Version 4 documentation.
// The canonical-request hash and signature are the published values.
func TestSigV4DocsExample(t *testing.T) {
	headers := http.Header{}
	headers.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	headers.Set("Host", "iam.amazonaws.com")
	headers.Set("X-Amz-Date", suiteDate)
	signed, err := signV4(v4Request{
		method: "GET", path: "/", rawQuery: "Action=ListUsers&Version=2010-05-08",
		headers: headers,
	}, v4Creds{
		accessKey: suiteAccessKey, secret: suiteSecret, region: suiteRegion,
		service: "iam", amzDate: suiteDate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256Hex([]byte(signed.canonical)); got != "f536975d06c0309214f805bb90ccff089219ecd68b2577efef23edd43b7e1a59" {
		t.Fatalf("canonical hash %s\n%s", got, signed.canonical)
	}
	if signed.signature != "5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7" {
		t.Fatalf("signature %s", signed.signature)
	}
}

func TestSigV4PathEncoding(t *testing.T) {
	if got := canonicalURI("/example%20space/foo/../bar", false); got != "/example%2520space/bar" {
		t.Fatalf("non-s3 path %s", got)
	}
	if got := canonicalURI("/example%20space/foo/../bar", true); got != "/example%20space/foo/../bar" {
		t.Fatalf("s3 path %s", got)
	}
	if got := canonicalURI("/./a/../b//c", false); got != "/b//c" {
		t.Fatalf("normalized %s", got)
	}
	if got := removeDotSegments("/example space/"); got != "/example space/" {
		t.Fatalf("space path %q", got)
	}
}

func TestSigV4ProviderSignsAndRedacts(t *testing.T) {
	red := secrets.NewRedactor()
	p, err := newSigV4(map[string]any{
		"accessKeyId": suiteAccessKey, "secretAccessKey": suiteSecret,
		"sessionToken": "session-token-value-0123456789",
		"region":       suiteRegion, "service": suiteService,
	}, red)
	if err != nil {
		t.Fatal(err)
	}
	prov := p.(*sigV4Provider)
	prov.now = func() time.Time { return time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC) }
	req := &spi.Request{
		Method:  "POST",
		URL:     "https://example.amazonaws.com/example%20space/?Param2=value2&Param1=value1",
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    []byte(`{"ok":true}`),
	}
	if err := prov.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if req.Headers.Get("X-Amz-Date") != suiteDate {
		t.Fatalf("date %s", req.Headers.Get("X-Amz-Date"))
	}
	if req.Headers.Get("X-Amz-Content-Sha256") != sha256Hex(req.Body) {
		t.Fatal("payload hash header")
	}
	if req.Headers.Get("X-Amz-Security-Token") != "session-token-value-0123456789" {
		t.Fatal("session token header")
	}
	authz := req.Headers.Get("Authorization")
	if !strings.Contains(authz, "SignedHeaders=") || !strings.Contains(authz, "x-amz-content-sha256") || !strings.Contains(authz, "x-amz-security-token") {
		t.Fatalf("authorization %s", authz)
	}
	if strings.Contains(authz, "/example%20space") && !strings.Contains(authz, "Credential=") {
		t.Fatal(authz)
	}
	if red.Redact(suiteSecret) == suiteSecret || red.Redact(authz) == authz || red.Redact(req.Headers.Get("X-Amz-Security-Token")) == req.Headers.Get("X-Amz-Security-Token") {
		t.Fatal("secret, signature, or session token was not redacted")
	}
	prov.now = func() time.Time { return time.Date(2015, 8, 30, 12, 36, 1, 0, time.UTC) }
	again := &spi.Request{Method: "POST", URL: req.URL, Headers: req.Headers.Clone(), Body: req.Body}
	if err := prov.Apply(context.Background(), again); err != nil {
		t.Fatal(err)
	}
	if again.Headers.Get("X-Amz-Date") == req.Headers.Get("X-Amz-Date") || again.Headers.Get("Authorization") == authz {
		t.Fatal("retry did not re-sign")
	}
}

func TestSigV4UnsignedAndEnvFallback(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", suiteAccessKey)
	t.Setenv("AWS_SECRET_ACCESS_KEY", suiteSecret)
	t.Setenv("AWS_SESSION_TOKEN", "env-session-token-0123456789")
	red := secrets.NewRedactor()
	p, err := newSigV4(map[string]any{
		"region": suiteRegion, "service": "s3", "unsignedPayload": true,
	}, red)
	if err != nil {
		t.Fatal(err)
	}
	prov := p.(*sigV4Provider)
	prov.now = func() time.Time { return time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC) }
	req := &spi.Request{Method: "PUT", URL: "https://example.amazonaws.com/example%20space/foo/../bar", Headers: http.Header{}, Body: []byte("payload")}
	if err := prov.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if req.Headers.Get("X-Amz-Content-Sha256") != "UNSIGNED-PAYLOAD" {
		t.Fatalf("hash %s", req.Headers.Get("X-Amz-Content-Sha256"))
	}
	if req.Headers.Get("X-Amz-Security-Token") != "env-session-token-0123456789" {
		t.Fatal("env session token")
	}
	if !strings.Contains(req.Headers.Get("Authorization"), "Credential="+suiteAccessKey+"/") {
		t.Fatal(req.Headers.Get("Authorization"))
	}
	// S3 keeps the encoded space and the dot segment.
	signed, err := signV4(v4Request{
		method: "PUT", path: "/example%20space/foo/../bar", headers: req.Headers.Clone(), body: req.Body,
	}, v4Creds{
		accessKey: suiteAccessKey, secret: suiteSecret, region: suiteRegion, service: "s3",
		unsigned: true, amzDate: suiteDate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(signed.canonical, "PUT\n/example%20space/foo/../bar\n") {
		t.Fatalf("canonical\n%s", signed.canonical)
	}
	if red.Redact(suiteSecret) == suiteSecret {
		t.Fatal("env secret not redacted")
	}
}

func TestSigV4ConfigErrors(t *testing.T) {
	red := secrets.NewRedactor()
	if _, err := newSigV4(map[string]any{"region": "us-east-1", "service": "s3"}, red); err == nil {
		t.Fatal("missing keys")
	}
	if _, err := newSigV4(map[string]any{"accessKeyId": "a", "secretAccessKey": "b", "region": "us-east-1"}, red); err == nil {
		t.Fatal("missing service")
	}
	if _, err := newSigV4(map[string]any{
		"accessKeyId": "a", "secretAccessKey": "b", "region": "us-east-1", "service": "s3",
		"unsignedPayload": "yes",
	}, red); err == nil {
		t.Fatal("bad flag")
	}
	p, err := newSigV4(map[string]any{
		"accessKeyId": suiteAccessKey, "secretAccessKey": suiteSecret,
		"region": suiteRegion, "service": suiteService,
	}, red)
	if err != nil {
		t.Fatal(err)
	}
	err = p.Apply(context.Background(), &spi.Request{URL: "/relative"})
	if err == nil || !strings.Contains(err.Error(), string(verr.CodeAuth)) {
		t.Fatalf("got %v", err)
	}
	// An explicit key wins over the environment.
	t.Setenv("AWS_ACCESS_KEY_ID", "FROMENV")
	p, err = newSigV4(map[string]any{
		"accessKeyId": "EXPLICITKEY", "secretAccessKey": suiteSecret,
		"region": suiteRegion, "service": suiteService,
	}, red)
	if err != nil {
		t.Fatal(err)
	}
	if p.(*sigV4Provider).accessKey != "EXPLICITKEY" {
		t.Fatal(p.(*sigV4Provider).accessKey)
	}
}

func parseSuiteRequest(t *testing.T, path string) (method, uriPath, rawQuery string, headers http.Header, body []byte) {
	t.Helper()
	raw := readText(t, path)
	head, bodyText, _ := strings.Cut(raw, "\n\n")
	body = []byte(strings.TrimSuffix(bodyText, "\n"))
	lines := strings.Split(head, "\n")
	method, target, err := parseSuiteRequestLine(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	uriPath, rawQuery, _ = strings.Cut(target, "?")
	headers = http.Header{}
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("header %q", line)
		}
		headers.Add(name, strings.TrimSpace(value))
	}
	return method, uriPath, rawQuery, headers, body
}

func parseSuiteRequestLine(line string) (string, string, error) {
	const suffix = " HTTP/1.1"
	if !strings.HasSuffix(line, suffix) {
		return "", "", verr.New(verr.CodeInternal, "bad request line")
	}
	method, target, ok := strings.Cut(strings.TrimSuffix(line, suffix), " ")
	if !ok || method == "" || target == "" {
		return "", "", verr.New(verr.CodeInternal, "bad request line")
	}
	return method, target, nil
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// A raw space or non-ASCII byte in the URL template is escaped by net/http
// before it is sent. Apply must sign that wire form, not the template text,
// or the server computes a different canonical URI.
func TestSigV4SignsTheEscapedWirePath(t *testing.T) {
	red := secrets.NewRedactor()
	p, err := newSigV4(map[string]any{
		"accessKeyId": suiteAccessKey, "secretAccessKey": suiteSecret,
		"region": suiteRegion, "service": suiteService,
	}, red)
	if err != nil {
		t.Fatal(err)
	}
	prov := p.(*sigV4Provider)
	prov.now = func() time.Time { return time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC) }
	for _, tc := range []struct{ template, wire string }{
		{"https://example.amazonaws.com/prod/a b/ü", "/prod/a%20b/%C3%BC"},
		{"https://example.amazonaws.com/prod/a%20b/%41", "/prod/a%20b/%41"},
	} {
		u, err := url.Parse(tc.template)
		if err != nil {
			t.Fatal(err)
		}
		if u.EscapedPath() != tc.wire {
			t.Fatalf("%s: wire path %s, want %s", tc.template, u.EscapedPath(), tc.wire)
		}
		req := &spi.Request{Method: "GET", URL: tc.template, Headers: http.Header{}}
		if err := prov.Apply(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		want, err := signV4(v4Request{method: "GET", path: tc.wire, headers: req.Headers.Clone()}, v4Creds{
			accessKey: suiteAccessKey, secret: suiteSecret, region: suiteRegion, service: suiteService, amzDate: suiteDate,
		})
		if err != nil {
			t.Fatal(err)
		}
		// The clone carries the Authorization header Apply set; canonicalHeaders skips it.
		if got := req.Headers.Get("Authorization"); got != want.authorization {
			t.Fatalf("%s:\n got  %s\n want %s", tc.template, got, want.authorization)
		}
	}
}

func TestSigV4SignsTheEscapedWireQuery(t *testing.T) {
	red := secrets.NewRedactor()
	p, err := newSigV4(map[string]any{
		"accessKeyId": suiteAccessKey, "secretAccessKey": suiteSecret,
		"region": suiteRegion, "service": suiteService,
	}, red)
	if err != nil {
		t.Fatal(err)
	}
	prov := p.(*sigV4Provider)
	prov.now = func() time.Time { return time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC) }
	req := &spi.Request{Method: "GET", URL: "http://host/p?a=x y", Headers: http.Header{}}
	if err := prov.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	want, err := signV4(v4Request{method: "GET", path: "/p", rawQuery: "a=x%20y", headers: req.Headers.Clone()}, v4Creds{
		accessKey: suiteAccessKey, secret: suiteSecret, region: suiteRegion, service: suiteService, amzDate: suiteDate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Headers.Get("Authorization"); got != want.authorization {
		t.Fatalf("got %s, want signature over escaped query %s", got, want.authorization)
	}
}
