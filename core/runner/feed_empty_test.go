package runner

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// A feed with no rows skips the case instead of running it un-fed with
// literal ${column} placeholders.
func TestEmptyFeedSkipsCase(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rows.csv"), []byte("id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &model.Project{
		SchemaVersion: "1.0", Name: "feed", SourceDir: dir,
		Environments: []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{
			Name: "c", Feed: &model.Feed{Type: "csv", Path: "rows.csv"},
			Steps: []model.Step{{Name: "get", Protocol: "rest", Request: map[string]any{"url": srv.URL + "/${id}"}}},
		}}}},
	}
	res := retryRun(t, nil, p, Options{})
	c := res.Suites[0].Cases[0]
	if c.Status != spi.StatusSkipped || hits.Load() != 0 {
		t.Fatalf("case status %s, requests %d; want skipped and none", c.Status, hits.Load())
	}
	if len(c.Steps) != 1 || c.Steps[0].Error == nil || c.Steps[0].Error.Code != string(verr.CodeConfig) {
		t.Fatalf("skip reason missing: %+v", c.Steps)
	}
}
