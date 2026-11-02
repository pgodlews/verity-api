package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secretscan"
	"github.com/pgodlews/verity-api/core/verr"
)

// SaveEnvironmentRequest replaces one environment's variables (and optionally
// its default flag), or creates the environment when Create is set.
type SaveEnvironmentRequest struct {
	Path      string            `json:"path"`
	Name      string            `json:"name"`
	Variables map[string]string `json:"variables"`
	// Default, when set, marks this environment default (true) or not
	// (false). Making one default clears the flag on the others.
	Default *bool `json:"default,omitempty"`
	// Create adds a new environment (to the root project file) instead of
	// editing an existing one; it is an error if the name is taken.
	Create bool `json:"create,omitempty"`
	// Baseline maps a project file's absolute path to the sha256 of its
	// bytes as loaded. When set, every file this save would write must
	// still match. Nil skips the check so a CLI or dev-bridge caller that
	// never had hashes can still save. The desktop App.SaveEnvironment
	// rejects a missing baseline. A mismatch writes nothing.
	Baseline map[string]string `json:"baseline,omitempty"`
	// BeforeWrite is called with the bytes about to be written, before the
	// rename. Nil is fine. It is not part of the JSON API.
	BeforeWrite func(path string, data []byte) `json:"-"`
}

// SaveEnvironment writes environment variables back to the file that defines
// the environment, editing YAML as a node tree so comments survive. Literal
// values that look like credentials are refused: secrets belong in
// ${env:NAME} references, never in project files.
func SaveEnvironment(req SaveEnvironmentRequest) (*SaveStepResult, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, verr.New(verr.CodeValidation, "environment name is required")
	}
	unlock := lockProject(req.Path)
	defer unlock()
	for k, v := range req.Variables {
		if strings.TrimSpace(k) == "" {
			return nil, verr.New(verr.CodeValidation, "variable names must not be empty")
		}
		// Only the text outside ${...} references is literal: "hunter2${x}"
		// still stores hunter2 in the file. Variables are a credential place,
		// so token-shaped values count as well as credential-named ones.
		if ok, _ := secretscan.Check(k, v, true); ok {
			return nil, verr.Newf(verr.CodeValidation,
				"%s looks like a credential; store it as ${env:NAME} and set NAME on this machine", k)
		}
	}
	root, err := filepath.Abs(req.Path)
	if err != nil {
		return nil, err
	}
	files, err := projectFiles(root)
	if err != nil {
		return nil, err
	}

	target := ""
	if req.Create {
		p, err := model.Load(root)
		if err != nil {
			return nil, err
		}
		for _, e := range p.Environments {
			if e.Name == req.Name {
				return nil, verr.Newf(verr.CodeValidation, "environment %q already exists", req.Name)
			}
		}
		target = root
	}

	// Default handling may touch every file that holds environments. Build the
	// whole edit first so a conflict or a later failure writes nothing, then
	// restore anything already renamed if validation fails.
	type planned struct {
		file    string
		orig    []byte
		updated []byte
	}
	var plan []planned
	for _, f := range files {
		orig, rerr := os.ReadFile(f)
		if rerr != nil {
			return nil, rerr
		}
		updated, found, eerr := editEnvironments(orig, f, req, f == target)
		if eerr != nil {
			return nil, verr.Wrap(verr.CodeConfig, eerr, "edit "+filepath.Base(f))
		}
		if found && target == "" {
			target = f
		}
		if updated == nil {
			continue
		}
		if req.Baseline != nil {
			want, ok := lookupBaseline(req.Baseline, f)
			if !ok || want != contentHash(orig) {
				return nil, envConflict()
			}
		}
		plan = append(plan, planned{file: f, orig: orig, updated: updated})
	}
	if target == "" {
		return nil, verr.Newf(verr.CodeNotFound, "environment %q not found in the project files", req.Name)
	}
	if req.Baseline != nil {
		for _, p := range plan {
			disk, rerr := os.ReadFile(p.file)
			if rerr != nil {
				return nil, rerr
			}
			if !bytes.Equal(disk, p.orig) {
				return nil, envConflict()
			}
		}
	}
	var done []planned
	restore := func() {
		for _, w := range done {
			_ = writeNoted(w.file, w.orig, req.BeforeWrite)
		}
	}
	for _, p := range plan {
		if werr := writeNoted(p.file, p.updated, req.BeforeWrite); werr != nil {
			restore()
			return nil, werr
		}
		done = append(done, p)
	}
	loaded, lerr := model.Load(root)
	if lerr == nil {
		if verrs := loaded.Validate(); len(verrs) > 0 {
			lerr = verrs[0]
		}
	}
	if lerr != nil {
		restore()
		return nil, verr.Wrap(verr.CodeValidation, lerr, "environment not saved, the project would be invalid")
	}
	written := make([]string, 0, len(plan))
	hashes := make(map[string]string, len(plan))
	for _, p := range plan {
		written = append(written, p.file)
		hashes[p.file] = contentHash(p.updated)
	}
	return &SaveStepResult{File: target, Files: written, Hashes: hashes}, nil
}

