package runner

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

// This verifier is independent of the provider's parser and signing helpers.
var digestFields = regexp.MustCompile(`([a-zA-Z_]+)=(?:"((?:\\.|[^"\\])*)"|([^,\s]+))`)

func verifyDigest(header, method, uri, password, algorithm, qop string) (map[string]string, bool) {
	fields := map[string]string{}
	for _, match := range digestFields.FindAllStringSubmatch(header, -1) {
		value := match[2]
		if value == "" {
			value = match[3]
		}
		fields[match[1]] = value
	}
	hash := func(s string) string {
		if strings.HasPrefix(algorithm, "SHA-256") {
			value := sha256.Sum256([]byte(s))
			return hex.EncodeToString(value[:])
		}
		value := md5.Sum([]byte(s))
		return hex.EncodeToString(value[:])
	}
	a1 := hash("user:testing:" + password)
	if strings.HasSuffix(algorithm, "-sess") {
		a1 = hash(a1 + ":" + fields["nonce"] + ":" + fields["cnonce"])
	}
	a2 := hash(method + ":" + uri)
	expected := hash(a1 + ":" + fields["nonce"] + ":" + a2)
	if qop != "" {
		expected = hash(a1 + ":" + fields["nonce"] + ":" + fields["nc"] + ":" + fields["cnonce"] + ":auth:" + a2)
	}
	valid := strings.HasPrefix(header, "Digest ") && fields["username"] == "user" &&
		fields["realm"] == "testing" && fields["opaque"] == "opaque-value" &&
		fields["uri"] == uri && fields["qop"] == qop && fields["response"] == expected
	return fields, valid
}

