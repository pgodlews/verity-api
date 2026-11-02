package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	content := `
schemaVersion: "1.0"
name: Test
environments:
  - name: dev
    default: true
    variables:
      BASE_URL: http://localhost
suites:
  - name: s1
    cases:
      - name: c1
        steps:
          - name: step1
            protocol: rest
            request:
              url: "${BASE_URL}/x"
            assertions:
              - type: status
                value: 200
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("expected valid, got %v", errs)
	}
	env, err := p.SelectEnvironment("")
	if err != nil {
		t.Fatal(err)
	}
	if env.Name != "dev" {
		t.Fatalf("expected default env dev, got %q", env.Name)
	}
}

func TestValidateMissingFields(t *testing.T) {
	p := &Project{}
	errs := p.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation errors for empty project")
	}
	// Should report missing schemaVersion, name, and suites.
	codes := map[string]bool{}
	for _, e := range errs {
		codes[e.Path] = true
	}
	if !codes["schemaVersion"] || !codes["name"] || !codes["suites"] {
		t.Fatalf("missing expected validation paths, got %v", codes)
	}
}

func TestValidateUnknownAuthRef(t *testing.T) {
	p := &Project{
		SchemaVersion: "1.0", Name: "x",
		Suites: []Suite{{
			Name: "s", Auth: &AuthRef{Use: []string{"nope"}},
			Cases: []TestCase{{Name: "c", Steps: []Step{{Name: "st", Protocol: "rest"}}}},
		}},
	}
	errs := p.Validate()
	found := false
	for _, e := range errs {
		if string(e.Code) == "not_found" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected not_found error for unknown auth reference")
	}
}

func TestIncludeMerge(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main.yaml")
	frag := filepath.Join(dir, "frag.yaml")
	if err := os.WriteFile(frag, []byte(`
suites:
  - name: extra
    cases:
      - name: c
        steps:
          - name: s
            protocol: rest
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, []byte(`
schemaVersion: "1.0"
name: Main
include:
  - frag.yaml
suites:
  - name: base
    cases:
      - name: c
        steps:
          - name: s
            protocol: rest
`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(main)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Suites) != 2 {
		t.Fatalf("expected 2 suites after include merge, got %d", len(p.Suites))
	}
}

