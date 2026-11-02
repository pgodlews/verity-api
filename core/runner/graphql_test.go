package runner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
)

func TestGraphQLIntrospectionCachedPerURL(t *testing.T) {
	var intro atomic.Int32
	token := "fixture-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "__schema") {
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Errorf("introspection auth = %q", r.Header.Get("Authorization"))
			}
			intro.Add(1)
			_, _ = w.Write([]byte(miniIntro))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"user":{"id":"1","name":"Ada"}}}`))
	}))
	defer srv.Close()

	step := func(name string) model.Step {
		return model.Step{
			Name: name, Protocol: "graphql",
			Request: map[string]any{
				"url":   srv.URL,
				"query": `query { user(id: "1") { id name } }`,
			},
			Assertions: []model.Assertion{{Type: "graphql_schema"}},
		}
	}
	t.Setenv("VERITY_FIXTURE_TOKEN", token)
	project := &model.Project{
		SchemaVersion: "1.0", Name: "gql",
		Auth: []model.AuthDef{{
			Name: "tok", Type: "bearer",
			Config: map[string]any{"token": "${env:VERITY_FIXTURE_TOKEN}"},
		}},
		Suites: []model.Suite{{
			Name:   "s",
			Schema: &model.GraphQLSchema{Introspect: true},
			Auth:   &model.AuthRef{Use: []string{"tok"}},
			Cases: []model.TestCase{{
				Name:  "c",
				Steps: []model.Step{step("one"), step("two")},
			}},
		}},
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if intro.Load() != 1 {
		t.Fatalf("introspection calls: %d", intro.Load())
	}
	if res.Summary.Status != "passed" {
		var msgs []string
		for _, s := range res.Suites {
			for _, c := range s.Cases {
				for _, st := range c.Steps {
					for _, a := range st.Assertions {
						msgs = append(msgs, st.Name+": "+a.Message)
					}
					if st.Error != nil {
						msgs = append(msgs, st.Error.Message)
					}
				}
			}
		}
		t.Fatalf("status %s: %s", res.Summary.Status, strings.Join(msgs, " | "))
	}
	for _, s := range res.Suites {
		for _, c := range s.Cases {
			for _, st := range c.Steps {
				blob := ""
				if st.Error != nil {
					blob = st.Error.Message
				}
				for _, a := range st.Assertions {
					blob += a.Message
				}
				if strings.Contains(blob, token) {
					t.Fatalf("token leaked in %s: %s", st.Name, blob)
				}
			}
		}
	}
}

func TestGraphQLIntrospectionFailureDoesNotLogAuth(t *testing.T) {
	token := "fixture-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "__schema") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("Authorization: Bearer " + token))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"user":{"id":"1","name":"Ada"}}}`))
	}))
	defer srv.Close()
	t.Setenv("VERITY_FIXTURE_TOKEN", token)
	project := &model.Project{
		SchemaVersion: "1.0", Name: "gql",
		Auth: []model.AuthDef{{
			Name: "tok", Type: "bearer",
			Config: map[string]any{"token": "${env:VERITY_FIXTURE_TOKEN}"},
		}},
		Suites: []model.Suite{{
			Name:   "s",
			Schema: &model.GraphQLSchema{Introspect: true},
			Auth:   &model.AuthRef{Use: []string{"tok"}},
			Cases: []model.TestCase{{
				Name: "c",
				Steps: []model.Step{{
					Name: "one", Protocol: "graphql",
					Request: map[string]any{
						"url":   srv.URL,
						"query": `query { user(id: "1") { id name } }`,
					},
					Assertions: []model.Assertion{{Type: "graphql_schema"}},
				}},
			}},
		}},
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status == "passed" {
		t.Fatal("introspection failure should fail the assertion")
	}
	for _, s := range res.Suites {
		for _, c := range s.Cases {
			for _, st := range c.Steps {
				for _, a := range st.Assertions {
					if strings.Contains(a.Message, token) || strings.Contains(a.Message, "Authorization") {
						t.Fatalf("leaked: %s", a.Message)
					}
				}
			}
		}
	}
}

const miniIntro = `{"data":{"__schema":{"queryType":{"name":"Query"},"types":[{"kind":"OBJECT","name":"Query","fields":[{"name":"user","args":[{"name":"id","type":{"kind":"NON_NULL","name":null,"ofType":{"kind":"SCALAR","name":"ID","ofType":null}},"defaultValue":null}],"type":{"kind":"OBJECT","name":"User","ofType":null},"isDeprecated":false}]},{"kind":"OBJECT","name":"User","fields":[{"name":"id","args":[],"type":{"kind":"NON_NULL","name":null,"ofType":{"kind":"SCALAR","name":"ID","ofType":null}},"isDeprecated":false},{"name":"name","args":[],"type":{"kind":"SCALAR","name":"String","ofType":null},"isDeprecated":false}]}]}}}`
