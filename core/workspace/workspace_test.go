package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

const rootYAML = `# Widget API tests — keep this comment
schemaVersion: "1.0"
name: Widgets
environments:
  - name: local
    default: true
    variables:
      BASE_URL: http://127.0.0.1:1
      TOKEN: ${env:VERITY_TEST_TOKEN}
  - name: prod
    variables:
      TOKEN: ${env:VERITY_TEST_TOKEN}
auth:
  - name: partner
    type: bearer
    config:
      token: ${partnerSecret}
include:
  - more.yaml
suites:
  - name: Smoke
    cases:
      - name: login
        steps:
          # the login step
          - name: Login
            protocol: rest
            request: {method: POST, url: "${BASE_URL}/login"}
            transfers:
              - {name: token, from: jsonpath, expr: $.token}
          - name: Me
            protocol: rest
            request:
              method: GET
              url: ${BASE_URL}/me   # inline comment
              headers:
                Authorization: Bearer ${token}
`

const moreYAML = `suites:
  - name: Extra
    cases:
      - name: other
        steps:
          - name: Uses token
            protocol: rest
            request:
              url: ${BASE_URL}/x?t=${token}
`

func writeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(rootYAML), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "more.yaml"), []byte(moreYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "project.yaml")
}

func TestSaveStepKeepsCommentsAndLayout(t *testing.T) {
	path := writeProject(t)
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	step := model.Step{Name: "Me", Protocol: "rest", Request: map[string]any{
		"method": "GET", "url": "${BASE_URL}/me?verbose=1",
	}, Assertions: []model.Assertion{{Type: "status", Value: 200}}}
	res, err := SaveStep(SaveStepRequest{Path: path, Suite: "Smoke", Case: "login", Index: 1, Step: step})
	if err != nil {
		t.Fatal(err)
	}
	if res.File != path {
		t.Fatalf("saved to %s, want %s", res.File, path)
	}
	data, _ := os.ReadFile(path)
	out := string(data)
	for _, want := range []string{"# Widget API tests — keep this comment", "# the login step", "${BASE_URL}/me?verbose=1", "\n  - name: local"} {
		if !strings.Contains(out, want) {
			t.Errorf("saved file lost %q:\n%s", want, out)
		}
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != beforeInfo.Mode().Perm() {
		t.Errorf("file mode changed to %v", st.Mode().Perm())
	}
	p, err := model.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Suites[0].Cases[0].Steps[1]
	if got.Request["url"] != "${BASE_URL}/me?verbose=1" || len(got.Assertions) != 1 {
		t.Fatalf("step not saved: %+v", got)
	}
	if p.Suites[0].Cases[0].Steps[0].Name != "Login" {
		t.Fatal("neighbouring step changed")
	}
}

func TestSaveStepFindsIncludedFileAndRejectsInvalid(t *testing.T) {
	path := writeProject(t)
	more := filepath.Join(filepath.Dir(path), "more.yaml")
	step := model.Step{Name: "Renamed", Protocol: "rest", Request: map[string]any{"url": "${BASE_URL}/y"}}
	res, err := SaveStep(SaveStepRequest{Path: path, Suite: "Extra", Case: "other", Index: 0, Step: step})
	if err != nil {
		t.Fatal(err)
	}
	if res.File != more {
		t.Fatalf("saved to %s, want the include %s", res.File, more)
	}

	before, _ := os.ReadFile(path)
	bad := model.Step{Name: "Me", Request: map[string]any{"url": "x"}, Auth: &model.AuthRef{Use: []string{"nope"}}}
	if _, err := SaveStep(SaveStepRequest{Path: path, Suite: "Smoke", Case: "login", Index: 1, Step: bad}); err == nil {
		t.Fatal("an edit referencing an unknown auth must be rejected")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("rejected edit was not rolled back")
	}
	if _, err := SaveStep(SaveStepRequest{Path: path, Suite: "Smoke", Case: "nope", Index: 0, Step: step}); err == nil {
		t.Fatal("unknown case must be an error")
	}
}

func TestInspect(t *testing.T) {
	path := writeProject(t)
	t.Setenv("VERITY_TEST_TOKEN", "x")
	info, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.EnvRefs) != 1 || info.EnvRefs[0].Name != "VERITY_TEST_TOKEN" || !info.EnvRefs[0].Set {
		t.Fatalf("envRefs: %+v", info.EnvRefs)
	}
	byName := map[string]UndefinedVar{}
	for _, u := range info.UndefinedVars {
		byName[u.Name] = u
	}
	// BASE_URL exists only in local; token is captured in Smoke/login but
	// used in Extra/other; partnerSecret is never defined.
	if u := byName["BASE_URL"]; len(u.MissingIn) != 1 || u.MissingIn[0] != "prod" {
		t.Errorf("BASE_URL: %+v", u)
	}
	if u := byName["token"]; len(u.UsedIn) != 1 || u.UsedIn[0] != "Extra/other/Uses token" {
		t.Errorf("token should be undefined only where it is used outside its case: %+v", u)
	}
	if u := byName["partnerSecret"]; len(u.MissingIn) != 2 || u.UsedIn[0] != "auth partner" {
		t.Errorf("partnerSecret: %+v", u)
	}
	if len(info.Captures) != 1 || info.Captures[0].Step != "Smoke/login/Login" {
		t.Errorf("captures: %+v", info.Captures)
	}
}