func TestRecursiveIncludeMerge(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	nestedDir := filepath.Join(subDir, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}

	frag2 := filepath.Join(nestedDir, "frag2.yaml")
	if err := os.WriteFile(frag2, []byte(`
suites:
  - name: nested-suite
    cases: [{name: c, steps: [{name: s, protocol: rest}]}]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	frag1 := filepath.Join(subDir, "frag1.yaml")
	if err := os.WriteFile(frag1, []byte(`
include:
  - nested/frag2.yaml
suites:
  - name: sub-suite
    cases: [{name: c, steps: [{name: s, protocol: rest}]}]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	main := filepath.Join(dir, "main.yaml")
	if err := os.WriteFile(main, []byte(`
schemaVersion: "1.0"
name: RecursiveTest
include:
  - sub/frag1.yaml
suites:
  - name: root-suite
    cases: [{name: c, steps: [{name: s, protocol: rest}]}]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Load(main)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(p.Suites) != 3 {
		t.Fatalf("expected 3 merged suites across 3 recursive files, got %d", len(p.Suites))
	}
	suiteNames := []string{p.Suites[0].Name, p.Suites[1].Name, p.Suites[2].Name}
	if suiteNames[0] != "root-suite" || suiteNames[1] != "sub-suite" || suiteNames[2] != "nested-suite" {
		t.Fatalf("unexpected suite order: %v", suiteNames)
	}
}

func TestCyclicIncludeDetection(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.yaml")
	fileB := filepath.Join(dir, "b.yaml")

	if err := os.WriteFile(fileA, []byte(`
schemaVersion: "1.0"
name: A
include:
  - b.yaml
suites:
  - name: sa
    cases: [{name: c, steps: [{name: s, protocol: rest}]}]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(fileB, []byte(`
schemaVersion: "1.0"
name: B
include:
  - a.yaml
suites:
  - name: sb
    cases: [{name: c, steps: [{name: s, protocol: rest}]}]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(fileA)
	if err == nil {
		t.Fatal("expected error for cyclic include, got nil")
	}
	if !strings.Contains(err.Error(), "cyclical include detected") {
		t.Fatalf("expected cyclical include error, got: %v", err)
	}
}

func TestEnvironmentAuthValidation(t *testing.T) {
	p := &Project{
		SchemaVersion: "1.0",
		Name:          "EnvAuthTest",
		Environments: []Environment{
			{
				Name: "dev",
				Auth: &AuthRef{Use: []string{"missing-auth"}},
			},
		},
		Suites: []Suite{
			{
				Name:  "s1",
				Cases: []TestCase{{Name: "c1", Steps: []Step{{Name: "st1", Protocol: "rest"}}}},
			},
		},
	}

	errs := p.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for unresolvable environment auth, got none")
	}
	found := false
	for _, e := range errs {
		if strings.HasPrefix(e.Path, "environments[0].auth") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected validation error at environments[0].auth, got %v", errs)
	}

	// Now define the auth and confirm valid
	p.Auth = []AuthDef{{Name: "missing-auth", Type: "bearer", Config: map[string]any{"token": "xyz"}}}
	errs2 := p.Validate()
	if len(errs2) != 0 {
		t.Fatalf("expected valid with auth defined, got %v", errs2)
	}
}

func TestLoadStepRetry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	content := `
schemaVersion: "1.0"
name: Retry
suites:
  - name: s
    cases:
      - name: c
        steps:
          - name: call
            protocol: rest
            timeoutMs: 1500
            retry:
              count: 2
              delayMs: 25
              until: status
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("expected valid, got %v", errs)
	}
	st := p.Suites[0].Cases[0].Steps[0]
	if st.TimeoutMs != 1500 {
		t.Fatalf("timeoutMs %d", st.TimeoutMs)
	}
	if st.Retry == nil || st.Retry.Count != 2 || st.Retry.DelayMs != 25 || st.Retry.Until != RetryUntilStatus {
		t.Fatalf("retry %#v", st.Retry)
	}
}

func TestValidateRetry(t *testing.T) {
	base := func(st Step) *Project {
		return &Project{
			SchemaVersion: "1.0", Name: "p",
			Suites: []Suite{{
				Name: "s",
				Cases: []TestCase{{
					Name:  "c",
					Steps: []Step{st},
				}},
			}},
		}
	}
	ok := Step{Name: "call", Protocol: "rest", TimeoutMs: 1, Retry: &Retry{Count: MaxRetryCount, DelayMs: 0, Until: RetryUntilPassed}}
	if errs := base(ok).Validate(); len(errs) != 0 {
		t.Fatalf("max count should be valid, got %v", errs)
	}

	tests := []struct {
		name string
		step Step
		path string
	}{
		{"count too high", Step{Name: "call", Protocol: "rest", Retry: &Retry{Count: 21, Until: RetryUntilPassed}}, "suites[0].cases[0].steps[0].retry.count"},
		{"count negative", Step{Name: "call", Protocol: "rest", Retry: &Retry{Count: -1, Until: RetryUntilPassed}}, "suites[0].cases[0].steps[0].retry.count"},
		{"until empty", Step{Name: "call", Protocol: "rest", Retry: &Retry{Count: 1}}, "suites[0].cases[0].steps[0].retry.until"},
		{"until unknown", Step{Name: "call", Protocol: "rest", Retry: &Retry{Count: 1, Until: "always"}}, "suites[0].cases[0].steps[0].retry.until"},
		{"delay negative", Step{Name: "call", Protocol: "rest", Retry: &Retry{Count: 1, DelayMs: -5, Until: RetryUntilStatus}}, "suites[0].cases[0].steps[0].retry.delayMs"},
		{"timeout negative", Step{Name: "call", Protocol: "rest", TimeoutMs: -1}, "suites[0].cases[0].steps[0].timeoutMs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := base(tc.step).Validate()
			found := false
			for _, e := range errs {
				if e.Path == tc.path && e.Code == "validation_error" {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected validation_error at %s, got %v", tc.path, errs)
			}
		})
	}
}

func TestValidateRetryOnHook(t *testing.T) {
	p := &Project{
		SchemaVersion: "1.0", Name: "p",
		Suites: []Suite{{
			Name: "s",
			Setup: &Hook{Steps: []Step{{
				Name: "h", Protocol: "rest",
				Retry: &Retry{Count: MaxRetryCount + 1, Until: RetryUntilPassed},
			}}},
			Cases: []TestCase{{
				Name:  "c",
				Steps: []Step{{Name: "st", Protocol: "rest"}},
			}},
		}},
	}
	errs := p.Validate()
	found := false
	for _, e := range errs {
		if e.Path == "suites[0].setup.steps[0].retry.count" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected hook retry error, got %v", errs)
	}
}

func TestValidateForMockAllowsNoSuites(t *testing.T) {
	p := &Project{
		SchemaVersion: "1.0",
		Name:          "Mocks",
		Mocks:         []Mock{{Method: "GET", Path: "/ping", Body: "ok"}},
	}
	if errs := p.ValidateForMock(); len(errs) != 0 {
		t.Fatal(errs)
	}
	errs := p.Validate()
	found := false
	for _, e := range errs {
		if e.Path == "suites" {
			found = true
		}
	}
	if !found {
		t.Fatalf("run validation must require suites, got %v", errs)
	}
}

func TestValidateMocks(t *testing.T) {
	base := func(m Mock) *Project {
		return &Project{
			SchemaVersion: "1.0", Name: "p",
			Suites: []Suite{{Name: "s", Cases: []TestCase{{Name: "c", Steps: []Step{{Name: "st", Protocol: "rest"}}}}}},
			Mocks:  []Mock{m},
		}
	}
	if errs := base(Mock{Method: "get", Path: "/widgets/{id}", JSON: map[string]any{"ok": true}}).Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
	errs := base(Mock{Method: "FETCH", Path: "widgets", Body: "x", JSON: map[string]any{"a": 1}, DelayMs: -1}).Validate()
	var paths []string
	for _, e := range errs {
		paths = append(paths, e.Path)
	}
	joined := strings.Join(paths, " ")
	for _, want := range []string{"mocks[0].method", "mocks[0].path", "mocks[0]", "mocks[0].delayMs"} {
		if !strings.Contains(joined, want) && want != "mocks[0]" {
			t.Fatalf("missing %s in %v", want, errs)
		}
	}
	if !strings.Contains(joined, "mocks[0]") {
		t.Fatal(errs)
	}
}

func TestValidateSigningAuth(t *testing.T) {
	base := func(defs []AuthDef) *Project {
		return &Project{
			SchemaVersion: "1.0", Name: "p", Auth: defs,
			Suites: []Suite{{Name: "s", Cases: []TestCase{{Name: "c", Steps: []Step{{Name: "st", Protocol: "rest"}}}}}},
		}
	}
	ok := base([]AuthDef{
		{Name: "aws", Type: "aws_sigv4", Config: map[string]any{"region": "us-east-1", "service": "s3", "unsignedPayload": true}},
		{Name: "mac", Type: "hmac", Config: map[string]any{"secret": "${env:HMAC_SECRET}", "algorithm": "SHA256", "encoding": "base64"}},
	})
	if errs := ok.Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
	bad := base([]AuthDef{
		{Name: "aws", Type: "aws_sigv4", Config: map[string]any{"service": "s3", "unsignedPayload": "yes"}},
		{Name: "mac", Type: "hmac", Config: map[string]any{"algorithm": "md5", "nonceHeaader": "X-Nonce"}},
		{Name: "aws2", Type: "aws_sigv4", Config: map[string]any{"region": "r", "service": "s", "secretAcessKey": "${env:K}"}},
	})
	var paths []string
	for _, e := range bad.Validate() {
		paths = append(paths, e.Path)
	}
	joined := strings.Join(paths, " ")
	for _, want := range []string{"auth[0].config.region", "auth[0].config.unsignedPayload", "auth[1].config.secret", "auth[1].config.algorithm",
		"auth[1].config.nonceHeaader", "auth[2].config.secretAcessKey"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, paths)
		}
	}
}
