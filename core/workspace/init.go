package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/verr"
)

const (
	gitignoreStart = "# >>> verity"
	gitignoreEnd   = "# <<< verity"
)

var gitignoreBlock = []string{
	gitignoreStart,
	".env",
	".env.*",
	"!.env.example",
	"reports/",
	".verity/local/",
	gitignoreEnd,
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const starterProject = `schemaVersion: "1.0"
name: My API Tests
environments:
  - name: local
    default: true
    variables:
      BASE_URL: "${env:BASE_URL}"
      API_TOKEN: "${env:API_TOKEN}"
suites:
  - name: Smoke
    cases:
      - name: health
        steps:
          - name: GET health
            protocol: rest
            request:
              method: GET
              url: "${BASE_URL}/health"
            assertions:
              - type: status
                value: 200
`

// InitOptions controls project directory initialization. GitignoreOnly is used
// by importers, which must not create a starter project or env example.
type InitOptions struct {
	Dir           string
	Force         bool
	GitignoreOnly bool
}

// InitResult lists the files created or changed by Init.
type InitResult struct {
	Dir        string   `json:"dir"`
	Project    string   `json:"project,omitempty"`
	Changed    []string `json:"changed"`
	EnvExample string   `json:"envExample,omitempty"`
}

// Init makes a project directory safe to commit. It updates only Verity's
// marked .gitignore block, optionally creates a validated starter project, and
// writes environment variable names without their values.
func Init(opts InitOptions) (*InitResult, error) {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "resolve init directory")
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, verr.Wrap(verr.CodeInternal, err, "create init directory")
	}
	result := &InitResult{Dir: abs, Changed: []string{}}
	if opts.GitignoreOnly {
		if err := initGitignore(abs, result); err != nil {
			return nil, err
		}
		return result, nil
	}

	project, err := findProject(abs)
	if err != nil {
		return nil, err
	}
	createProject := project == ""
	if createProject {
		project = filepath.Join(abs, "project.yaml")
		p, err := model.Decode([]byte(starterProject), project)
		if err != nil {
			return nil, err
		}
		if errs := p.Validate(); len(errs) > 0 {
			return nil, verr.Wrap(verr.CodeValidation, errs[0], "starter project is invalid")
		}
		if err := writeKeepingMode(project, []byte(starterProject)); err != nil {
			return nil, verr.Wrap(verr.CodeInternal, err, "write starter project")
		}
		result.Changed = append(result.Changed, project)
	}

	info, err := Inspect(project)
	if createProject && err != nil {
		return nil, verr.Wrap(verr.CodeValidation, err, "load starter project")
	}
	if !createProject && err != nil {
		return nil, err
	}
	envExample, err := envExampleContents(info)
	if err != nil {
		return nil, err
	}

	if err := initGitignore(abs, result); err != nil {
		return nil, err
	}
	result.Project = project
	result.EnvExample = filepath.Join(abs, ".env.example")
	if _, err := os.Stat(result.EnvExample); err != nil && !os.IsNotExist(err) {
		return nil, verr.Wrap(verr.CodeInternal, err, "inspect .env.example")
	} else if os.IsNotExist(err) || opts.Force {
		if err := writeKeepingMode(result.EnvExample, envExample); err != nil {
			return nil, verr.Wrap(verr.CodeInternal, err, "write .env.example")
		}
		result.Changed = append(result.Changed, result.EnvExample)
	}
	return result, nil
}

func findProject(dir string) (string, error) {
	for _, name := range []string{"project.yaml", "project.yml", "project.json"} {
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		if err == nil {
			if !st.Mode().IsRegular() {
				return "", verr.Newf(verr.CodeConfig, "%s is not a regular file", path)
			}
			return path, nil
		}
		if !os.IsNotExist(err) {
			return "", verr.Wrap(verr.CodeInternal, err, "find project file")
		}
	}
	return "", nil
}

func initGitignore(dir string, result *InitResult) error {
	path := filepath.Join(dir, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return verr.Wrap(verr.CodeInternal, err, "read .gitignore")
	}
	updated, err := updateGitignore(data)
	if err != nil {
		return err
	}
	if err == nil && string(updated) == string(data) {
		return nil
	}
	if err := writeKeepingMode(path, updated); err != nil {
		return verr.Wrap(verr.CodeInternal, err, "write .gitignore")
	}
	result.Changed = append(result.Changed, path)
	return nil
}

func updateGitignore(data []byte) ([]byte, error) {
	newline := "\n"
	if strings.Contains(string(data), "\r\n") {
		newline = "\r\n"
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	var lines []string
	if text != "" {
		lines = strings.Split(text, "\n")
	}
	// Exactly one start marker followed by exactly one end marker, or none
	// of either: anything else is refused rather than guessed at, so lines
	// outside the block are never lost.
	start, end := -1, -1
	for i, line := range lines {
		switch line {
		case gitignoreStart:
			if start >= 0 {
				return nil, verr.New(verr.CodeConfig, ".gitignore has duplicate Verity block markers")
			}
			start = i
		case gitignoreEnd:
			if end >= 0 {
				return nil, verr.New(verr.CodeConfig, ".gitignore has duplicate Verity block markers")
			}
			end = i
		}
	}
	if start >= 0 || end >= 0 {
		if start < 0 || end < start {
			return nil, verr.New(verr.CodeConfig, ".gitignore has an incomplete Verity block")
		}
		replaced := append([]string{}, lines[:start]...)
		replaced = append(replaced, gitignoreBlock...)
		lines = append(replaced, lines[end+1:]...)
	} else {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, gitignoreBlock...)
	}
	out := strings.Join(lines, newline)
	if !strings.HasSuffix(out, newline) {
		out += newline
	}
	return []byte(out), nil
}

func envExampleContents(info *ProjectInfo) ([]byte, error) {
	var out strings.Builder
	fmt.Fprintf(&out, "# Environment variables referenced by %s.\n", filepath.Base(info.Path))
	for _, ref := range info.EnvRefs {
		if !envNamePattern.MatchString(ref.Name) {
			return nil, verr.Newf(verr.CodeValidation, "environment variable name %q is not valid in a dotenv file", ref.Name)
		}
		where := make([]string, len(ref.UsedIn))
		for i, use := range ref.UsedIn {
			where[i] = strings.Join(strings.Fields(use), " ")
		}
		fmt.Fprintf(&out, "# Used in: %s.\n%s=\n", strings.Join(where, ", "), ref.Name)
	}
	return []byte(out.String()), nil
}
