package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

func TestOAuth2RunCachingAndRedaction(t *testing.T) {
	for _, grant := range []string{"client_credentials", "password", "refresh_token", "authorization_code"} {
		t.Run(grant, func(t *testing.T) {
			clientSecret, password, refresh, rotated, access := rand.Text(), rand.Text(), rand.Text(), rand.Text(), rand.Text()
			t.Setenv("OAUTH_CLIENT_SECRET", clientSecret)
			t.Setenv("OAUTH_PASSWORD", password)
			t.Setenv("OAUTH_REFRESH_TOKEN", refresh)
			allSecrets := []string{clientSecret, password, refresh, rotated, access}
			var tokenCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					tokenCalls.Add(1)
					_ = r.ParseForm()
					wantGrant := grant
					if grant == "authorization_code" {
						wantGrant = "refresh_token"
					}
					if r.PostForm.Get("grant_type") != wantGrant {
						t.Error("incorrect grant at token endpoint")
					}
					if grant == "password" && r.PostForm.Get("password") != password || (grant == "refresh_token" || grant == "authorization_code") && r.PostForm.Get("refresh_token") != refresh {
						t.Error("credentials were not resolved from environment")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": rotated, "expires_in": 3600})
					return
				}
				if r.Header.Get("Authorization") != "Bearer "+access {
					t.Error("incorrect API bearer token")
					w.WriteHeader(http.StatusUnauthorized)
				}
				// Unlabelled values exercise literal registration, not field-name masking.
				_, _ = w.Write([]byte(strings.Join(allSecrets, " ")))
			}))
			defer srv.Close()
			project := &model.Project{
				SchemaVersion: "1.0", Name: "oauth-report",
				Auth: []model.AuthDef{{Name: "oauth", Type: "oauth2", Config: map[string]any{
					"authUrl": srv.URL + "/authorize", "tokenUrl": srv.URL + "/token", "clientId": "client", "clientSecret": "${env:OAUTH_CLIENT_SECRET}",
					"grant": grant, "username": "user", "password": "${env:OAUTH_PASSWORD}", "refreshToken": "${env:OAUTH_REFRESH_TOKEN}",
				}}},
				Suites: []model.Suite{{Name: "suite", Auth: &model.AuthRef{Use: []string{"oauth"}}}},
			}
			for _, name := range []string{"first", "second", "third"} {
				project.Suites[0].Cases = append(project.Suites[0].Cases, model.TestCase{
					Name: name, Feed: &model.Feed{Type: "json", Inline: []map[string]string{{"row": "1"}, {"row": "2"}}},
					Steps: []model.Step{
						{Name: "first step", Protocol: "rest", Request: map[string]any{"url": srv.URL}, Assertions: []model.Assertion{{Type: "status", Value: 200}}},
						{Name: "second step", Protocol: "rest", Request: map[string]any{"url": srv.URL}, Assertions: []model.Assertion{{Type: "status", Value: 200}}},
					},
				})
			}
			before, _ := json.Marshal(project)
			reg := bootstrap.Default()
			res, err := New(reg).Run(context.Background(), project, Options{Parallel: 3})
			if err != nil {
				t.Fatal(err)
			}
			if res.Summary.Status != spi.StatusPassed || tokenCalls.Load() != 1 {
				t.Fatalf("status=%s; token calls=%d", res.Summary.Status, tokenCalls.Load())
			}
			after, _ := json.Marshal(project)
			if !bytes.Equal(before, after) {
				t.Fatal("run mutated project auth config")
			}
			out, _ := json.Marshal(res)
			for _, secret := range allSecrets {
				if bytes.Contains(out, []byte(secret)) {
					t.Fatal("credential leaked into run result")
				}
			}
			body := res.Suites[0].Cases[0].Steps[0].Response.Body
			if body != strings.TrimSpace(strings.Repeat(secrets.Mask+" ", len(allSecrets))) {
				t.Fatal("response body did not mask every secret")
			}
			for _, typ := range []string{"console", "json", "junit", "html"} {
				reporter, err := reg.Reporter(typ)
				if err != nil {
					t.Fatal(err)
				}
				var buf bytes.Buffer
				if err := reportTo(reporter, res, &buf); err != nil {
					t.Fatal(err)
				}
				if buf.Len() == 0 {
					t.Fatalf("empty %s report", typ)
				}
				for _, secret := range allSecrets {
					if strings.Contains(buf.String(), secret) {
						t.Fatalf("credential leaked into %s report", typ)
					}
				}
			}
		})
	}
}