func TestImportPreviewAndSave(t *testing.T) {
	spec := filepath.Join("..", "..", "examples", "specs", "sample.postman_collection.json")
	pv, err := Preview("postman", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Stats.Suites != 3 || pv.Stats.Steps != 8 || len(pv.Warnings) == 0 || !strings.Contains(pv.YAML, "name: Widget Shop") {
		t.Fatalf("preview: %+v", pv.Stats)
	}
	out := filepath.Join(t.TempDir(), "shop.yaml")
	got, err := Save(ImportSaveRequest{Kind: "postman", Spec: spec, Out: out})
	if err != nil || got != out {
		t.Fatalf("save: %v %s", err, got)
	}
	if _, err := Save(ImportSaveRequest{Kind: "postman", Spec: spec, Out: out}); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("second save should refuse to overwrite, got %v", err)
	}
	if _, err := Save(ImportSaveRequest{Kind: "postman", Spec: spec, Out: out, Overwrite: true}); err != nil {
		t.Fatal(err)
	}
}

func TestImportSoapUIPreview(t *testing.T) {
	spec := filepath.Join("..", "..", "examples", "specs", "sample.soapui.xml")
	pv, err := Preview("soapui", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Stats.Suites != 1 || pv.Stats.Steps != 2 || len(pv.Warnings) == 0 || !strings.Contains(pv.YAML, "Sample API") {
		t.Fatalf("preview: %+v warnings %d", pv.Stats, len(pv.Warnings))
	}
	if !strings.Contains(pv.YAML, "protocol: soap") || !strings.Contains(pv.YAML, "protocol: rest") {
		t.Fatal("preview YAML missing soap and rest steps")
	}
}

func TestSaveEnvironment(t *testing.T) {
	path := writeProject(t)
	yes := true
	res, err := SaveEnvironment(SaveEnvironmentRequest{Path: path, Name: "prod",
		Variables: map[string]string{"BASE_URL": "https://prod.example", "TOKEN": "${env:VERITY_TEST_TOKEN}"}, Default: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if res.File != path {
		t.Fatalf("saved to %s", res.File)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# Widget API tests — keep this comment") {
		t.Fatal("comment lost")
	}
	p, err := model.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var def []string
	for _, e := range p.Environments {
		if e.Default {
			def = append(def, e.Name)
		}
	}
	if len(def) != 1 || def[0] != "prod" || p.Environments[1].Variables["BASE_URL"] != "https://prod.example" {
		t.Fatalf("environments after save: %+v", p.Environments)
	}

	if _, err := SaveEnvironment(SaveEnvironmentRequest{Path: path, Name: "staging", Create: true,
		Variables: map[string]string{"BASE_URL": "https://staging.example"}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := model.Load(path); len(p.Environments) != 3 || p.Environments[2].Name != "staging" {
		t.Fatalf("create: %+v", p.Environments)
	}
	if _, err := SaveEnvironment(SaveEnvironmentRequest{Path: path, Name: "staging", Create: true}); err == nil {
		t.Fatal("duplicate create must fail")
	}

	before, _ := os.ReadFile(path)
	if _, err := SaveEnvironment(SaveEnvironmentRequest{Path: path, Name: "local",
		Variables: map[string]string{"API_TOKEN": "hunter2hunter2"}}); err == nil || !strings.Contains(err.Error(), "env:") {
		t.Fatalf("a literal credential must be refused, got %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("refused save changed the file")
	}
	if _, err := SaveEnvironment(SaveEnvironmentRequest{Path: path, Name: "nope", Variables: map[string]string{}}); err == nil {
		t.Fatal("unknown environment must fail")
	}
}

func TestSaveEnvironmentRefusesLiteralAroundReference(t *testing.T) {
	path := writeProject(t)
	for _, vars := range []map[string]string{
		{"PASSWORD": "hunter2${x}"},
		{"CREDENTIAL": "hunter2"},
	} {
		if _, err := SaveEnvironment(SaveEnvironmentRequest{Path: path, Name: "local", Variables: vars}); err == nil {
			t.Errorf("%v: literal credential accepted", vars)
		}
	}
	if _, err := SaveEnvironment(SaveEnvironmentRequest{Path: path, Name: "local",
		Variables: map[string]string{"PASSWORD": "${env:PW}", "BASE_URL": "http://x"}}); err != nil {
		t.Fatalf("pure reference refused: %v", err)
	}
}

func TestInspectRunScopedCaptureInSetupHook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	yaml := `schemaVersion: "1.0"
name: hooks
environments: [{name: local, default: true}]
suites:
  - name: s
    setup:
      name: login
      steps:
        - name: token
          protocol: rest
          request: {url: "http://127.0.0.1:1/login"}
          transfers: [{name: token, from: body, scope: run}]
    cases:
      - name: c
        steps:
          - name: use
            protocol: rest
            request: {url: "http://127.0.0.1:1/x?t=${token}"}
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range info.UndefinedVars {
		if u.Name == "token" {
			t.Fatalf("token is captured by the setup hook, not undefined: %+v", u)
		}
	}
	if len(info.Captures) != 1 || info.Captures[0].Step != "s/setup:login/token" || info.Captures[0].Scope != "run" {
		t.Fatalf("captures: %+v", info.Captures)
	}
}

// Saving a step in a JSON project must not round numbers elsewhere in it.
func TestSaveStepJSONKeepsLargeNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.json")
	src := `{"schemaVersion":"1.0","name":"n","environments":[{"name":"e","default":true}],"suites":[{"name":"s","cases":[{"name":"c","steps":[` +
		`{"name":"a","protocol":"rest","request":{"url":"http://x","json":{"id":9007199254740993}}},` +
		`{"name":"b","protocol":"rest","request":{"url":"http://x"}}]}]}]}`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	step := model.Step{Name: "b", Protocol: "rest", Request: map[string]any{"url": "http://y"}}
	if _, err := SaveStep(SaveStepRequest{Path: path, Suite: "s", Case: "c", Index: 1, Step: step}); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("large number rounded:\n%s", out)
	}
}
