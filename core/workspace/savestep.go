package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/verr"
)

// SaveStepRequest identifies one step and its new content.
type SaveStepRequest struct {
	Path  string     `json:"path"`
	Suite string     `json:"suite"`
	Case  string     `json:"case"`
	Index int        `json:"index"`
	Step  model.Step `json:"step"`
	// Baseline is the sha256 of the step as loaded (HashStep), not the file
	// hash returned in SaveStepResult.Hashes. When it is set, a mismatch is
	// a conflict and writes nothing.
	Baseline string `json:"baseline,omitempty"`
	// FileBaseline is the sha256 of the whole file that defines the step.
	// It is compared only when Baseline is empty: the desktop editor sends
	// it when HashStep failed and there is no step hash. A mismatch is a
	// conflict and writes nothing. Empty together with Baseline skips both
	// checks, which is only for CLI and dev-bridge callers. The desktop
	// App.SaveStep requires one of the two.
	FileBaseline string `json:"fileBaseline,omitempty"`
	// BeforeWrite is called with the bytes about to be written, before the
	// rename. The GUI records their hash so the watcher can ignore its own
	// save. Nil is fine. It is not part of the JSON API.
	BeforeWrite func(path string, data []byte) `json:"-"`
}

// SaveStepResult names the file that was rewritten. Files lists every path
// this save wrote (an environment save can rewrite several). Hashes are the
// sha256 of those file bytes, keyed by path. An environment save reuses them
// as the next file baseline. They are not step baselines; a step baseline is
// HashStep of the step.
type SaveStepResult struct {
	File   string            `json:"file"`
	Files  []string          `json:"files,omitempty"`
	Hashes map[string]string `json:"hashes,omitempty"`
}

// SaveStep writes one step back to the file that defines it — the project
// file or one of its includes. YAML files are edited as a node tree, so
// comments and the rest of the file survive; JSON files are re-encoded. Saves
// normalize files to two-space indentation, exactly like verity fmt. The
// project is reloaded and validated afterwards, and the file is restored if
// the edit made it invalid.
func SaveStep(req SaveStepRequest) (*SaveStepResult, error) {
	step, err := normalizedStepJSONText(req.Step)
	if err != nil {
		return nil, err
	}
	req.Step = step
	// Credentials belong in ${env:NAME} references, never in project files.
	found, err := CheckStep(req.Step)
	if err != nil {
		return nil, err
	}
	if len(found) > 0 {
		return nil, literalSecretError(found)
	}
	unlock := lockProject(req.Path)
	defer unlock()
	root, err := filepath.Abs(req.Path)
	if err != nil {
		return nil, err
	}
	files, err := projectFiles(root)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		orig, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		updated, found, err := replaceStep(orig, f, req)
		if err != nil {
			return nil, verr.Wrap(verr.CodeConfig, err, "edit "+filepath.Base(f))
		}
		if !found {
			continue
		}
		if req.Baseline != "" || req.FileBaseline != "" {
			disk, rerr := os.ReadFile(f)
			if rerr != nil {
				return nil, rerr
			}
			if !bytes.Equal(disk, orig) {
				return nil, stepConflict()
			}
			if req.Baseline != "" {
				got, ok, herr := hashStepIn(orig, f, req.Suite, req.Case, req.Index)
				if herr != nil {
					return nil, verr.Wrap(verr.CodeConfig, herr, "read "+filepath.Base(f))
				}
				if !ok || got != req.Baseline {
					return nil, stepConflict()
				}
			} else if contentHash(orig) != req.FileBaseline {
				return nil, stepConflict()
			}
		}
		if err := writeNoted(f, updated, req.BeforeWrite); err != nil {
			return nil, err
		}
		p, lerr := model.Load(root)
		if lerr == nil {
			if verrs := p.Validate(); len(verrs) > 0 {
				lerr = verrs[0]
			}
		}
		if lerr != nil {
			if rerr := writeNoted(f, orig, req.BeforeWrite); rerr != nil {
				return nil, fmt.Errorf("edit made the project invalid (%v) and restoring %s failed: %w", lerr, f, rerr)
			}
			return nil, verr.Wrap(verr.CodeValidation, lerr, "step not saved, the project would be invalid")
		}
		return &SaveStepResult{
			File:   f,
			Files:  []string{f},
			Hashes: map[string]string{f: contentHash(updated)},
		}, nil
	}
	return nil, verr.Newf(verr.CodeNotFound, "step %d of %s / %s not found in the project files", req.Index, req.Suite, req.Case)
}

func stepConflict() error {
	return verr.New(verr.CodeConflict, "this request changed on disk since it was loaded")
}