func TestDigestHTTP(t *testing.T) {
	for _, algorithm := range []string{"MD5", "MD5-sess", "SHA-256", "SHA-256-sess"} {
		for _, mode := range []string{"auth", "legacy", "stale", "wrong", "parallel"} {
			t.Run(algorithm+"/"+mode, func(t *testing.T) {
				password := rand.Text()
				configured := password
				if mode == "wrong" {
					configured = rand.Text()
				}
				t.Setenv("DIGEST_PASSWORD", configured)
				qop := "auth"
				if mode == "legacy" {
					qop = ""
				}
				var mu sync.Mutex
				calls, unauthorized := 0, 0
				nonce := "first"
				seen := map[string]bool{}
				var hashes []string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if string(body) != "request payload" {
						t.Error("request body was not replayed")
					}
					mu.Lock()
					defer mu.Unlock()
					calls++
					fields, valid := verifyDigest(r.Header.Get("Authorization"), r.Method, r.URL.RequestURI(), password, algorithm, qop)
					stale := false
					if valid && mode == "stale" && nonce == "first" && len(hashes) > 0 {
						nonce = "second"
						stale = true
					}
					if !valid || fields["nonce"] != nonce {
						unauthorized++
						challenge := fmt.Sprintf(`Digest realm="testing", nonce="%s", opaque="opaque-value", algorithm=%s`, nonce, algorithm)
						if qop != "" {
							challenge += `, qop="auth-int, auth"`
						}
						if stale {
							challenge += `, stale=true`
						}
						w.Header().Set("WWW-Authenticate", challenge)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if qop != "" {
						key := fields["nonce"] + ":" + fields["nc"]
						if seen[key] {
							t.Error("nonce count was reused")
						}
						seen[key] = true
						n, err := strconv.ParseUint(fields["nc"], 16, 32)
						if err != nil || n == 0 || len(fields["cnonce"]) < 16 {
							t.Error("invalid nonce count or cnonce")
						}
					}
					hashes = append(hashes, fields["response"])
					fmt.Fprintf(w, `{"password":%q,"echo":%q}`, configured, fields["response"])
				}))
				defer srv.Close()
				cases, steps, parallel := 1, 2, 1
				if mode == "stale" {
					steps = 3
				}
				if mode == "parallel" {
					cases, parallel = 16, 8
				}
				project := &model.Project{
					SchemaVersion: "1.0", Name: "digest",
					Auth: []model.AuthDef{{Name: "digest", Type: "digest", Config: map[string]any{
						"username": "user", "password": "${env:DIGEST_PASSWORD}",
					}}},
					Suites: []model.Suite{{Name: "suite", Auth: &model.AuthRef{Use: []string{"digest"}}}},
				}
				for i := 0; i < cases; i++ {
					c := model.TestCase{Name: fmt.Sprint(i)}
					for j := 0; j < steps; j++ {
						c.Steps = append(c.Steps, model.Step{
							Name: fmt.Sprint(j), Protocol: "rest",
							Request: map[string]any{
								"url": srv.URL + "/resource?part=1", "query": map[string]any{"extra": "two"},
								"method": "POST", "body": "request payload",
							},
							Assertions: []model.Assertion{{Type: "status", Value: 200}},
						})
					}
					project.Suites[0].Cases = append(project.Suites[0].Cases, c)
				}
				reg := bootstrap.Default()
				result, err := New(reg).Run(context.Background(), project, Options{Parallel: parallel})
				if err != nil {
					t.Fatal(err)
				}
				want := spi.StatusPassed
				if mode == "wrong" {
					want = spi.StatusFailed
				}
				if result.Summary.Status != want {
					t.Fatalf("status=%s want=%s: %+v", result.Summary.Status, want, result.Suites[0].Cases[0].Steps)
				}
				mu.Lock()
				defer mu.Unlock()
				if mode == "auth" || mode == "legacy" {
					if unauthorized != 1 || calls != 3 {
						t.Fatalf("preauthorization failed: calls=%d challenges=%d", calls, unauthorized)
					}
				}
				if mode == "stale" && (unauthorized != 2 || calls != 5) {
					t.Fatalf("stale challenge not retried once: calls=%d challenges=%d", calls, unauthorized)
				}
				if mode == "wrong" && calls != 4 {
					t.Fatalf("retry bound: got %d calls", calls)
				}
				for _, typ := range []string{"json", "junit", "html", "console"} {
					reporter, _ := reg.Reporter(typ)
					var out bytes.Buffer
					if err := reportTo(reporter, result, &out); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(out.String(), configured) {
						t.Fatalf("password leaked into %s", typ)
					}
					for _, hash := range hashes {
						if strings.Contains(out.String(), hash) {
							t.Fatalf("digest response leaked into %s", typ)
						}
					}
				}
				raw, _ := json.Marshal(result)
				if strings.Contains(string(raw), configured) {
					t.Fatal("password in result")
				}
				header := result.Suites[0].Cases[0].Steps[0].Request.Headers["Authorization"]
				if header != "" && !strings.Contains(header, secrets.Mask) {
					t.Fatal("authorization capture not redacted")
				}
			})
		}
	}
}

func TestDigestSOAPAndGraphQL(t *testing.T) {
	for _, protocol := range []string{"soap", "graphql"} {
		t.Run(protocol, func(t *testing.T) {
			password := rand.Text()
			t.Setenv("DIGEST_PASSWORD", password)
			var mu sync.Mutex
			var calls int
			var firstBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				body, _ := io.ReadAll(r.Body)
				if calls == 1 {
					firstBody = body
					w.Header().Set("WWW-Authenticate", `Digest realm="testing", nonce="n", opaque="opaque-value", qop="auth", algorithm=SHA-256`)
					w.WriteHeader(401)
					return
				}
				if !bytes.Equal(body, firstBody) || len(body) == 0 {
					t.Error("body changed during challenge replay")
				}
				if _, valid := verifyDigest(r.Header.Get("Authorization"), r.Method, r.URL.RequestURI(), password, "SHA-256", "auth"); !valid {
					t.Error("invalid signature")
					w.WriteHeader(401)
				}
			}))
			defer srv.Close()
			project := &model.Project{
				SchemaVersion: "1.0", Name: protocol,
				Auth: []model.AuthDef{{Name: "digest", Type: "digest", Config: map[string]any{
					"username": "user", "password": "${env:DIGEST_PASSWORD}",
				}}},
				Suites: []model.Suite{{Name: "suite", Auth: &model.AuthRef{Use: []string{"digest"}}, Cases: []model.TestCase{
					{Name: "case", Steps: []model.Step{{
						Name: "request", Protocol: protocol,
						Request:    map[string]any{"url": srv.URL, "body": "<Envelope/>", "query": "{ status }"},
						Assertions: []model.Assertion{{Type: "status", Value: 200}},
					}}},
				}}},
			}
			result, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if result.Summary.Status != spi.StatusPassed || calls != 2 {
				t.Fatalf("status=%s calls=%d", result.Summary.Status, calls)
			}
		})
	}
}

