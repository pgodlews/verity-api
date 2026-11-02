package protocols

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// validateUpload checks multipart and bodyFile shape. It does not read files;
// those are opened in prepareUpload when the request is sent.
func validateUpload(raw map[string]any) error {
	if raw["multipart"] == nil && raw["bodyFile"] == nil {
		return nil
	}
	n := 0
	for _, key := range []string{"body", "json", "form", "multipart", "bodyFile"} {
		if raw[key] != nil {
			n++
		}
	}
	if n != 1 {
		return verr.New(verr.CodeConfig, "use only one of body, json, form, multipart, or bodyFile")
	}
	if raw["bodyFile"] != nil {
		path, ok := raw["bodyFile"].(string)
		if !ok || path == "" || strings.ContainsAny(path, "\r\n") {
			return verr.New(verr.CodeConfig, "bodyFile must be a non-empty file path without newlines")
		}
		return nil
	}
	parts, ok := raw["multipart"].([]any)
	if !ok {
		return verr.New(verr.CodeConfig, "multipart must be a list of parts")
	}
	for i, item := range parts {
		part, ok := item.(map[string]any)
		if !ok {
			return verr.Newf(verr.CodeConfig, "multipart part %d: must be an object", i)
		}
		if err := validatePart(i, part); err != nil {
			return err
		}
	}
	return nil
}

func validatePart(i int, part map[string]any) error {
	bad := func(message string) error {
		return verr.Newf(verr.CodeConfig, "multipart part %d: %s", i, message)
	}
	name, ok := part["name"].(string)
	if !ok || name == "" || strings.ContainsAny(name, "\r\n") {
		return bad("name must be a non-empty string without newlines")
	}
	_, hasValue := part["value"]
	_, hasFile := part["file"]
	if hasValue == hasFile {
		return bad("requires exactly one of value or file")
	}
	if hasValue {
		s, ok := scalarString(part["value"])
		if !ok {
			return bad("value must be a string")
		}
		part["value"] = s
	}
	for key, v := range part {
		if key == "value" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return bad(key + " must be a string")
		}
		switch key {
		case "name":
		case "file":
			if s == "" || strings.ContainsAny(s, "\r\n") {
				return bad("file must be a non-empty path without newlines")
			}
		case "filename":
			if !hasFile || !safeWireName(s) {
				return bad("filename must be a single path segment on a file part")
			}
		case "contentType":
			if !hasFile || s == "" || strings.ContainsAny(s, "\r\n") {
				return bad("contentType requires a file part and a non-empty value without newlines")
			}
		default:
			return bad("unknown field " + key)
		}
	}
	return nil
}

// FinalizeBody reads multipart and bodyFile payloads onto req. The runner
// calls it before auth so a signer hashes the bytes that will be sent.
// Calling it again leaves those bytes in place: a second multipart build
// would mint a new boundary and invalidate the signature.
func FinalizeBody(dir string, allowExternal bool, req *spi.Request) error {
	return prepareUpload(dir, allowExternal, req)
}

// prepareUpload reads multipart and bodyFile payloads. Summaries are stored
// separately from the wire body so captures can describe file parts without
// including their bytes. dir is the project directory; an empty dir resolves
// relative paths against the current working directory.
func prepareUpload(dir string, allowExternal bool, req *spi.Request) error {
	if req == nil || req.Config == nil {
		return nil
	}
	if req.BodySummary != nil {
		return nil
	}
	if req.Config["multipart"] == nil && req.Config["bodyFile"] == nil {
		return nil
	}
	if err := validateUpload(req.Config); err != nil {
		return err
	}
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	if path, ok := req.Config["bodyFile"].(string); ok {
		return writeBodyFile(dir, allowExternal, req, path)
	}
	return writeMultipart(dir, allowExternal, req)
}

func writeBodyFile(dir string, allowExternal bool, req *spi.Request, path string) error {
	filename, err := wireFilename(path, "")
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	size, err := copyUploadFile(&buf, dir, allowExternal, path)
	if err != nil {
		return err
	}
	if req.Headers.Get("Content-Type") == "" {
		req.Headers.Set("Content-Type", "application/octet-stream")
	}
	summary := fmt.Sprintf("file: filename=%s size=%d\n", filename, size)
	req.Body = buf.Bytes()
	req.BodySummary = &summary
	return nil
}

