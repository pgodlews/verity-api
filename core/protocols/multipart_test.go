package protocols

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

const fileSentinel = "RAW-FILE-BYTES"

func TestRESTMultipartTextAndFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sub", "doc.bin")
	if err := os.WriteFile(path, []byte(fileSentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	var got []uploadedPart
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts, err := readUpload(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		got = parts
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	raw := map[string]any{
		"method": "POST",
		"url":    srv.URL + "/up",
		"multipart": []any{
			map[string]any{"name": "note", "value": "hello"},
			map[string]any{"name": "qty", "value": 7},
			map[string]any{
				"name":        "doc",
				"file":        filepath.Join("sub", "doc.bin"),
				"filename":    "my file.txt",
				"contentType": "text/plain",
			},
		},
	}
	req, err := RESTProtocol{}.BuildRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Body) != 0 {
		t.Fatal("BuildRequest read the upload")
	}
	resp, err := RESTProtocol{}.Execute(context.Background(), &spi.ProtocolContext{Dir: dir, Client: srv.Client()}, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d body %s", resp.StatusCode, resp.Body)
	}
	media, params, err := mime.ParseMediaType(req.Headers.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" || params["boundary"] == "" {
		t.Fatalf("content-type %q", req.Headers.Get("Content-Type"))
	}
	if strings.Contains(string(req.Body), fileSentinel) == false {
		t.Fatal("wire body missing file bytes")
	}
	if req.BodySummary == nil || strings.Contains(*req.BodySummary, fileSentinel) {
		t.Fatalf("summary leaked file bytes: %v", req.BodySummary)
	}
	wantSummary := fmt.Sprintf("text: name=note (5 bytes)\ntext: name=qty (1 bytes)\nfile: name=doc filename=my file.txt size=%d\n", len(fileSentinel))
	if *req.BodySummary != wantSummary {
		t.Fatalf("summary\n got %q\nwant %q", *req.BodySummary, wantSummary)
	}
	if len(got) != 3 || got[0].name != "note" || got[1].name != "qty" || got[2].name != "doc" {
		t.Fatalf("part order: %+v", got)
	}
	if got[0].body != "hello" || got[1].body != "7" {
		t.Fatalf("text parts: %+v", got[:2])
	}
	if got[2].body != fileSentinel || got[2].filename != "my file.txt" || got[2].contentType != "text/plain" {
		t.Fatalf("file part: %+v", got[2])
	}
}

func TestRESTMultipartBoundary(t *testing.T) {
	dir := t.TempDir()
	var header string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("Content-Type")
		if _, err := readUpload(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	raw := map[string]any{
		"method":  "POST",
		"url":     srv.URL + "/up",
		"headers": map[string]any{"Content-Type": "multipart/form-data; boundary=Bound_123"},
		"multipart": []any{
			map[string]any{"name": "note", "value": "hello"},
		},
	}
	req, err := RESTProtocol{}.BuildRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (RESTProtocol{}).Execute(context.Background(), &spi.ProtocolContext{Dir: dir, Client: srv.Client()}, req); err != nil {
		t.Fatal(err)
	}
	if header != "multipart/form-data; boundary=Bound_123" {
		t.Fatalf("server content-type %q", header)
	}
	if !strings.Contains(string(req.Body), "--Bound_123") {
		t.Fatalf("body did not use the requested boundary: %s", req.Body)
	}

	// A multipart type without a boundary gets the generated one so the body parses.
	raw["headers"] = map[string]any{"Content-Type": "multipart/form-data"}
	req, err = RESTProtocol{}.BuildRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareUpload(dir, false, req); err != nil {
		t.Fatal(err)
	}
	ctype := req.Headers.Get("Content-Type")
	media, params, err := mime.ParseMediaType(ctype)
	if err != nil || media != "multipart/form-data" || params["boundary"] == "" {
		t.Fatalf("boundary not added: %q", ctype)
	}
	if _, err := readParts(ctype, req.Body); err != nil {
		t.Fatal(err)
	}
}

func TestRESTMultipartMissingFile(t *testing.T) {
	dir := t.TempDir()
	raw := map[string]any{
		"method": "POST",
		"url":    "/up",
		"multipart": []any{
			map[string]any{"name": "doc", "file": "missing.bin"},
		},
	}
	req, err := RESTProtocol{}.BuildRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = RESTProtocol{}.Execute(context.Background(), &spi.ProtocolContext{Dir: dir}, req)
	ve, ok := verr.As(err)
	if !ok || ve.Code != verr.CodeConfig {
		t.Fatalf("expected config_error, got %v", err)
	}
	if !strings.Contains(ve.Message, `missing.bin`) || !strings.Contains(ve.Message, "not found") {
		t.Fatalf("message %q", ve.Message)
	}
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("error leaked the project directory: %v", err)
	}
	if len(req.Body) != 0 {
		t.Fatal("failed upload left file bytes on the request")
	}
}

func TestRESTMultipartConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"not a list", map[string]any{"multipart": "x"}, "list of parts"},
		{"two bodies", map[string]any{"multipart": []any{}, "json": map[string]any{}}, "only one of"},
		{"part type", map[string]any{"multipart": []any{"x"}}, "must be an object"},
		{"no name", map[string]any{"multipart": []any{map[string]any{"value": "a"}}}, "name must"},
		{"both fields", map[string]any{"multipart": []any{map[string]any{"name": "a", "value": "b", "file": "c"}}}, "exactly one"},
		{"neither field", map[string]any{"multipart": []any{map[string]any{"name": "a"}}}, "exactly one"},
		{"empty file", map[string]any{"multipart": []any{map[string]any{"name": "a", "file": ""}}}, "non-empty path"},
		{"bad filename", map[string]any{"multipart": []any{map[string]any{"name": "a", "file": "c", "filename": "a/b"}}}, "filename"},
		{"content type on text", map[string]any{"multipart": []any{map[string]any{"name": "a", "value": "b", "contentType": "text/plain"}}}, "contentType"},
		{"unknown field", map[string]any{"multipart": []any{map[string]any{"name": "a", "value": "b", "extra": "c"}}}, "unknown field"},
		{"newline in name", map[string]any{"multipart": []any{map[string]any{"name": "a\nb", "value": "c"}}}, "newlines"},
		{"bad bodyFile", map[string]any{"bodyFile": ""}, "bodyFile"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RESTProtocol{}.BuildRequest(context.Background(), tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestRESTBodyFile(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(abs, []byte(fileSentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()

	var body []byte
	var header string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		body = b
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	raw := map[string]any{
		"method":   "POST",
		"url":      srv.URL + "/up",
		"bodyFile": abs,
	}
	req, err := RESTProtocol{}.BuildRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (RESTProtocol{}).Execute(context.Background(), &spi.ProtocolContext{Dir: other, AllowExternalFiles: true, Client: srv.Client()}, req); err != nil {
		t.Fatal(err)
	}
	if string(body) != fileSentinel {
		t.Fatalf("server body %q", body)
	}
	if header != "application/octet-stream" {
		t.Fatalf("content-type %q", header)
	}
	if req.BodySummary == nil || strings.Contains(*req.BodySummary, fileSentinel) {
		t.Fatalf("summary leaked file bytes: %v", req.BodySummary)
	}
	if !strings.Contains(*req.BodySummary, "filename=payload.bin") || !strings.Contains(*req.BodySummary, fmt.Sprintf("size=%d", len(fileSentinel))) {
		t.Fatalf("summary %q", *req.BodySummary)
	}

	raw["headers"] = map[string]any{"Content-Type": "application/custom"}
	req, err = RESTProtocol{}.BuildRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareUpload(other, true, req); err != nil { // absolute path outside other: explicit opt-in
		t.Fatal(err)
	}
	if req.Headers.Get("Content-Type") != "application/custom" {
		t.Fatalf("user content-type replaced: %q", req.Headers.Get("Content-Type"))
	}

	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	req, err = RESTProtocol{}.BuildRequest(context.Background(), map[string]any{
		"url": "/up", "bodyFile": "subdir",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = prepareUpload(dir, false, req)
	ve, ok := verr.As(err)
	if !ok || ve.Code != verr.CodeConfig || !strings.Contains(ve.Message, "directory") {
		t.Fatalf("directory upload: %v", err)
	}
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("error leaked the project directory: %v", err)
	}
}

type uploadedPart struct {
	name, filename, contentType, body string
}

func readUpload(r *http.Request) ([]uploadedPart, error) {
	return readParts(r.Header.Get("Content-Type"), readBody(r))
}

func readBody(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	return b
}

func readParts(contentType string, body []byte) ([]uploadedPart, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, err
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var out []uploadedPart
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(p)
		if err != nil {
			return nil, err
		}
		out = append(out, uploadedPart{
			name:        p.FormName(),
			filename:    p.FileName(),
			contentType: p.Header.Get("Content-Type"),
			body:        string(b),
		})
	}
}

// TestUploadConfinedToProjectDir: a project may not read files outside its
// directory (absolute, ../, or via a symlink) unless explicitly allowed, and
// multipart text values never appear in the capture summary.
func TestUploadConfinedToProjectDir(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "ok.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	build := func(file string) *spi.Request {
		req, err := RESTProtocol{}.BuildRequest(context.Background(), map[string]any{"url": "/up", "bodyFile": file})
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	for _, f := range []string{secret, "../id_rsa", "link"} {
		err := prepareUpload(project, false, build(f))
		if err == nil || !strings.Contains(err.Error(), "outside the project directory") {
			t.Errorf("%s: expected confinement error, got %v", f, err)
		}
		if err := prepareUpload(project, true, build(f)); err != nil {
			t.Errorf("%s with opt-in: %v", f, err)
		}
	}
	if err := prepareUpload(project, false, build("ok.txt")); err != nil {
		t.Fatalf("file inside the project: %v", err)
	}

	req, err := RESTProtocol{}.BuildRequest(context.Background(), map[string]any{
		"url": "/up", "multipart": []any{map[string]any{"name": "password", "value": "hunter2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareUpload(project, false, req); err != nil {
		t.Fatal(err)
	}
	if req.BodySummary == nil || strings.Contains(*req.BodySummary, "hunter2") || !strings.Contains(*req.BodySummary, "name=password (7 bytes)") {
		t.Fatalf("summary must not carry text values: %v", req.BodySummary)
	}
	first := append([]byte(nil), req.Body...)
	if err := FinalizeBody(project, false, req); err != nil {
		t.Fatal(err)
	}
	if string(req.Body) != string(first) {
		t.Fatal("preparing an upload twice changed the signed bytes")
	}
}
