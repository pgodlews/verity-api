package workspace

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pgodlews/verity-api/core/model"
)

// EnvRef is an ${env:NAME} reference and whether NAME is set on this
// machine. The value itself is never read into the result.
type EnvRef struct {
	Name   string   `json:"name"`
	Set    bool     `json:"set"`
	UsedIn []string `json:"usedIn"`
}

// UndefinedVar is a ${var} that nothing defines in some environments.
type UndefinedVar struct {
	Name      string   `json:"name"`
	MissingIn []string `json:"missingIn"`
	UsedIn    []string `json:"usedIn"`
}

// CaptureDef is a variable a step's transfer captures at run time.
type CaptureDef struct {
	Name string `json:"name"`
	From string `json:"from"`
	Expr string `json:"expr,omitempty"`
	// Scope is the transfer scope ("" or "case", or "run").
	Scope string `json:"scope,omitempty"`
	Step  string `json:"step"`
}

// StepOrigin is the file that defines one step. Index is the step's position
// in that file's case, which is the index SaveStep writes. Occurrence is the
// zero-based count of earlier steps in that case with the same name. Hash is
// HashStep of the step as loaded; SaveStep refuses a write when the file's
// step no longer hashes to it. The desktop app uses both to tell whether an
// external edit touched the request that is open.
type StepOrigin struct {
	Suite      string `json:"suite"`
	Case       string `json:"case"`
	Index      int    `json:"index"`
	File       string `json:"file"`
	Occurrence int    `json:"occurrence"`
	Hash       string `json:"hash,omitempty"`
}

// FileHash is the sha256 of one project file as loaded. SaveEnvironment
// compares it before writing.
type FileHash struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// ProjectInfo is everything a frontend needs to show a project.
type ProjectInfo struct {
	Path          string         `json:"path"`
	Project       *model.Project `json:"project"`
	EnvRefs       []EnvRef       `json:"envRefs"`
	UndefinedVars []UndefinedVar `json:"undefinedVars"`
	Captures      []CaptureDef   `json:"captures"`
	// StepFiles names the file each step is defined in. Empty when the
	// project files could not be walked; the project path is the fallback.
	StepFiles []StepOrigin `json:"stepFiles,omitempty"`
	// FileHashes are the project file and its fragments, in walk order.
	FileHashes []FileHash `json:"fileHashes,omitempty"`
}

var refPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// Inspect loads a project (with includes merged) and reports its secret
// references, variables that are undefined in some environments, and the
// variables captured at run time.
func Inspect(path string) (*ProjectInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p, err := model.Load(abs)
	if err != nil {
		return nil, err
	}
	if err := exposeJSONText(p); err != nil {
		return nil, err
	}
	info := &ProjectInfo{
		Path: abs, Project: p, EnvRefs: []EnvRef{}, UndefinedVars: []UndefinedVar{},
		Captures: []CaptureDef{}, StepFiles: stepOrigins(abs), FileHashes: fileHashes(abs),
	}

	envUse := map[string][]string{}
	undef := map[string]*UndefinedVar{}
	noteEnv := func(v any, where string) {
		for _, ref := range refs(v) {
			if name, ok := strings.CutPrefix(ref, "env:"); ok {
				envUse[name] = appendOnce(envUse[name], where)
			}
		}
	}
	// noteUndefined records plain ${name} refs in v that defined() rejects
	// for some environment.
	noteUndefined := func(v any, where string, defined func(env model.Environment, name string) bool) {
		for _, ref := range refs(v) {
			if strings.Contains(ref, ":") {
				continue // env:/secret: refs are not variables
			}
			for _, env := range p.Environments {
				if defined(env, ref) {
					continue
				}
				u := undef[ref]
				if u == nil {
					u = &UndefinedVar{Name: ref}
					undef[ref] = u
				}
				u.MissingIn = appendOnce(u.MissingIn, env.Name)
				u.UsedIn = appendOnce(u.UsedIn, where)
			}
		}
	}
	inEnv := func(env model.Environment, name string) bool {
		_, ok := env.Variables[name]
		return ok
	}

	for _, env := range p.Environments {
		noteEnv(env.Variables, "environment "+env.Name)
	}
	for _, a := range p.Auth {
		where := "auth " + a.Name
		noteEnv(a.Config, where)
		// Auth configs resolve once per run from environment variables only.
		noteUndefined(a.Config, where, inEnv)
	}

	// Run-scoped captures are visible to every case after the capturing one.
	runScoped := map[string]bool{}
	for _, s := range p.Suites {
		noteEnv(s.Vars, "suite "+s.Name)
		for _, c := range inExecutionOrder(s) {
			caseWhere := s.Name + "/" + c.Name
			noteEnv(c.Vars, caseWhere)
			if c.Feed != nil {
				noteEnv(c.Feed.Config, caseWhere+" feed")
				for _, row := range c.Feed.Inline {
					noteEnv(row, caseWhere+" feed")
				}
			}
			// Feed columns are unknown without reading the feed; trust them.
			hasFeed := c.Feed != nil
			local := map[string]bool{}
			for k := range runScoped {
				local[k] = true
			}
			for k := range s.Vars {
				local[k] = true
			}
			for k := range c.Vars {
				local[k] = true
			}
			for _, st := range c.Steps {
				where := s.Name + "/" + c.Name + "/" + st.Name
				for k := range st.Vars {
					local[k] = true
				}
				fields := []any{st.Request, st.Vars}
				for _, a := range st.Assertions {
					fields = append(fields, a.Expr, a.Value, a.Config)
				}
				for _, f := range fields {
					noteEnv(f, where)
					if !hasFeed {
						noteUndefined(f, where, func(env model.Environment, name string) bool {
							return local[name] || inEnv(env, name)
						})
					}
				}
				// Captures become visible to the steps after this one.
				for _, t := range st.Transfers {
					noteEnv(t.Expr, where)
					noteEnv(t.Default, where)
					local[t.Name] = true
					if t.Scope == model.TransferScopeRun {
						runScoped[t.Name] = true
					}
					info.Captures = append(info.Captures, CaptureDef{Name: t.Name, From: t.From, Expr: t.Expr, Scope: t.Scope, Step: where})
				}
			}
		}
	}

	for name, used := range envUse {
		_, set := os.LookupEnv(name)
		info.EnvRefs = append(info.EnvRefs, EnvRef{Name: name, Set: set, UsedIn: used})
	}
	sort.Slice(info.EnvRefs, func(i, j int) bool { return info.EnvRefs[i].Name < info.EnvRefs[j].Name })
	for _, u := range undef {
		info.UndefinedVars = append(info.UndefinedVars, *u)
	}
	sort.Slice(info.UndefinedVars, func(i, j int) bool { return info.UndefinedVars[i].Name < info.UndefinedVars[j].Name })
	return info, nil
}

// refs returns the inner text of every ${...} reference in a value tree.
func refs(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, m := range refPattern.FindAllStringSubmatch(x, -1) {
				out = append(out, strings.TrimSpace(m[1]))
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case map[string]string:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

// stepOrigins records the first file that defines each suite/case/index.
// SaveStep walks files in the same order and edits the first match.
func stepOrigins(root string) []StepOrigin {
	files, err := projectFiles(root)
	if err != nil {
		return nil
	}
	var out []StepOrigin
	seen := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		p, err := model.Decode(data, f)
		if err != nil {
			continue
		}
		for _, s := range p.Suites {
			for _, c := range s.Cases {
				occ := map[string]int{}
				for i, st := range c.Steps {
					key := s.Name + "\x00" + c.Name + "\x00" + strconv.Itoa(i)
					n := occ[st.Name]
					occ[st.Name] = n + 1
					if seen[key] {
						continue
					}
					seen[key] = true
					hash, _ := HashStep(st)
					out = append(out, StepOrigin{
						Suite: s.Name, Case: c.Name, Index: i, File: f,
						Occurrence: n, Hash: hash,
					})
				}
			}
		}
	}
	return out
}

func fileHashes(root string) []FileHash {
	files, err := projectFiles(root)
	if err != nil {
		return nil
	}
	out := make([]FileHash, 0, len(files))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		out = append(out, FileHash{Path: f, Hash: contentHash(b)})
	}
	return out
}

func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// inExecutionOrder lists a suite's cases the way the runner executes them:
// each setup hook step as its own case, then the cases, then teardown.
func inExecutionOrder(s model.Suite) []model.TestCase {
	var out []model.TestCase
	hook := func(prefix string, h *model.Hook) {
		if h == nil {
			return
		}
		for _, st := range h.Steps {
			out = append(out, model.TestCase{Name: prefix + h.Name, Steps: []model.Step{st}})
		}
	}
	hook("setup:", s.Setup)
	out = append(out, s.Cases...)
	hook("teardown:", s.Teardown)
	return out
}
