package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

func TestRunFiltersAndUntilStep(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	step := func(name string) model.Step {
		return model.Step{Name: name, Protocol: "rest", Request: map[string]any{"url": srv.URL + "/json"},
			Transfers: []model.TransferSpec{{Name: "wname", From: "jsonpath", Expr: "$.name"}}}
	}
	project := &model.Project{
		SchemaVersion: "1.0", Name: "f",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{
			{Name: "smoke", Cases: []model.TestCase{
				{Name: "login flow", Steps: []model.Step{step("a"), step("b"), step("c")}},
				{Name: "other", Steps: []model.Step{step("x")}},
			}},
			{Name: "full", Cases: []model.TestCase{{Name: "z", Steps: []model.Step{step("z")}}}},
		},
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{
		Suites: []string{"sm*"}, Cases: []string{"login flow"}, UntilStep: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suites) != 1 || len(res.Suites[0].Cases) != 1 || len(res.Suites[0].Cases[0].Steps) != 2 {
		t.Fatalf("filter not applied: %+v", res.Suites)
	}
	if len(project.Suites[0].Cases[0].Steps) != 3 {
		t.Fatal("filtering mutated the input project")
	}
	caps := res.Suites[0].Cases[0].Steps[1].Captures
	if len(caps) != 1 || caps[0].Name != "wname" || caps[0].Value != "widget" {
		t.Fatalf("captures: %+v", caps)
	}
}

func TestTransportErrorKeepsCause(t *testing.T) {
	project := &model.Project{
		SchemaVersion: "1.0", Name: "e",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{
			{Name: "down", Protocol: "rest", Request: map[string]any{"url": "http://127.0.0.1:9/x"}},
		}}}}},
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Suites[0].Cases[0].Steps[0].Error
	if e == nil || len(e.Message) <= len("execute request") {
		t.Fatalf("error lost its cause: %+v", e)
	}
}

func TestRunFilterErrors(t *testing.T) {
	project := &model.Project{
		SchemaVersion: "1.0", Name: "f",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{
			{Name: "x", Protocol: "rest", Request: map[string]any{"url": "http://127.0.0.1:9"}},
		}}}}},
	}
	for name, opts := range map[string]Options{
		"no match":      {Suites: []string{"typo"}},
		"bad glob":      {Cases: []string{"["}},
		"negative step": {UntilStep: -1},
	} {
		if _, err := New(bootstrap.Default()).Run(context.Background(), project, opts); err == nil {
			t.Errorf("%s: expected an error, got a run", name)
		}
	}
}

func TestSensitiveCapturesAreMasked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token":"abcd1234","echo":"abcd1234","pin":"abc"}`))
	}))
	defer srv.Close()
	project := &model.Project{
		SchemaVersion: "1.0", Name: "c",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{
			{Name: "login", Protocol: "rest", Request: map[string]any{"url": srv.URL},
				Transfers: []model.TransferSpec{
					{Name: "token", From: "jsonpath", Expr: "$.token"},
					{Name: "pinSecret", From: "jsonpath", Expr: "$.pin"},
				}},
		}}}}},
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	for _, lit := range []string{"abcd1234", `"abc"`} {
		if strings.Contains(string(b), lit) {
			t.Errorf("captured secret %s visible in the result: %s", lit, b)
		}
	}
}

func TestBailStopsBetweenFeedRows(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	project := &model.Project{
		SchemaVersion: "1.0", Name: "b",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "rows",
			Feed: &model.Feed{Type: "json", Inline: []map[string]string{{"n": "1"}, {"n": "2"}, {"n": "3"}}},
			Steps: []model.Step{{Name: "x", Protocol: "rest", Request: map[string]any{"url": srv.URL},
				Assertions: []model.Assertion{{Type: "status", Value: 200}}}},
		}}}},
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{Bail: true})
	if err != nil {
		t.Fatal(err)
	}
	cases := res.Suites[0].Cases
	if hits.Load() != 1 || len(cases) != 3 || cases[1].Status != spi.StatusSkipped || cases[2].Status != spi.StatusSkipped {
		t.Fatalf("hits=%d cases=%+v", hits.Load(), cases)
	}
}
