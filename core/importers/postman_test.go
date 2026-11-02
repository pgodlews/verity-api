package importers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/secretscan"
)

var (
	sampleCollection  = filepath.Join("..", "..", "examples", "specs", "sample.postman_collection.json")
	sampleEnvironment = filepath.Join("..", "..", "examples", "specs", "sample.postman_environment.json")
)

func importSample(t *testing.T, envs ...string) *Result {
	t.Helper()
	res, err := ImportPostman(sampleCollection, envs...)
	if err != nil {
		t.Fatal(err)
	}
	if errs := res.Project.Validate(); len(errs) != 0 {
		t.Fatalf("generated project invalid: %v", errs)
	}
	return res
}

func findStep(t *testing.T, p *model.Project, suite, kase, step string) (model.TestCase, model.Step) {
	t.Helper()
	for _, s := range p.Suites {
		if s.Name != suite {
			continue
		}
		for _, c := range s.Cases {
			if c.Name != kase {
				continue
			}
			for _, st := range c.Steps {
				if st.Name == step {
					return c, st
				}
			}
		}
	}
	t.Fatalf("step %s/%s/%s not found", suite, kase, step)
	return model.TestCase{}, model.Step{}
}

func hasWarning(res *Result, substr string) bool {
	for _, w := range res.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestImportPostmanStructure(t *testing.T) {
	p := importSample(t).Project
	if p.Name != "Widget Shop" {
		t.Fatalf("project name = %q", p.Name)
	}
	var got []string
	for _, s := range p.Suites {
		for _, c := range s.Cases {
			got = append(got, s.Name+" > "+c.Name)
		}
	}
	want := []string{
		"Widget Shop > Widget Shop",
		"Auth > Auth",
		"Widgets > Widgets",
		"Widgets > Widgets / Admin",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("suites/cases:\n got %q\nwant %q", got, want)
	}
	c, _ := findStep(t, p, "Auth", "Auth", "Login")
	if len(c.Steps) != 2 || c.Steps[1].Name != "Me" {
		t.Fatalf("Auth case should hold Login then Me in order, got %+v", c.Steps)
	}
}

func TestImportPostmanRequests(t *testing.T) {
	p := importSample(t).Project

	_, login := findStep(t, p, "Auth", "Auth", "Login")
	if login.Request["method"] != "POST" || login.Request["url"] != "${baseUrl}/login" {
		t.Fatalf("login request: %+v", login.Request)
	}
	if body := login.Request["body"]; body != `{"username": "${user}", "password": "${password}"}` {
		t.Fatalf("login body not rewritten: %v", body)
	}

	_, get := findStep(t, p, "Widgets", "Widgets", "Get widget")
	if get.Request["url"] != "${baseUrl}/widgets/${id}?expand=parts" {
		t.Fatalf("path variable not converted: %v", get.Request["url"])
	}
	if get.Vars["id"] != "42" {
		t.Fatalf("path variable value not carried: %v", get.Vars)
	}
	h := get.Request["headers"].(map[string]any)
	if h["X-Trace"] != "${fn:uuid}" {
		t.Fatalf("dynamic variable should use uuid interpolation, got %v", h["X-Trace"])
	}
	if _, ok := h["X-Debug"]; ok {
		t.Fatal("disabled header imported")
	}

	_, search := findStep(t, p, "Widgets", "Widgets", "Search")
	if search.Protocol != "graphql" || search.Request["query"] == nil {
		t.Fatalf("graphql body not mapped: %+v", search)
	}
	if v := search.Request["variables"].(map[string]any)["q"]; v != "${query}" {
		t.Fatalf("graphql variables not rewritten: %v", v)
	}

	_, upload := findStep(t, p, "Widgets", "Widgets / Admin", "Upload image")
	if upload.Skip {
		t.Fatal("form-data step with a src should be imported runnable")
	}
	parts, ok := upload.Request["multipart"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("multipart: %#v", upload.Request["multipart"])
	}
	part := parts[0].(map[string]any)
	if part["name"] != "file" || part["file"] != "widget.png" || len(part) != 2 {
		t.Fatalf("upload part: %#v", part)
	}
}

func TestImportPostmanAuth(t *testing.T) {
	res := importSample(t)
	p := res.Project

	defs := map[string]model.AuthDef{}
	for _, d := range p.Auth {
		defs[d.Name] = d
	}
	basic, ok := defs["basic"]
	if !ok || basic.Config["username"] != "admin" || basic.Config["password"] != "${env:BASIC_PASSWORD}" {
		t.Fatalf("basic auth: %+v", basic)
	}
	oauth, ok := defs["oauth2-client-credentials"]
	if !ok || oauth.Type != "oauth2_client_credentials" ||
		oauth.Config["tokenUrl"] != "${baseUrl}/oauth/token" ||
		oauth.Config["clientSecret"] != "${partnerSecret}" || oauth.Config["scope"] != "widgets:read" {
		t.Fatalf("oauth2 auth: %+v", oauth)
	}
	if _, ok := defs["bearer"]; ok {
		t.Fatal("bearer over a captured token must be a header, not an AuthDef")
	}

	// Collection bearer {{token}} is captured by Login, so it lands as a header.
	_, me := findStep(t, p, "Auth", "Auth", "Me")
	if h := me.Request["headers"].(map[string]any); h["Authorization"] != "Bearer ${token}" {
		t.Fatalf("captured bearer not injected as header: %v", h)
	}
	// noauth on Login: no Authorization header.
	_, login := findStep(t, p, "Auth", "Auth", "Login")
	if h := login.Request["headers"].(map[string]any); h["Authorization"] != nil {
		t.Fatalf("noauth request got auth header: %v", h)
	}

	// Folder auth lands on the case; a request override lands on the step.
	admin, del := findStep(t, p, "Widgets", "Widgets / Admin", "Delete widget")
	if admin.Auth == nil || !reflect.DeepEqual(admin.Auth.Use, []string{"basic"}) || del.Auth != nil {
		t.Fatalf("folder auth: case=%+v step=%+v", admin.Auth, del.Auth)
	}
	_, partner := findStep(t, p, "Widgets", "Widgets / Admin", "Partner token")
	if partner.Auth == nil || partner.Auth.Inherit == nil || *partner.Auth.Inherit ||
		!reflect.DeepEqual(partner.Auth.Use, []string{"oauth2-client-credentials"}) {
		t.Fatalf("request auth override: %+v", partner.Auth)
	}
}

func TestImportPostmanSecrets(t *testing.T) {
	res := importSample(t, sampleEnvironment)
	p := res.Project

	if len(p.Environments) != 1 || p.Environments[0].Name != "Staging" || !p.Environments[0].Default {
		t.Fatalf("environments: %+v", p.Environments)
	}
	vars := p.Environments[0].Variables
	if vars["baseUrl"] != "https://staging.widgets.example" {
		t.Fatalf("environment value should override collection variable, got %q", vars["baseUrl"])
	}
	if vars["apiKey"] != "${env:APIKEY}" || vars["password"] != "${env:PASSWORD}" {
		t.Fatalf("secrets not moved to env refs: apiKey=%q password=%q", vars["apiKey"], vars["password"])
	}
	if _, ok := vars["legacy"]; ok {
		t.Fatal("disabled environment value imported")
	}
	if _, ok := vars["unused"]; ok {
		t.Fatal("disabled collection variable imported")
	}

	_, partner := findStep(t, p, "Widgets", "Widgets / Admin", "Partner token")
	form := partner.Request["form"].(map[string]any)
	if form["signing_secret"] != "${env:SIGNING_SECRET}" || form["note"] != "hello world" {
		t.Fatalf("form secret not scrubbed: %v", form)
	}

	// No literal secret from the fixtures may reach the project or warnings.
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	all := string(out) + strings.Join(res.Warnings, "\n")
	for _, lit := range []string{"hunter2hunter2", "demo-api-key-not-a-real-secret", "s3cret", "abc123"} {
		if strings.Contains(all, lit) {
			t.Fatalf("literal secret %q leaked into the import output", lit)
		}
	}
}

func TestImportPostmanScriptsAndWarnings(t *testing.T) {
	res := importSample(t)
	p := res.Project

	_, login := findStep(t, p, "Auth", "Auth", "Login")
	wantLogin := []model.Assertion{
		{Type: "header", Expr: "Content-Type", Op: "exists", Description: "responds with a content type"},
		{Type: "status", Value: 200, Description: "Status code is 200"},
	}
	if !reflect.DeepEqual(login.Assertions, wantLogin) {
		t.Fatalf("login assertions:\n got %+v\nwant %+v", login.Assertions, wantLogin)
	}
	if !reflect.DeepEqual(login.Transfers, []model.TransferSpec{{Name: "token", From: "jsonpath", Expr: "$.access_token", Scope: model.TransferScopeRun}}) {
		t.Fatalf("login transfers (Widgets uses the token, so it is run-scoped): %+v", login.Transfers)
	}

	_, me := findStep(t, p, "Auth", "Auth", "Me")
	if len(me.Assertions) != 3 ||
		!reflect.DeepEqual(me.Assertions[1], model.Assertion{Type: "jsonpath", Expr: "$.user.name", Op: "eq", Value: "ada", Description: "is ada"}) ||
		!reflect.DeepEqual(me.Assertions[2], model.Assertion{Type: "jsonpath", Expr: "$.user.roles[0]", Op: "exists", Description: "is ada"}) {
		t.Fatalf("me assertions: %+v", me.Assertions)
	}

	_, get := findStep(t, p, "Widgets", "Widgets", "Get widget")
	if !reflect.DeepEqual(get.Assertions[1:], []model.Assertion{
		{Type: "response_time", Op: "lt", Value: 1000},
		{Type: "contains", Value: "widget"},
	}) {
		t.Fatalf("get widget assertions: %+v", get.Assertions)
	}

	for _, w := range []string{
		"Auth/Me: 1 test-script line(s) not converted (first: console.log(body);)",
		"Widgets/Get widget: pre-request script not converted",
		"auth \"basic\": literal password not imported; set environment variable BASIC_PASSWORD",
	} {
		if !hasWarning(res, w) {
			t.Errorf("missing warning %q in:\n%s", w, strings.Join(res.Warnings, "\n"))
		}
	}
	if hasWarning(res, "uses ${token}") {
		t.Errorf("a run-scoped capture needs no warning:\n%s", strings.Join(res.Warnings, "\n"))
	}
	if hasWarning(res, "formdata body is not supported") {
		t.Errorf("formdata with a src should convert, warnings:\n%s", strings.Join(res.Warnings, "\n"))
	}
	if hasWarning(res, "dynamic variable {{$guid}}") {
		t.Fatal("supported dynamic variable should not produce a warning")
	}
}

func TestRewritePostmanDynamicVariables(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		warning bool
	}{
		{input: "{{$guid}}", want: "${fn:uuid}"},
		{input: "{{$randomUUID}}", want: "${fn:uuid}"},
		{input: "{{$timestamp}}", want: "${fn:timestamp}"},
		{input: "{{$isoTimestamp}}", want: "${fn:isoTimestamp}"},
		{input: "{{$randomInt}}", want: "${fn:randomInt}"},
		{input: "{{$randomColor}}", want: "{{$randomColor}}", warning: true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			imp := &pmImporter{res: &Result{}, warned: map[string]bool{}}
			if got := imp.rewriteVars(tc.input, "test"); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if got := len(imp.res.Warnings) != 0; got != tc.warning {
				t.Fatalf("warning presence = %v, want %v (%q)", got, tc.warning, imp.res.Warnings)
			}
		})
	}
}

