package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
)

func TestCaseExecSetupOnceAndIsolatedRunVars(t *testing.T) {
	var setupHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/setup":
			setupHits.Add(1)
			fmt.Fprint(w, `{"token":"tok-from-setup","region":"eu"}`)
		default:
			if r.Header.Get("Authorization") != "Bearer tok-from-setup" {
				http.Error(w, "auth", http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"region":"eu"}`)
		}
	}))
	defer srv.Close()

	p := &model.Project{
		SchemaVersion: "1.0",
		Name:          "caseexec",
		Environments:  []model.Environment{{Name: "local", Default: true}},
		Suites: []model.Suite{{
			Name: "api",
			Setup: &model.Hook{Name: "login", Steps: []model.Step{{
				Name: "login", Protocol: "rest",
				Request: map[string]any{"url": srv.URL + "/setup"},
				Transfers: []model.TransferSpec{{
					Name: "token", From: "jsonpath", Expr: "$.token", Scope: model.TransferScopeRun,
				}},
			}}},
			Cases: []model.TestCase{{
				Name: "work",
				Steps: []model.Step{{
					Name: "work", Protocol: "rest",
					Request: map[string]any{
						"url":     srv.URL + "/work",
						"headers": map[string]any{"Authorization": "Bearer ${token}"},
					},
					Assertions: []model.Assertion{{Type: "status", Value: 200}},
					Transfers: []model.TransferSpec{{
						Name: "region", From: "jsonpath", Expr: "$.region", Scope: model.TransferScopeRun,
					}},
				}},
			}},
		}},
	}
	exec, err := New(bootstrap.Default()).Prepare(p, Options{FollowRedirects: true})
	if err != nil {
		t.Fatal(err)
	}
	setups := exec.RunSetups(context.Background())
	if len(setups) != 1 || setups[0].Result.Status != "passed" {
		t.Fatalf("setup %+v", setups)
	}
	if _, ok := exec.RunScoped()["token"]; !ok {
		t.Fatal("setup did not record the run-scoped token")
	}
	baseline := exec.RunScoped()
	vu := map[string]string{}
	for k, v := range baseline {
		vu[k] = v
	}
	cases := exec.Cases()
	rt := &countRT{base: srv.Client().Transport}
	for i := 0; i < 2; i++ {
		res, err := exec.RunCase(context.Background(), CaseRequest{
			Suite: cases[0].Suite, Case: cases[0].Case,
			Resolver:  exec.NewResolver(cases[0].Suite, cases[0].Case, vu),
			Transport: rt,
			OnRunVar: func(name, value string) {
				vu[name] = value
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 1 || res[0].Status != "passed" {
			t.Fatalf("case %+v", res)
		}
	}
	if setupHits.Load() != 1 {
		t.Fatalf("setup hits %d", setupHits.Load())
	}
	if rt.n.Load() != 2 {
		t.Fatalf("transport round trips %d, want 2", rt.n.Load())
	}
	if vu["region"] != "eu" {
		t.Fatalf("vu region %q", vu["region"])
	}
	if _, ok := baseline["region"]; ok {
		t.Fatal("caller's baseline map was updated")
	}
	if _, ok := exec.RunScoped()["region"]; ok {
		t.Fatal("run-scoped capture leaked onto the shared baseline")
	}
}

type countRT struct {
	base http.RoundTripper
	n    atomic.Int32
}

func (c *countRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.base.RoundTrip(r)
}
