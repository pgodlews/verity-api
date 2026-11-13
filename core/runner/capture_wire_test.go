package runner

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

// The captured URL must be the URL that was sent, including the step's
// query: map and an apikey placed in the query, with the key redacted.
func TestRequestCaptureMatchesWireURL(t *testing.T) {
	const key = "query-api-key-value-5150"
	t.Setenv("VERITY_TEST_QKEY", key)
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { sent = r.URL.RawQuery }))
	defer srv.Close()
	p := &model.Project{
		SchemaVersion: "1.0", Name: "capture",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Auth:         []model.AuthDef{{Name: "k", Type: "apikey", Config: map[string]any{"name": "api_key", "value": "${env:VERITY_TEST_QKEY}", "in": "query"}}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{{
			Name: "get", Protocol: "rest", Auth: &model.AuthRef{Use: []string{"k"}},
			Request: map[string]any{"url": srv.URL + "/p?x=1", "query": map[string]any{"page": "2"}},
		}}}}}},
	}
	res := retryRun(t, nil, p, Options{})
	got := res.Suites[0].Cases[0].Steps[0].Request
	if got == nil {
		t.Fatal("no request capture")
	}
	if !strings.Contains(sent, "page=2") || !strings.Contains(sent, "api_key=") {
		t.Fatalf("wire query %q", sent)
	}
	for _, want := range []string{"x=1", "page=2", "api_key=" + secrets.Mask} {
		if !strings.Contains(got.URL, want) && !strings.Contains(got.URL, strings.ReplaceAll(want, "*", "%2A")) {
			t.Errorf("captured URL %q lacks %q", got.URL, want)
		}
	}
	if strings.Contains(got.URL, key) {
		t.Fatalf("api key leaked in captured URL %q", got.URL)
	}
}

func newCookieReq() *spi.Request {
	return &spi.Request{Method: "GET", URL: "http://127.0.0.1/", Headers: http.Header{}, Cookies: map[string]string{"session": "s3ss10n-value"}}
}

func TestCaptureCookiesAreMasked(t *testing.T) {
	req := captureRequest(newCookieReq(), secrets.NewRedactor())
	if req.Cookies["session"] != secrets.Mask {
		t.Fatalf("cookies = %v", req.Cookies)
	}
}
