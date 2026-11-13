package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/verr"
)

// Load reads a project from a YAML or JSON file, recursively merging any
// included fragment files. The file extension (.yaml/.yml/.json) selects the
// decoder. The returned project has SourceDir set for relative resolution and
// SourceFiles set to the absolute paths Load read plus feed and upload files
// the merged project references.
//
// Load performs structural decoding only; call Validate for semantic checks.
//
// Included fragments must lie inside the project directory (the directory
// of the root file); LoadWith with AllowExternalFiles lifts that.
func Load(path string) (*Project, error) {
	return LoadWith(path, LoadOptions{})
}

// LoadOptions changes how Load reads a project.
type LoadOptions struct {
	// AllowExternalFiles lets include read fragments outside the project
	// directory (--allow-external-files; trusted projects only).
	AllowExternalFiles bool
}

// LoadWith is Load with options.
func LoadWith(path string, opts LoadOptions) (*Project, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, verr.Context(verr.CodeConfig, err, fmt.Sprintf("resolve path %q", path))
	}
	visited := map[string]bool{}
	l := loader{root: filepath.Dir(abs), allowExternal: opts.AllowExternalFiles}
	p, err := l.loadRecursive(abs, visited, []string{abs})
	if err != nil {
		return nil, err
	}
	p.SourceFiles = sourceFiles(p, visited)
	return p, nil
}

type loader struct {
	root          string
	allowExternal bool
}

func (l loader) loadRecursive(path string, visited map[string]bool, chain []string) (*Project, error) {
	p, err := loadFile(path)
	if err != nil {
		return nil, err
	}
	p.SourceDir = filepath.Dir(path)
	visited[path] = true

	for _, inc := range p.Include {
		incPath := inc
		if !filepath.IsAbs(incPath) {
			incPath = filepath.Join(p.SourceDir, inc)
		}
		absInc, err := filepath.Abs(incPath)
		if err != nil {
			return nil, verr.Context(verr.CodeConfig, err, fmt.Sprintf("resolve include path %q", inc))
		}
		if _, err := fsutil.ResolveInside(l.root, absInc, l.allowExternal, "include"); err != nil {
			return nil, err
		}

		// Check for cycle in the current inclusion chain
		for _, prev := range chain {
			if prev == absInc {
				return nil, verr.Newf(verr.CodeValidation, "cyclical include detected: %s -> %s", strings.Join(chain, " -> "), absInc)
			}
		}

		// If already visited and merged earlier (e.g. diamond include), skip to avoid duplicate suites/defs
		if visited[absInc] {
			continue
		}

		frag, err := l.loadRecursive(absInc, visited, append(chain, absInc))
		if err != nil {
			return nil, verr.Context(verr.CodeConfig, err, fmt.Sprintf("include %q", inc))
		}
		mergeFragment(p, frag)
	}
	p.Include = nil
	return p, nil
}

// loadFile decodes a single project (or fragment) file.
func loadFile(path string) (*Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, verr.Context(verr.CodeConfig, err, fmt.Sprintf("read %q", path))
	}
	return Decode(data, path)
}

// Decode parses raw bytes into a Project using the format implied by ext.
// ext may be a filename or extension; JSON is used for .json, YAML otherwise.
func Decode(data []byte, ext string) (*Project, error) {
	var p Project
	if strings.HasSuffix(strings.ToLower(ext), ".json") {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if err := dec.Decode(&p); err != nil {
			return nil, verr.Context(verr.CodeValidation, err, "parse JSON")
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			if err == nil {
				return nil, verr.New(verr.CodeValidation, "parse JSON: multiple values")
			}
			return nil, verr.Context(verr.CodeValidation, err, "parse JSON")
		}
		// Reject keys the model does not define (a typo such as
		// "asertions" would otherwise be dropped silently).
		var generic any
		gdec := json.NewDecoder(bytes.NewReader(data))
		gdec.UseNumber()
		if err := gdec.Decode(&generic); err == nil {
			if err := checkKnownFields(generic, "json"); err != nil {
				return nil, err
			}
		}
	} else {
		if err := decodeYAMLProject(data, &p); err != nil {
			return nil, verr.Context(verr.CodeValidation, err, "parse YAML")
		}
	}
	return &p, nil
}