func writeMultipart(dir string, allowExternal bool, req *spi.Request) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if ct := req.Headers.Get("Content-Type"); ct != "" {
		if _, params, err := mime.ParseMediaType(ct); err == nil && params["boundary"] != "" {
			if err := writer.SetBoundary(params["boundary"]); err != nil {
				return verr.Wrap(verr.CodeConfig, err, "invalid multipart boundary")
			}
		}
	}

	var summary strings.Builder
	for i, item := range req.Config["multipart"].([]any) {
		part := item.(map[string]any)
		name := part["name"].(string)
		if value, ok := part["value"].(string); ok {
			if err := writer.WriteField(name, value); err != nil {
				return verr.Wrap(verr.CodeConfig, err, "write multipart text part")
			}
			// Values stay out of captures: a text field may be a password, and
			// this shape would slip past the structural redaction patterns.
			fmt.Fprintf(&summary, "text: name=%s (%d bytes)\n", name, len(value))
			continue
		}
		path := part["file"].(string)
		override, _ := part["filename"].(string)
		filename, err := wireFilename(path, override)
		if err != nil {
			msg := err.Error()
			if ve, ok := verr.As(err); ok {
				msg = ve.Message
			}
			return verr.Newf(verr.CodeConfig, "multipart part %d (%s): %s", i, name, msg)
		}
		contentType := "application/octet-stream"
		if v, ok := part["contentType"].(string); ok && v != "" {
			contentType = v
		}
		disp := mime.FormatMediaType("form-data", map[string]string{
			"name":     name,
			"filename": filename,
		})
		if disp == "" {
			return verr.Newf(verr.CodeConfig, "multipart part %d (%s): invalid content disposition", i, name)
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", disp)
		header.Set("Content-Type", contentType)
		out, err := writer.CreatePart(header)
		if err != nil {
			return verr.Wrap(verr.CodeConfig, err, "create multipart file part")
		}
		size, err := copyUploadFile(out, dir, allowExternal, path)
		if err != nil {
			if ve, ok := verr.As(err); ok {
				return verr.Newf(verr.CodeConfig, "multipart part %d (%s): %s", i, name, ve.Message)
			}
			return verr.Newf(verr.CodeConfig, "multipart part %d (%s): read file %q", i, name, path)
		}
		fmt.Fprintf(&summary, "file: name=%s filename=%s size=%d\n", name, filename, size)
	}
	if err := writer.Close(); err != nil {
		return verr.Wrap(verr.CodeConfig, err, "close multipart body")
	}
	setMultipartContentType(req.Headers, writer)
	text := summary.String()
	req.Body = body.Bytes()
	req.BodySummary = &text
	return nil
}

// setMultipartContentType sets a multipart Content-Type with a boundary when
// the request has none. An explicit Content-Type is kept; if it is multipart
// and omits a boundary, the generated boundary is added so the body still parses.
func setMultipartContentType(h http.Header, w *multipart.Writer) {
	ct := h.Get("Content-Type")
	if ct == "" {
		h.Set("Content-Type", w.FormDataContentType())
		return
	}
	media, params, err := mime.ParseMediaType(ct)
	if err != nil || params["boundary"] != "" || !strings.HasPrefix(media, "multipart/") {
		return
	}
	if params == nil {
		params = map[string]string{}
	}
	params["boundary"] = w.Boundary()
	h.Set("Content-Type", mime.FormatMediaType(media, params))
}

// insideDir reports (as an error) whether path, after resolving symlinks,
// lies outside dir. Uploads are confined to the project directory by default
// so that opening and running a downloaded project cannot send arbitrary
// local files (keys, dotenv files) to the project's servers.
func insideDir(dir, path string) error {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // reported as "not found" by the caller
		}
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("outside")
	}
	return nil
}

func copyUploadFile(out io.Writer, dir string, allowExternal bool, path string) (int64, error) {
	resolved := path
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(dir, resolved)
	}
	if dir != "" && !allowExternal {
		if err := insideDir(dir, resolved); err != nil {
			return 0, verr.Newf(verr.CodeConfig,
				"upload file %q is outside the project directory; run with --allow-external-files if this project is trusted", path)
		}
	}
	file, err := os.Open(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, verr.Newf(verr.CodeConfig, "upload file %q not found", path)
		}
		return 0, verr.Newf(verr.CodeConfig, "open upload file %q", path)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, verr.Newf(verr.CodeConfig, "stat upload file %q", path)
	}
	if info.IsDir() {
		return 0, verr.Newf(verr.CodeConfig, "upload file %q is a directory", path)
	}
	size, err := io.Copy(out, file)
	if err != nil {
		return 0, verr.Newf(verr.CodeConfig, "read upload file %q", path)
	}
	return size, nil
}

func wireFilename(path, override string) (string, error) {
	name := override
	if name == "" {
		name = filepath.Base(path)
	}
	if !safeWireName(name) {
		return "", verr.Newf(verr.CodeConfig, "invalid upload filename %q", name)
	}
	return name, nil
}

func safeWireName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\r\n")
}

// scalarString accepts the scalars YAML and JSON decode into a part value.
func scalarString(v any) (string, bool) {
	switch v.(type) {
	case nil, string, bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return toStr(v), true
	default:
		return "", false
	}
}