func TestConvertTestScriptIdioms(t *testing.T) {
	cases := []struct {
		line string
		want model.Assertion
	}{
		{"pm.response.to.have.status(201);", model.Assertion{Type: "status", Value: 201}},
		{"pm.expect(pm.response.code).to.eql(204)", model.Assertion{Type: "status", Value: 204}},
		{"pm.response.to.be.ok;", model.Assertion{Type: "status", Value: 200}},
		{"pm.expect(pm.response.code).to.be.oneOf([200, 201]);", model.Assertion{Type: "status", Op: "matches", Value: "^(200|201)$"}},
		{"pm.expect(pm.response.responseTime).to.be.at.most(250);", model.Assertion{Type: "response_time", Op: "lte", Value: 250}},
		{`pm.response.to.have.header("ETag", "abc");`, model.Assertion{Type: "header", Expr: "ETag", Op: "eq", Value: "abc"}},
		{`pm.response.to.have.body('pong');`, model.Assertion{Type: "regex", Value: "^pong$"}},
		{`pm.expect(pm.response.json().items[0]["display name"]).to.equal(3); // count`, model.Assertion{Type: "jsonpath", Expr: "$.items[0]['display name']", Op: "eq", Value: 3}},
		{"pm.expect(pm.response.json().gone).to.not.exist;", model.Assertion{Type: "jsonpath", Expr: "$.gone", Op: "not_exists"}},
	}
	for _, tc := range cases {
		r := convertTestScript([]string{tc.line})
		if len(r.Unconverted) != 0 || len(r.Assertions) != 1 || !reflect.DeepEqual(r.Assertions[0], tc.want) {
			t.Errorf("%s:\n got %+v (unconverted %q)\nwant %+v", tc.line, r.Assertions, r.Unconverted, tc.want)
		}
	}

	r := convertTestScript([]string{"pm.response.to.be.success;"})
	if len(r.Assertions) != 2 {
		t.Fatalf("success should give a 2xx range, got %+v", r.Assertions)
	}

	transfers := []struct {
		line string
		want model.TransferSpec
	}{
		{`pm.collectionVariables.set("id", pm.response.json().data.id);`, model.TransferSpec{Name: "id", From: "jsonpath", Expr: "$.data.id"}},
		{`postman.setEnvironmentVariable('etag', postman.getResponseHeader('ETag'));`, model.TransferSpec{Name: "etag", From: "header", Expr: "ETag"}},
		{`pm.globals.set("code", pm.response.code);`, model.TransferSpec{Name: "code", From: "status"}},
		{`pm.variables.set("raw", pm.response.text());`, model.TransferSpec{Name: "raw", From: "body"}},
	}
	for _, tc := range transfers {
		r := convertTestScript([]string{tc.line})
		if len(r.Transfers) != 1 || !reflect.DeepEqual(r.Transfers[0], tc.want) {
			t.Errorf("%s: got %+v (unconverted %q)", tc.line, r.Transfers, r.Unconverted)
		}
	}

	// A set() whose value is not a response accessor is not a transfer.
	r = convertTestScript([]string{`var n = 1;`, `pm.environment.set("n", n + 1);`, `if (x) {`, `}`})
	if len(r.Transfers) != 0 || len(r.Unconverted) != 3 {
		t.Fatalf("expected 3 unconverted lines, got %+v", r)
	}
}

func TestImportPostmanRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := ImportPostman(write("v1.json", `{"id":"x","name":"old","requests":[]}`)); err == nil || !strings.Contains(err.Error(), "v1") {
		t.Fatalf("v1 collection should be rejected with a re-export hint, got %v", err)
	}
	if _, err := ImportPostman(write("empty.json", `{"info":{"name":"e","schema":"v2.1"},"item":[]}`)); err == nil {
		t.Fatal("collection without requests should fail")
	}
	// API-wrapped collections ({"collection": ...}) and string URLs are accepted.
	res, err := ImportPostman(write("wrapped.json",
		`{"collection":{"info":{"name":"w"},"item":[{"name":"ping","request":"https://example.com/ping"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	st := res.Project.Suites[0].Cases[0].Steps[0]
	if st.Request["url"] != "https://example.com/ping" || st.Request["method"] != "GET" {
		t.Fatalf("string request: %+v", st.Request)
	}
}

// TestImportPostmanRuns executes the imported Auth flow against a local
// server: login captures a token that the next request sends as a bearer.
func TestImportPostmanRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			io.WriteString(w, `{"ok":true}`)
		case "/login":
			var in struct{ Username, Password string }
			json.NewDecoder(r.Body).Decode(&in)
			if in.Username != "ada" || in.Password != "pw-from-env" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"access_token":"tok-123"}`)
		case "/me":
			if r.Header.Get("Authorization") != "Bearer tok-123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"user":{"name":"ada","roles":["admin"]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("PASSWORD", "pw-from-env")
	t.Setenv("APIKEY", "unused")
	p := importSample(t, sampleEnvironment).Project
	p.Suites = p.Suites[:2] // root (Health) + Auth

	res, err := runner.New(bootstrap.Default()).Run(context.Background(), p, runner.Options{
		ExtraVars: map[string]string{"baseUrl": srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status != "passed" {
		b, _ := json.MarshalIndent(res, "", "  ")
		t.Fatalf("imported Postman flow did not pass:\n%s", b)
	}
}

// TestImportPostmanReviewRegressions covers findings from the 2026-10-30
// code review: name collisions, path-variable suffixes, secrets in raw
// URLs/bodies/GraphQL variables/warnings, and GraphQL + query API keys.
func TestImportPostmanReviewRegressions(t *testing.T) {
	col := `{"info":{"name":"r","schema":"v2.1"},"item":[
	  {"name":"A","item":[{"name":"a1","request":"https://x.test/a"}]},
	  {"name":"A (2)","item":[{"name":"a2","request":"https://x.test/b"}]},
	  {"name":"A","item":[{"name":"a3","request":"https://x.test/c"}]},
	  {"name":"Login","event":[{"listen":"test","script":{"exec":["pm.environment.set(\"key\", pm.response.json().key);"]}}],
	   "request":{"method":"POST","url":{"raw":"https://x.test/login?token=abcd1234&debug=1","query":[{"key":"debug","value":"1","disabled":true}]},
	     "body":{"mode":"raw","raw":"{\"user\":\"ada\",\"password\":\"hunter2\"}"}}},
	  {"name":"File","request":"https://x.test/users/:id.json"},
	  {"name":"Leaky script","event":[{"listen":"test","script":{"exec":["pm.environment.set(\"token\", \"sup3rs3cret\");"]}}],"request":"https://x.test/s"},
	  {"name":"GQL","request":{"method":"POST","url":"https://x.test/graphql",
	     "auth":{"type":"apikey","apikey":[{"key":"key","value":"api_key"},{"key":"value","value":"{{key}}"},{"key":"in","value":"query"}]},
	     "body":{"mode":"graphql","graphql":{"query":"{ me { id } }","variables":"{\"password\": \"pw-literal\"}"}}}}
	]}`
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	if err := os.WriteFile(path, []byte(col), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportPostman(path)
	if err != nil {
		t.Fatal(err)
	}
	p := res.Project
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("invalid project (duplicate names?): %v", errs)
	}
	var names []string
	for _, s := range p.Suites {
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, []string{"r", "A", "A (2)", "A (3)"}) {
		t.Fatalf("suite names: %q", names)
	}
	steps := map[string]model.Step{}
	for _, st := range p.Suites[0].Cases[0].Steps {
		steps[st.Name] = st
	}
	if u := steps["Login"].Request["url"]; u != "https://x.test/login?token=${env:TOKEN}" {
		t.Errorf("raw URL query secret / disabled param: %v", u)
	}
	if b := steps["Login"].Request["body"]; b != `{"user":"ada","password":"${env:PASSWORD}"}` {
		t.Errorf("body secret not scrubbed: %v", b)
	}
	if u := steps["File"].Request["url"]; u != "https://x.test/users/${id}.json" {
		t.Errorf("path variable suffix: %v", u)
	}
	gql := steps["GQL"]
	if gql.Protocol != "graphql" || gql.Request["query"] != "{ me { id } }" || gql.Request["url"] != "https://x.test/graphql?api_key=${key}" {
		t.Errorf("graphql + captured query key: %+v", gql.Request)
	}
	if v := gql.Request["variables"].(map[string]any)["password"]; v != "${env:PASSWORD_2}" {
		t.Errorf("graphql variable secret: %v", v)
	}
	out, _ := json.Marshal(p)
	all := string(out) + strings.Join(res.Warnings, "\n")
	for _, lit := range []string{"abcd1234", "hunter2", "sup3rs3cret", "pw-literal"} {
		if strings.Contains(all, lit) {
			t.Errorf("literal %q leaked into project or warnings", lit)
		}
	}
}

func TestImportPostmanFormData(t *testing.T) {
	col := `{
	  "info": {"name": "forms", "schema": "v2.1"},
	  "item": [
	    {"name": "Mixed", "request": {"method": "POST", "url": "/up", "body": {"mode": "formdata", "formdata": [
	      {"key": "payload", "type": "text", "value": "{\"password\":\"inner-secret\",\"ok\":\"{{id}}\"}"},
	      {"key": "note", "value": "hello {{name}}"},
	      {"key": "gone", "value": "nope", "disabled": true},
	      {"key": "hidden", "type": "file", "src": "disabled-only-path", "disabled": true},
	      {"key": "docs", "type": "file", "src": ["a.txt", "b.txt"], "contentType": "text/plain"},
	      {"key": "extra", "type": "file", "src": "c.txt"},
	      {"key": "tpl", "type": "file", "src": "{{uploadPath}}"},
	      {"key": "missing", "type": "file", "value": "ignored-file-value"},
	      {"key": "", "value": "orphan"},
	      {"key": "password", "value": "form-secret-value"},
	      {"key": "note2", "value": "abcdEFGH1234ijklMNOP5678"}
	    ]}}},
	    {"name": "Raw file", "request": {"method": "POST", "url": "/raw", "body": {"mode": "file", "file": {"src": "payload.bin"}}}},
	    {"name": "Raw file missing", "request": {"method": "POST", "url": "/raw", "body": {"mode": "file", "file": {}}}},
	    {"name": "Only missing", "request": {"method": "POST", "url": "/nope", "body": {"mode": "formdata", "formdata": [
	      {"key": "file", "type": "file"}
	    ]}}}
	  ]
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	if err := os.WriteFile(path, []byte(col), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportPostman(path)
	if err != nil {
		t.Fatal(err)
	}
	if errs := res.Project.Validate(); len(errs) != 0 {
		t.Fatalf("invalid project: %v", errs)
	}
	steps := map[string]model.Step{}
	for _, st := range res.Project.Suites[0].Cases[0].Steps {
		steps[st.Name] = st
	}
	mixed := steps["Mixed"]
	if mixed.Skip {
		t.Fatal("mixed formdata should stay runnable")
	}
	want := []any{
		map[string]any{"name": "payload", "value": `{"password":"${env:PASSWORD}","ok":"${id}"}`},
		map[string]any{"name": "note", "value": "hello ${name}"},
		map[string]any{"name": "docs", "file": "a.txt", "contentType": "text/plain"},
		map[string]any{"name": "docs", "file": "b.txt", "contentType": "text/plain"},
		map[string]any{"name": "extra", "file": "c.txt"},
		map[string]any{"name": "tpl", "file": "${uploadPath}"},
		map[string]any{"name": "password", "value": "${env:PASSWORD_2}"},
		map[string]any{"name": "note2", "value": "${env:NOTE2}"},
	}
	if !reflect.DeepEqual(mixed.Request["multipart"], want) {
		t.Fatalf("multipart\n got %#v\nwant %#v", mixed.Request["multipart"], want)
	}
	raw := steps["Raw file"]
	if raw.Skip || raw.Request["bodyFile"] != "payload.bin" {
		t.Fatalf("file body: skip=%v request=%v", raw.Skip, raw.Request)
	}
	missing := steps["Raw file missing"]
	if !missing.Skip || missing.Request["bodyFile"] != nil {
		t.Fatalf("file body without src: skip=%v request=%v", missing.Skip, missing.Request)
	}
	only := steps["Only missing"]
	if !only.Skip || only.Request["multipart"] != nil {
		t.Fatalf("unusable formdata: skip=%v request=%v", only.Skip, only.Request)
	}
	for _, w := range []string{
		`Mixed: formdata file part "missing" has no src`,
		"Mixed: formdata part has no key; skipped",
		`Mixed: body field "password" held a literal credential; set environment variable PASSWORD`,
		`Mixed: formdata field "password" held a literal credential; set environment variable PASSWORD_2`,
		`Mixed: formdata field "note2" held a literal credential; set environment variable NOTE2`,
		"Raw file missing: file body has no src; step imported with skip: true",
		`Only missing: formdata file part "file" has no src`,
		"Only missing: formdata body has no usable parts; step imported with skip: true",
	} {
		if !hasWarning(res, w) {
			t.Errorf("missing warning %q in:\n%s", w, strings.Join(res.Warnings, "\n"))
		}
	}
	out, _ := json.Marshal(res.Project)
	all := string(out) + strings.Join(res.Warnings, "\n")
	for _, lit := range []string{"inner-secret", "form-secret-value", "abcdEFGH1234ijklMNOP5678", "disabled-only-path", "ignored-file-value"} {
		if strings.Contains(all, lit) {
			t.Errorf("literal %q leaked into project or warnings", lit)
		}
	}
}

func TestImportPostmanMixedTemplateCredentials(t *testing.T) {
	col := `{"info":{"name":"m","schema":"v2.1"},"item":[{"name":"r","request":{"method":"POST",
	  "url":"https://x.test/a?api_key=k3y{{n}}",
	  "header":[{"key":"Authorization","value":"Bearer t0ken{{v}}"},{"key":"X-Ok","value":"Bearer {{token}}"}],
	  "auth":{"type":"basic","basic":[{"key":"username","value":"u"},{"key":"password","value":"pw{{suffix}}"}]},
	  "body":{"mode":"formdata","formdata":[{"key":"password","type":"text","value":"hunter2{{SUFFIX}}"}]}}}]}`
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(col), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportPostman(path)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(res.Project)
	all := string(out) + strings.Join(res.Warnings, "\n")
	for _, lit := range []string{"hunter2", "k3y", "t0ken", `"pw`} {
		if strings.Contains(all, lit) {
			t.Errorf("literal fragment %q leaked:\n%s", lit, all)
		}
	}
	if !strings.Contains(string(out), `Bearer ${token}`) {
		t.Errorf("a pure template header must stay a reference: %s", out)
	}
}

func TestPromoteCrossCaseCapturesOrder(t *testing.T) {
	step := func(url string, tr ...model.TransferSpec) model.Step {
		return model.Step{Name: "s", Protocol: "rest", Request: map[string]any{"url": url}, Transfers: tr}
	}
	p := &model.Project{Suites: []model.Suite{
		{Name: "A", Cases: []model.TestCase{{Name: "A", Steps: []model.Step{step("http://x/${late}")}}}},
		{Name: "B", Cases: []model.TestCase{{Name: "B", Steps: []model.Step{
			step("http://x/login", model.TransferSpec{Name: "late", From: "body"}, model.TransferSpec{Name: "own", From: "body"}),
			step("http://x/${own}"),
		}}}},
	}}
	imp := &pmImporter{res: &Result{}, warned: map[string]bool{}}
	imp.promoteCrossCaseCaptures(p)
	for _, tr := range p.Suites[1].Cases[0].Steps[0].Transfers {
		if tr.Scope != "" {
			t.Fatalf("%s: used only earlier or in its own case, must stay case-scoped: %+v", tr.Name, tr)
		}
	}
	if len(imp.res.Warnings) != 1 || !strings.Contains(imp.res.Warnings[0], "A/A: uses ${late}, which is only captured later, in B/B") {
		t.Fatalf("warnings: %q", imp.res.Warnings)
	}
}

// A case that reads a value before capturing it itself still needs the
// earlier case's capture, so that one is run-scoped.
func TestPromoteCrossCaseCapturesUseBeforeOwnCapture(t *testing.T) {
	step := func(url string, tr ...model.TransferSpec) model.Step {
		return model.Step{Name: "s", Protocol: "rest", Request: map[string]any{"url": url}, Transfers: tr}
	}
	p := &model.Project{Suites: []model.Suite{
		{Name: "A", Cases: []model.TestCase{{Name: "A", Steps: []model.Step{step("http://x/login", model.TransferSpec{Name: "tok", From: "body"})}}}},
		{Name: "B", Cases: []model.TestCase{{Name: "B", Steps: []model.Step{
			step("http://x/${tok}"),
			step("http://x/refresh", model.TransferSpec{Name: "tok", From: "body"}),
		}}}},
	}}
	imp := &pmImporter{res: &Result{}, warned: map[string]bool{}}
	imp.promoteCrossCaseCaptures(p)
	if got := p.Suites[0].Cases[0].Steps[0].Transfers[0].Scope; got != model.TransferScopeRun {
		t.Fatalf("A's capture scope = %q, want run", got)
	}
	if got := p.Suites[1].Cases[0].Steps[1].Transfers[0].Scope; got != "" {
		t.Fatalf("B's own later capture should stay case-scoped, got %q", got)
	}
}

func TestImportPostmanSavedExamplesRedactsCredentials(t *testing.T) {
	const secret = "hunter2-example-secret"
	col := `{"info":{"name":"Examples","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
	  "item":[{"name":"Get widget","request":{"method":"GET","url":{"raw":"{{baseUrl}}/widgets/{{id}}","path":["widgets","{{id}}"]}},
	    "response":[{"name":"OK","code":200,
	      "originalRequest":{"method":"GET","url":{"raw":"{{baseUrl}}/widgets/{{id}}","path":["widgets","{{id}}"]}},
	      "header":[{"key":"Content-Type","value":"application/json"},{"key":"Authorization","value":"Bearer ` + secret + `"}],
	      "body":"{\"id\":\"{{id}}\",\"name\":\"widget\",\"password\":\"` + secret + `\"}"}]}]}`
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	if err := os.WriteFile(path, []byte(col), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportPostman(path)
	if err != nil {
		t.Fatal(err)
	}
	if errs := res.Project.Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(res.Project.Mocks) != 1 {
		t.Fatalf("mocks: %+v", res.Project.Mocks)
	}
	m := res.Project.Mocks[0]
	if m.Method != "GET" || m.Path != "/widgets/{id}" || m.Status != 200 {
		t.Fatalf("mock route: %+v", m)
	}
	if m.Headers["Content-Type"] != "application/json" || m.Headers["Authorization"] != "REDACTED" {
		t.Fatalf("headers: %v", m.Headers)
	}
	if strings.Contains(m.Body, secret) || !strings.Contains(m.Body, "REDACTED") || !strings.Contains(m.Body, "${id}") {
		t.Fatalf("body: %s", m.Body)
	}
	warned := false
	for _, w := range res.Warnings {
		if strings.Contains(w, secret) {
			t.Fatalf("warning contains the credential: %s", w)
		}
		if strings.Contains(w, "saved example") && strings.Contains(w, "REDACTED") && strings.Contains(w, "Get widget") && !strings.Contains(w, "OK") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("warnings: %q", res.Warnings)
	}
	raw, err := res.Project.Marshal("project.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("credential written into the project")
	}
	findings, err := secretscan.ScanFile("project.yaml", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("scan findings: %+v", findings)
	}
}

func TestImportPostmanSavedExampleScrubsPathURLAndName(t *testing.T) {
	const (
		pathSecret = "sk_live_51HhExampleSecretValue"
		urlSecret  = "s3cr3t-password"
		nameSecret = "sk_live_NameShouldNotBePrinted"
	)
	col := `{"info":{"name":"Examples","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
	  "item":[{"name":"Get widget","request":{"method":"GET","url":"http://example.com/widgets"},
	    "response":[{"name":"` + nameSecret + `","code":200,
	      "originalRequest":{"method":"GET","url":{"path":["api","` + pathSecret + `"]}},
	      "header":[{"key":"Location","value":"https://user:` + urlSecret + `@example.com/cb"}],
	      "body":"{\"url\":\"https://user:` + urlSecret + `@example.com/hook\",\"note\":\"key AKIAIOSFODNN7EXAMPLE here\"}"}]}]}`
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	if err := os.WriteFile(path, []byte(col), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportPostman(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Project.Mocks) != 1 {
		t.Fatalf("mocks: %+v", res.Project.Mocks)
	}
	m := res.Project.Mocks[0]
	if m.Path != "/api/REDACTED" {
		t.Fatalf("path: %s", m.Path)
	}
	if strings.Contains(m.Path, pathSecret) || strings.Contains(m.Body, urlSecret) || strings.Contains(m.Body, "AKIA") {
		t.Fatalf("secret written into mock: %+v", m)
	}
	if m.Headers["Location"] != "REDACTED" {
		t.Fatalf("location: %v", m.Headers)
	}
	if !strings.Contains(m.Body, "REDACTED") {
		t.Fatalf("body: %s", m.Body)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, pathSecret) || strings.Contains(w, urlSecret) || strings.Contains(w, nameSecret) || strings.Contains(w, "AKIA") {
			t.Fatalf("warning echoes a credential: %s", w)
		}
	}
	raw, err := res.Project.Marshal("project.yaml")
	if err != nil {
		t.Fatal(err)
	}
	findings, err := secretscan.ScanFile("project.yaml", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("scan findings: %+v", findings)
	}
}