// mergeFragment appends suites, mocks, environments, and auth defs from a
// fragment into the parent project. Scalar fields on the parent take precedence.
func mergeFragment(parent, frag *Project) {
	parent.Environments = append(parent.Environments, frag.Environments...)
	parent.Auth = append(parent.Auth, frag.Auth...)
	parent.Suites = append(parent.Suites, frag.Suites...)
	parent.Mocks = append(parent.Mocks, frag.Mocks...)
	if parent.Name == "" {
		parent.Name = frag.Name
	}
	if parent.SchemaVersion == "" {
		parent.SchemaVersion = frag.SchemaVersion
	}
	if parent.Schema == nil {
		parent.Schema = frag.Schema
	}
}

// sourceFiles lists the project file, every fragment Load read, feed files,
// and upload files. Feed and upload paths resolve against the root project
// directory, matching the runner (fragment-relative paths are not used).
// Inline feed rows replace the file at run time, so a path on an inline feed
// is not recorded. Missing files are still recorded: watch mode has to see
// them appear.
func sourceFiles(p *Project, read map[string]bool) []string {
	set := make(map[string]struct{}, len(read))
	for f := range read {
		if f == "" {
			continue
		}
		set[filepath.Clean(f)] = struct{}{}
	}
	add := func(path string) {
		abs, ok := resolveSourcePath(p.SourceDir, path)
		if !ok {
			return
		}
		set[abs] = struct{}{}
	}
	addSteps := func(steps []Step) {
		for _, st := range steps {
			for _, path := range uploadPaths(st.Request) {
				add(path)
			}
			if st.Schema != nil {
				add(st.Schema.File)
			}
		}
	}
	if p.Schema != nil {
		add(p.Schema.File)
	}
	for i := range p.Suites {
		s := &p.Suites[i]
		if s.Schema != nil {
			add(s.Schema.File)
		}
		if s.Setup != nil {
			addSteps(s.Setup.Steps)
		}
		if s.Teardown != nil {
			addSteps(s.Teardown.Steps)
		}
		for j := range s.Cases {
			c := &s.Cases[j]
			if c.Feed != nil && c.Feed.Path != "" && len(c.Feed.Inline) == 0 {
				add(c.Feed.Path)
			}
			addSteps(c.Steps)
		}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// ReferencedFiles returns feed, schema, and upload paths without reading them.
// Paths resolve against SourceDir, matching Load and the runner. This also
// works for projects decoded from Git blobs rather than the working tree.
func ReferencedFiles(p *Project) []string {
	return sourceFiles(p, nil)
}

// resolveSourcePath makes a feed or upload path absolute against dir.
// Relative paths use the project directory. An empty path is skipped.
func resolveSourcePath(dir, path string) (string, bool) {
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return "", false
	}
	if !filepath.IsAbs(path) {
		if dir == "" {
			return "", false
		}
		path = filepath.Join(dir, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	return abs, true
}

// uploadPaths returns multipart file parts and a bodyFile path from a request.
// It does not read the files. YAML/JSON decode into []any of map[string]any;
// a []map[string]any is accepted for projects built in Go.
func uploadPaths(req map[string]any) []string {
	if req == nil {
		return nil
	}
	var out []string
	if s, ok := req["bodyFile"].(string); ok && s != "" {
		out = append(out, s)
	}
	switch parts := req["multipart"].(type) {
	case []any:
		for _, item := range parts {
			if s, ok := partFile(item); ok {
				out = append(out, s)
			}
		}
	case []map[string]any:
		for _, part := range parts {
			if s, ok := part["file"].(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func partFile(item any) (string, bool) {
	part, ok := item.(map[string]any)
	if !ok {
		return "", false
	}
	s, ok := part["file"].(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// SelectEnvironment returns the named environment, or the default-marked one
// when name is empty, or the first environment as a last resort. It returns an
// empty (named) environment if the project declares none.
func (p *Project) SelectEnvironment(name string) (Environment, error) {
	if name != "" {
		for _, e := range p.Environments {
			if e.Name == name {
				return e, nil
			}
		}
		return Environment{}, verr.Newf(verr.CodeNotFound, "environment %q not found", name)
	}
	for _, e := range p.Environments {
		if e.Default {
			return e, nil
		}
	}
	if len(p.Environments) > 0 {
		return p.Environments[0], nil
	}
	return Environment{Name: "", Variables: map[string]string{}}, nil
}

// AuthByName returns the named AuthDef and whether it exists.
func (p *Project) AuthByName(name string) (AuthDef, bool) {
	for _, a := range p.Auth {
		if a.Name == name {
			return a, true
		}
	}
	return AuthDef{}, false
}