func envConflict() error {
	return verr.New(verr.CodeConflict, "this environment changed on disk since it was loaded")
}

// editEnvironments applies req to one file. It returns nil bytes when the
// file needs no change, and found=true when the named environment is here.
func editEnvironments(data []byte, file string, req SaveEnvironmentRequest, createHere bool) ([]byte, bool, error) {
	if strings.HasSuffix(strings.ToLower(file), ".json") {
		return editEnvironmentsJSON(data, req, createHere)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, nil
	}
	root := doc.Content[0]
	envs := mapValue(root, "environments")
	changed, found := false, false

	if createHere {
		if envs == nil {
			key := &yaml.Node{Kind: yaml.ScalarNode, Value: "environments"}
			envs = &yaml.Node{Kind: yaml.SequenceNode}
			root.Content = append(root.Content, key, envs)
		}
		var n yaml.Node
		if err := n.Encode(model.Environment{Name: req.Name, Variables: req.Variables}); err != nil {
			return nil, false, err
		}
		envs.Content = append(envs.Content, &n)
		changed, found = true, true
	}

	for _, e := range seqItems(envs) {
		name := scalar(mapValue(e, "name"))
		if name == req.Name {
			found = true
			if !createHere {
				setMapValue(e, "variables", varsNode(req.Variables))
				changed = true
			}
		}
		if req.Default != nil {
			want := name == req.Name && *req.Default
			if current := scalar(mapValue(e, "default")) == "true"; current != want {
				if want {
					setMapValue(e, "default", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
				} else {
					deleteMapKey(e, "default")
				}
				changed = true
			}
		}
	}
	if !changed {
		return nil, found, nil
	}
	out, err := formatEditedYAML(data, &doc)
	return out, found, err
}

// varsNode builds a mapping node with keys in sorted order (stable diffs).
func varsNode(vars map[string]string) *yaml.Node {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		n.Content = append(n.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: k},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: vars[k]})
	}
	return n
}

func setMapValue(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v.HeadComment, v.LineComment = m.Content[i+1].HeadComment, m.Content[i+1].LineComment
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
}

func deleteMapKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func editEnvironmentsJSON(data []byte, req SaveEnvironmentRequest, createHere bool) ([]byte, bool, error) {
	// UseNumber keeps number text (9007199254740993) through the round
	// trip; float64 would round it in steps the user did not edit.
	var p model.Project
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return nil, false, err
	}
	changed, found := false, false
	if createHere {
		p.Environments = append(p.Environments, model.Environment{Name: req.Name, Variables: req.Variables})
		changed, found = true, true
	}
	for i := range p.Environments {
		e := &p.Environments[i]
		if e.Name == req.Name {
			found = true
			if !createHere {
				e.Variables = req.Variables
				changed = true
			}
		}
		if req.Default != nil {
			want := e.Name == req.Name && *req.Default
			if e.Default != want {
				e.Default = want
				changed = true
			}
		}
	}
	if !changed {
		return nil, found, nil
	}
	encoded, err := json.MarshalIndent(&p, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("encode: %w", err)
	}
	out, err := model.FormatJSON(encoded)
	return out, found, err
}