func TestDigestUnsupportedChallengeIsAuthError(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("WWW-Authenticate", `Digest realm="testing", nonce="n", qop="auth-int"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	password := rand.Text()
	t.Setenv("DIGEST_PASSWORD", password)
	project := &model.Project{
		SchemaVersion: "1.0", Name: "unsupported",
		Auth: []model.AuthDef{{Name: "digest", Type: "digest", Config: map[string]any{
			"username": "user", "password": "${env:DIGEST_PASSWORD}",
		}}},
		Suites: []model.Suite{{Name: "suite", Auth: &model.AuthRef{Use: []string{"digest"}}, Cases: []model.TestCase{
			{Name: "case", Steps: []model.Step{{
				Name: "request", Protocol: "rest", Request: map[string]any{"url": srv.URL},
				Retry: &model.Retry{Count: 2, Until: "status"},
			}}},
		}}},
	}
	result, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	step := result.Suites[0].Cases[0].Steps[0]
	mu.Lock()
	defer mu.Unlock()
	if step.Status != spi.StatusError || step.Error == nil || step.Error.Code != "auth_error" || step.Attempts != 1 || calls != 1 {
		t.Fatalf("unsupported challenge was classified or retried incorrectly: %+v; calls=%d", step, calls)
	}
}

// A 401 that arrives after a redirect must be answered for the redirected
// request (its URI and method), not the original one.
func TestDigestAfterRedirect(t *testing.T) {
	password := rand.Text()
	t.Setenv("DIGEST_PASSWORD", password)
	var mu sync.Mutex
	var signedURIs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new?x=1", http.StatusFound)
			return
		}
		fields, valid := verifyDigest(r.Header.Get("Authorization"), r.Method, r.URL.RequestURI(), password, "MD5", "auth")
		if !valid {
			w.Header().Set("WWW-Authenticate", `Digest realm="testing", nonce="n2", opaque="opaque-value", qop="auth"`)
			w.WriteHeader(401)
			return
		}
		signedURIs = append(signedURIs, fields["uri"])
	}))
	defer srv.Close()
	project := &model.Project{
		SchemaVersion: "1.0", Name: "redirect",
		Auth: []model.AuthDef{{Name: "digest", Type: "digest", Config: map[string]any{
			"username": "user", "password": "${env:DIGEST_PASSWORD}",
		}}},
		Suites: []model.Suite{{Name: "suite", Auth: &model.AuthRef{Use: []string{"digest"}}, Cases: []model.TestCase{
			{Name: "case", Steps: []model.Step{{
				Name: "get", Protocol: "rest", Request: map[string]any{"url": srv.URL + "/old"},
				Assertions: []model.Assertion{{Type: "status", Value: 200}},
			}}},
		}}},
	}
	result, err := New(bootstrap.Default()).Run(context.Background(), project, Options{FollowRedirects: true})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if result.Summary.Status != spi.StatusPassed || len(signedURIs) != 1 || signedURIs[0] != "/new?x=1" {
		t.Fatalf("status=%s signed=%v", result.Summary.Status, signedURIs)
	}
}