func writeNoted(path string, data []byte, note func(string, []byte)) error {
	if note != nil {
		note(path, data)
	}
	return writeKeepingMode(path, data)
}

// hashStepIn hashes the step at suite/case/index in one project file.
// ok is false when that case is not in the file.
func hashStepIn(data []byte, file, suite, kase string, index int) (string, bool, error) {
	p, err := model.Decode(data, file)
	if err != nil {
		return "", false, err
	}
	for _, s := range p.Suites {
		if s.Name != suite {
			continue
		}
		for _, c := range s.Cases {
			if c.Name != kase {
				continue
			}
			if index < 0 || index >= len(c.Steps) {
				return "", false, fmt.Errorf("step index %d out of range (case has %d steps)", index, len(c.Steps))
			}
			h, err := HashStep(c.Steps[index])
			return h, true, err
		}
	}
	return "", false, nil
}

// ProjectFiles lists a project and its included fragments, depth-first. Feed
// and upload files are deliberately excluded.
func ProjectFiles(path string) ([]string, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return projectFiles(root)
}

// projectFiles lists the root file followed by its includes, depth-first.
func projectFiles(root string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	var walk func(string) error
	walk = func(f string) error {
		if seen[f] {
			return nil
		}
		seen[f] = true
		out = append(out, f)
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		p, err := model.Decode(data, f)
		if err != nil {
			return err
		}
		for _, inc := range p.Include {
			if !filepath.IsAbs(inc) {
				inc = filepath.Join(filepath.Dir(f), inc)
			}
			if err := walk(filepath.Clean(inc)); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk(root)
}

func replaceStep(data []byte, file string, req SaveStepRequest) ([]byte, bool, error) {
	if strings.HasSuffix(strings.ToLower(file), ".json") {
		return replaceStepJSON(data, req)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, err
	}
	if len(doc.Content) == 0 {
		return nil, false, nil
	}
	steps := findSteps(doc.Content[0], req.Suite, req.Case)
	if steps == nil {
		return nil, false, nil
	}
	if req.Index < 0 || req.Index >= len(steps.Content) {
		return nil, false, fmt.Errorf("step index %d out of range (case has %d steps)", req.Index, len(steps.Content))
	}
	var n yaml.Node
	if err := n.Encode(req.Step); err != nil {
		return nil, false, err
	}
	old := steps.Content[req.Index]
	n.HeadComment, n.LineComment, n.FootComment = old.HeadComment, old.LineComment, old.FootComment
	steps.Content[req.Index] = &n

	out, err := formatEditedYAML(data, &doc)
	return out, true, err
}

func formatEditedYAML(original []byte, doc *yaml.Node) ([]byte, error) {
	return model.FormatYAMLNode(original, doc)
}

// findSteps returns the steps sequence of suite/case in a project mapping.
func findSteps(root *yaml.Node, suite, kase string) *yaml.Node {
	for _, s := range seqItems(mapValue(root, "suites")) {
		if scalar(mapValue(s, "name")) != suite {
			continue
		}
		for _, c := range seqItems(mapValue(s, "cases")) {
			if scalar(mapValue(c, "name")) == kase {
				if steps := mapValue(c, "steps"); steps != nil && steps.Kind == yaml.SequenceNode {
					return steps
				}
			}
		}
	}
	return nil
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func seqItems(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

func scalar(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	return n.Value
}

func replaceStepJSON(data []byte, req SaveStepRequest) ([]byte, bool, error) {
	// UseNumber keeps number text (9007199254740993) through the round
	// trip; float64 would round it in steps the user did not edit.
	var p model.Project
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return nil, false, err
	}
	for si := range p.Suites {
		if p.Suites[si].Name != req.Suite {
			continue
		}
		for ci := range p.Suites[si].Cases {
			c := &p.Suites[si].Cases[ci]
			if c.Name != req.Case {
				continue
			}
			if req.Index < 0 || req.Index >= len(c.Steps) {
				return nil, false, fmt.Errorf("step index %d out of range (case has %d steps)", req.Index, len(c.Steps))
			}
			c.Steps[req.Index] = req.Step
			encoded, err := model.MarshalJSONIndent(&p)
			if err != nil {
				return nil, false, err
			}
			out, err := model.FormatJSON(encoded)
			return out, true, err
		}
	}
	return nil, false, nil
}

// writeKeepingMode replaces a file via a temp file and rename, keeping its mode.
func writeKeepingMode(path string, data []byte) error {
	// Write through a symlinked project file: replace its target, not the
	// link, so the link and the file it points to stay as the user set them.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".verity-save-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
