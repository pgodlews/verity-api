package mock

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPetstorePaths(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "specs", "petstore.openapi.yaml")
	h, err := Handler(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/pets", "/v1/pets", "/pets/1", "/v1/pets/1"} {
		code, _, _ := call(t, h, http.MethodGet, p, nil, "")
		if p == "/pets" || p == "/v1/pets" || strings.HasSuffix(p, "/1") {
			if code != 200 {
				t.Fatalf("%s: %d", p, code)
			}
		}
	}
	code, _, _ := call(t, h, http.MethodPost, "/pets", nil, "")
	if code != 201 {
		t.Fatalf("create: %d", code)
	}
}

func TestOpenBindsLoopback(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "specs", "petstore.openapi.yaml")
	srv, err := Open(path, Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(srv.URL, "http://127.0.0.1:") || strings.HasSuffix(srv.URL, ":0") {
		t.Fatalf("url %s", srv.URL)
	}
	if srv.Kind != KindOpenAPI || srv.Port == 0 {
		t.Fatalf("kind %s port %d", srv.Kind, srv.Port)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- srv.Run(ctx) }()

	var resp *http.Response
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err = http.Get(srv.URL + "/pets")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown hung")
	}
}

func TestDetectProject(t *testing.T) {
	kind, err := Detect([]byte("schemaVersion: \"1.0\"\nname: n\n"))
	if err != nil || kind != KindProject {
		t.Fatalf("%s %v", kind, err)
	}
	if _, err := os.Stat(filepath.Join("..", "..", "examples", "specs", "petstore.openapi.yaml")); err != nil {
		t.Fatal(err)
	}
}
