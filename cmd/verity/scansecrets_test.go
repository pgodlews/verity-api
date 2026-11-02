package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const leakyProject = `schemaVersion: "1.0"
name: leaky
environments:
  - name: dev
    variables:
      API_TOKEN: ${env:API_TOKEN}
include: [more.yaml]
suites: []
`

const leakyFragment = `suites:
  - name: s
    cases:
      - name: c
        steps:
          - name: call
            protocol: rest
            request:
              url: http://localhost/x
              headers:
                X-Api-Key: literal-key
`

func writeLeaky(t *testing.T, dir string, fragment string) string {
	t.Helper()
	p := filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(p, []byte(leakyProject), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "more.yaml"), []byte(fragment), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanSecretsWorkingTree(t *testing.T) {
	dir := t.TempDir()
	p := writeLeaky(t, dir, leakyFragment)
	if code := run([]string{"scan-secrets", p}); code != exitTestFailed {
		t.Fatalf("leaky fragment: exit %d, want %d", code, exitTestFailed)
	}
	writeLeaky(t, dir, "suites: []\n")
	if code := run([]string{"scan-secrets", p}); code != exitOK {
		t.Fatalf("clean project: exit %d, want 0", code)
	}
	if code := run([]string{"scan-secrets", filepath.Join(dir, "missing.yaml")}); code != exitConfig {
		t.Fatalf("missing project: exit %d, want %d", code, exitConfig)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// --staged scans the index: a secret only in the working tree does not
// block, one that is staged does, and so does a staged .env file.
func TestScanSecretsStaged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	p := writeLeaky(t, dir, "suites: []\n")
	git(t, dir, "add", ".")
	if code := run([]string{"scan-secrets", "--staged", p}); code != exitOK {
		t.Fatalf("clean index: exit %d", code)
	}
	writeLeaky(t, dir, leakyFragment) // working tree only
	if code := run([]string{"scan-secrets", "--staged", p}); code != exitOK {
		t.Fatalf("unstaged secret must not block the commit: exit %d", code)
	}
	git(t, dir, "add", "more.yaml")
	if code := run([]string{"scan-secrets", "--staged", p}); code != exitTestFailed {
		t.Fatalf("staged secret: exit %d, want %d", code, exitTestFailed)
	}
	writeLeaky(t, dir, "suites: []\n")
	git(t, dir, "add", "more.yaml")
	for _, tc := range []struct {
		name, content string
		want          int
	}{
		{".env.example", "# names only\nAPI_TOKEN=\nBASE_URL=http://localhost\nPASSWORD=<your password>\n", exitOK},
		{".env.example", "API_TOKEN=hunter2\n", exitTestFailed},
		{".env.example", "API_TOKEN=<hunter2>\n", exitTestFailed},
		{".env.example", "API_TOKEN=<example-token>\nSECRET=<set me>\n", exitOK},
		{".env.local", "API_TOKEN=x\n", exitTestFailed},
		{".ENV", "API_TOKEN=x\n", exitTestFailed},
	} {
		if err := os.WriteFile(filepath.Join(dir, tc.name), []byte(tc.content), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "add", "-f", tc.name)
		if code := run([]string{"scan-secrets", "--staged", p}); code != tc.want {
			t.Fatalf("staged %s %q: exit %d, want %d", tc.name, tc.content, code, tc.want)
		}
		git(t, dir, "rm", "-q", "--cached", tc.name)
		os.Remove(filepath.Join(dir, tc.name))
	}
}

func TestScanSecretsStagedOutsideRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	p := writeLeaky(t, t.TempDir(), "suites: []\n")
	if code := run([]string{"scan-secrets", "--staged", p}); code != exitConfig {
		t.Fatalf("outside a repo: exit %d, want %d", code, exitConfig)
	}
}

// A staged symlinked fragment fails closed: its blob is the target's name.
func TestScanSecretsStagedSymlinkFailsClosed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	p := writeLeaky(t, dir, leakyFragment)
	if err := os.Rename(filepath.Join(dir, "more.yaml"), filepath.Join(dir, "suites.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("suites.yaml", filepath.Join(dir, "more.yaml")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	git(t, dir, "add", ".")
	if code := run([]string{"scan-secrets", "--staged", p}); code != exitConfig {
		t.Fatalf("staged symlink: exit %d, want %d (fail closed)", code, exitConfig)
	}
}

// Includes whose names start with ".." are inside the repository.
func TestScanSecretsStagedDotDotName(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	p := filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(p, []byte("schemaVersion: \"1.0\"\nname: x\ninclude: [..secrets.yaml]\nsuites: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "..secrets.yaml"), []byte(leakyFragment), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	if code := run([]string{"scan-secrets", "--staged", p}); code != exitTestFailed {
		t.Fatalf("..secrets.yaml: exit %d, want %d", code, exitTestFailed)
	}
}

// fmt --check --staged judges the index: an unformatted staged file fails
// even when the working tree is formatted, and the reverse passes.
func TestFmtCheckStaged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	p := filepath.Join(dir, "project.yaml")
	messy := "schemaVersion: \"1.0\"\nname: x\nsuites:\n    -   name: s\n        cases: []\n"
	if err := os.WriteFile(p, []byte(messy), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	if code := run([]string{"fmt", p}); code != exitOK {
		t.Fatalf("fmt: exit %d", code)
	}
	if code := run([]string{"fmt", "--check", "--staged", p}); code != exitTestFailed {
		t.Fatalf("unformatted index, formatted tree: exit %d, want %d", code, exitTestFailed)
	}
	git(t, dir, "add", ".")
	if err := os.WriteFile(p, []byte(messy), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"fmt", "--check", "--staged", p}); code != exitOK {
		t.Fatalf("formatted index, messy tree: exit %d, want 0", code)
	}
	if code := run([]string{"fmt", "--staged", p}); code != exitUsage {
		t.Fatalf("--staged without --check: exit %d, want %d", code, exitUsage)
	}
}
