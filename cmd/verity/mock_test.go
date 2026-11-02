package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/importers"
	"github.com/pgodlews/verity-api/core/mock"
)

func TestCmdMockUsage(t *testing.T) {
	if code := cmdMock(nil); code != exitUsage {
		t.Fatalf("usage exit %d", code)
	}
	if code := cmdMock([]string{"missing.yaml"}); code != exitConfig {
		t.Fatalf("missing file exit %d", code)
	}
}

func TestMockThenRunPetstore(t *testing.T) {
	spec := filepath.Join("..", "..", "examples", "specs", "petstore.openapi.yaml")
	srv, err := mock.Open(spec, mock.Options{Host: "127.0.0.1", Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- srv.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(srv.URL + "/pets")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("mock /pets: %d", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	res, err := importers.ImportOpenAPI(spec)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	if err := res.Project.Save(path); err != nil {
		t.Fatal(err)
	}
	code := cmdRun([]string{path, "--var", "BASE_URL=" + srv.URL, "--var", "petId=1"})
	if code != 0 {
		t.Fatalf("verity run exited %d", code)
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mock shutdown hung")
	}
	if strings.Contains(srv.URL, "0.0.0.0") {
		t.Fatal(srv.URL)
	}
}
