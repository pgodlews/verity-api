package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

func TestUpdateGitignore(t *testing.T) {
	block := "# >>> verity\n.env\n.env.*\n!.env.example\nreports/\n.verity/local/\n# <<< verity\n"
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", want: block},
		{name: "append", input: "node_modules/\n", want: "node_modules/\n" + block},
		{name: "add newline", input: "node_modules/", want: "node_modules/\n" + block},
		{
			name:  "replace marked block",
			input: "before\n# >>> verity\nold\n# <<< verity\nafter\n",
			want:  "before\n" + block + "after\n",
		},
		{
			name:  "CRLF",
			input: "before\r\n# >>> verity\r\nold\r\n# <<< verity\r\n",
			want:  "before\r\n" + strings.ReplaceAll(block, "\n", "\r\n"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := updateGitignore([]byte(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}
			twice, err := updateGitignore(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(twice) != string(got) {
				t.Fatal("gitignore update is not idempotent")
			}
		})
	}
}

func TestUpdateGitignoreRejectsIncompleteBlock(t *testing.T) {
	if _, err := updateGitignore([]byte("# >>> verity\n.env\n")); err == nil {
		t.Fatal("incomplete block accepted")
	}
}

func TestInitCreatesGitReadyProject(t *testing.T) {
	dir := t.TempDir()
	result, err := Init(InitOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changed) != 3 {
		t.Fatalf("changed files: %v", result.Changed)
	}
	project, err := model.Load(filepath.Join(dir, "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if errs := project.Validate(); len(errs) > 0 {
		t.Fatalf("starter project does not validate: %v", errs)
	}
	envExample, err := os.ReadFile(filepath.Join(dir, ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"API_TOKEN=\n", "BASE_URL=\n", "# Used in: environment local."} {
		if !strings.Contains(string(envExample), want) {
			t.Errorf(".env.example missing %q:\n%s", want, envExample)
		}
	}

	second, err := Init(InitOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Changed) != 0 {
		t.Fatalf("second init changed files: %v", second.Changed)
	}
}

func TestInitProtectsEnvExampleWithoutForce(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(InitOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".env.example")
	custom := []byte("# local names\nCUSTOM=\n")
	if err := os.WriteFile(path, custom, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Init(InitOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Fatal("init overwrote .env.example without --force")
	}
	if _, err := Init(InitOptions{Dir: dir, Force: true}); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == string(custom) || !strings.Contains(string(got), "API_TOKEN=\n") {
		t.Fatalf("forced init did not refresh .env.example:\n%s", got)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != beforeInfo.Mode().Perm() {
		t.Fatalf("file mode changed to %v", st.Mode().Perm())
	}
}

func TestInitGitignoreOnly(t *testing.T) {
	dir := t.TempDir()
	result, err := Init(InitOptions{Dir: dir, GitignoreOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changed) != 1 || filepath.Base(result.Changed[0]) != ".gitignore" {
		t.Fatalf("changed files: %v", result.Changed)
	}
	for _, name := range []string{"project.yaml", ".env.example"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("gitignore-only init created %s", name)
		}
	}
}

func TestInitEnvExampleIncludesAllReferenceScopes(t *testing.T) {
	dir := t.TempDir()
	project := `schemaVersion: "1.0"
name: refs
suites:
  - name: suite
    vars:
      SUITE_TOKEN: "${env:SUITE_TOKEN}"
    cases:
      - name: case
        vars:
          CASE_TOKEN: "${env:CASE_TOKEN}"
        feed:
          type: json
          inline:
            - value: "${env:FEED_TOKEN}"
        steps:
          - name: step
            protocol: rest
            request:
              url: "${BASE_URL}"
            assertions:
              - type: contains
                value: ok
                config:
                  token: "${env:ASSERTION_TOKEN}"
            transfers:
              - name: captured
                from: body
                default: "${env:TRANSFER_TOKEN}"
`
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(InitOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SUITE_TOKEN", "CASE_TOKEN", "FEED_TOKEN", "ASSERTION_TOKEN", "TRANSFER_TOKEN"} {
		if !strings.Contains(string(data), name+"=\n") {
			t.Errorf(".env.example missing %s:\n%s", name, data)
		}
	}
}

func TestUpdateGitignoreRefusesDuplicateEndMarker(t *testing.T) {
	in := "# >>> verity\n.env\n# <<< verity\nimportant-generated-dir/\n# <<< verity\n"
	if _, err := updateGitignore([]byte(in)); err == nil {
		t.Fatal("duplicate end marker must be refused, not resolved by dropping lines")
	}
}
