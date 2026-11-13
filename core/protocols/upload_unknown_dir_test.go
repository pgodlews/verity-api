package protocols

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

// Without a project directory an upload cannot be checked for confinement,
// so it is refused unless the caller opts in to external files.
func TestUploadWithUnknownProjectDirNeedsOptIn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "body.bin")
	if err := os.WriteFile(file, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	build := func() map[string]any {
		return map[string]any{"method": "POST", "url": "http://127.0.0.1/x", "bodyFile": file}
	}
	req, err := RESTProtocol{}.BuildRequest(context.Background(), build())
	if err != nil {
		t.Fatal(err)
	}
	err = prepareUpload("", false, req)
	if got, ok := verr.As(err); !ok || got.Code != verr.CodeConfig {
		t.Fatalf("unknown dir upload error = %v, want %s", err, verr.CodeConfig)
	}
	req, _ = RESTProtocol{}.BuildRequest(context.Background(), build())
	if err := prepareUpload("", true, req); err != nil || string(req.Body) != "payload" {
		t.Fatalf("opt-in upload = %q, %v", req.Body, err)
	}
}
