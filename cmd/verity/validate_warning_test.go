package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// withStdout runs fn while os.Stdout is a pipe and returns what fn wrote.
func withStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	<-done
	return buf.String()
}

func TestValidateWarnsAboutProcessEnvironmentFallback(t *testing.T) {
	t.Setenv("VERITY_TEST_FALLBACK_TOKEN", "x")
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	body := projectYAML("http://127.0.0.1:1/${VERITY_TEST_FALLBACK_TOKEN}/${DEFINED_NOWHERE_XYZ}")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	out := withStdout(t, func() { code = run([]string{"validate", path, "--json"}) })
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	var got struct {
		OK       bool `json:"ok"`
		Warnings []struct {
			Code string `json:"code"`
			Name string `json:"name"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !got.OK || len(got.Warnings) != 1 || got.Warnings[0].Code != "env_fallback" || got.Warnings[0].Name != "VERITY_TEST_FALLBACK_TOKEN" {
		t.Fatalf("warnings = %+v", got.Warnings)
	}
}
