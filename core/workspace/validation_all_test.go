package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

// Two problems in one project: a run reports both, and Validate returns
// them as structured issues with paths.
func TestWorkspaceReportsAllValidationErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.yaml")
	body := "schemaVersion: \"1.0\"\nname: p\nsuites:\n  - name: s\n    cases:\n      - name: \"\"\n        steps:\n          - name: a\n            protocol: rest\n      - name: c\n        steps: []\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), RunOptions{Path: path})
	got, ok := verr.As(err)
	if !ok || got.Code != verr.CodeValidation {
		t.Fatalf("run error = %v", err)
	}
	all, _ := got.Details["errors"].([]*verr.Error)
	if len(all) < 2 || !strings.Contains(got.Message, all[1].Message) {
		t.Fatalf("run reported %d errors: %v", len(all), got.Message)
	}

	res := Validate(context.Background(), path, "", nil)
	if res.OK || len(res.Issues) != len(res.Errors) || len(res.Issues) < 2 {
		t.Fatalf("validate = %+v", res)
	}
	for _, issue := range res.Issues {
		if issue.Code != verr.CodeValidation || issue.Path == "" {
			t.Fatalf("issue without code/path: %+v", issue)
		}
	}
}
