package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pgodlews/verity-api/core/secretscan"
	"github.com/pgodlews/verity-api/core/vcs"
	"github.com/pgodlews/verity-api/core/verr"
)

// envFile matches dotenv files (any letter case: .ENV is the same file on
// case-insensitive file systems). .env.example is meant to be committed, but
// only with empty or placeholder values, which the staged scan checks.
var envFile = regexp.MustCompile(`(?i)^\.env(\..+)?$`)

func isEnvExample(base string) bool { return strings.EqualFold(base, ".env.example") }

// placeholder matches a .env.example value that only describes what goes
// there: "<your token>", "<example-key>", "<set me>". "<hunter2>" is a value.
var placeholder = regexp.MustCompile(`(?i)^<\s*(your|example|replace|set|enter|insert|placeholder|changeme|todo)\b[^>]*>$|^<[^>]*\s[^>]*>$`)

// StagedScan is the result of the in-process `scan-secrets --staged` check.
// Findings come from the project and its fragments as staged, plus values
// in a staged .env.example. EnvFiles are staged dotenv files that must not
// be committed. A clean scan has neither.
type StagedScan struct {
	Findings []secretscan.Finding
	EnvFiles []string
}

// Clean reports whether the index has nothing the secret scan would reject.
func (s *StagedScan) Clean() bool {
	return s != nil && len(s.Findings) == 0 && len(s.EnvFiles) == 0
}

// ScanStaged scans the git index the way `verity scan-secrets --staged`
// does: project files and fragments as staged, staged dotenv files, and
// values in a staged .env.example. The working tree is not read.
func ScanStaged(projectPath string) (*StagedScan, error) {
	abs, dir, err := projectDir(projectPath)
	if err != nil {
		return nil, err
	}
	read, root, err := indexReader(dir)
	if err != nil {
		return nil, err
	}
	found, err := ScanSecrets(abs, read)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, verr.New(verr.CodeConfig, "the project file is not in the index, so the staged secret scan cannot read it")
		}
		return nil, verr.Wrap(verr.CodeConfig, err, "scan staged project")
	}
	names, err := vcs.StagedChanges(root)
	if err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "list staged files")
	}
	var envFiles []string
	var examples []string
	for _, name := range names {
		base := filepath.Base(name)
		switch {
		case !envFile.MatchString(base):
		case isEnvExample(base):
			examples = append(examples, name)
		default:
			envFiles = append(envFiles, name)
		}
	}
	for _, ex := range examples {
		data, rerr := vcs.ReadIndex(root, ex)
		if rerr != nil {
			return nil, verr.Wrap(verr.CodeConfig, rerr, "read staged "+ex)
		}
		found = append(found, scanDotenv(ex, data)...)
	}
	if found == nil {
		found = []secretscan.Finding{}
	}
	return &StagedScan{Findings: found, EnvFiles: envFiles}, nil
}

// ScanCommit scans the blobs this commit will record: the selected paths,
// not the rest of the index. A staged rename is refused when either side's
// base name is a dotenv file and the rename touches the selection.
// .env.example is still scanned for real values.
func ScanCommit(projectPath string, rels []string) (*StagedScan, error) {
	abs, dir, err := projectDir(projectPath)
	if err != nil {
		return nil, err
	}
	if !vcs.Available() {
		return nil, verr.New(verr.CodeConfig, "git was not found on PATH; install Git to scan the index")
	}
	root, err := vcs.RepoRoot(dir)
	if err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "locate git repository")
	}
	selected := map[string]struct{}{}
	for _, rel := range rels {
		selected[rel] = struct{}{}
	}
	ops, err := vcs.StagedOps(root)
	if err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "list staged files")
	}
	seenEnv := map[string]struct{}{}
	seenExample := map[string]struct{}{}
	var envFiles []string
	var examples []string
	consider := func(name string) {
		base := filepath.Base(name)
		if !envFile.MatchString(base) {
			return
		}
		if isEnvExample(base) {
			if _, ok := seenExample[name]; ok {
				return
			}
			seenExample[name] = struct{}{}
			examples = append(examples, name)
			return
		}
		if _, ok := seenEnv[name]; ok {
			return
		}
		seenEnv[name] = struct{}{}
		envFiles = append(envFiles, name)
	}
	for _, rel := range rels {
		consider(rel)
	}
	for _, op := range ops {
		if op.From == "" {
			continue
		}
		_, selNew := selected[op.Path]
		_, selOld := selected[op.From]
		if !selNew && !selOld {
			continue
		}
		consider(op.Path)
		consider(op.From)
	}
	set := projectFileSet(abs)
	var found []secretscan.Finding
	for _, rel := range rels {
		absPath := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
		if _, ok := set[absPath]; !ok {
			continue
		}
		data, rerr := vcs.ReadIndex(root, rel)
		if rerr != nil {
			return nil, verr.Wrap(verr.CodeConfig, rerr, "read staged "+rel)
		}
		file := relToProject(dir, absPath)
		part, serr := secretscan.ScanFile(file, data)
		if serr != nil {
			return nil, verr.Wrap(verr.CodeConfig, serr, "scan staged "+rel)
		}
		found = append(found, part...)
	}
	for _, ex := range examples {
		data, rerr := vcs.ReadIndex(root, ex)
		if rerr != nil {
			return nil, verr.Wrap(verr.CodeConfig, rerr, "read staged "+ex)
		}
		found = append(found, scanDotenv(ex, data)...)
	}
	if found == nil {
		found = []secretscan.Finding{}
	}
	return &StagedScan{Findings: found, EnvFiles: envFiles}, nil
}

// StagedReader returns a ReadFunc that reads the git index for the repository
// containing dir. `verity fmt --check --staged` and the secret scan share it.
func StagedReader(dir string) (ReadFunc, error) {
	read, _, err := indexReader(dir)
	return read, err
}

func indexReader(dir string) (ReadFunc, string, error) {
	if !vcs.Available() {
		return nil, "", verr.New(verr.CodeConfig, "git was not found on PATH; install Git to scan the index")
	}
	root, err := vcs.RepoRoot(dir)
	if err != nil {
		if errors.Is(err, vcs.ErrUnavailable) {
			return nil, "", verr.New(verr.CodeConfig, "git was not found on PATH; install Git to scan the index")
		}
		if vcs.IsNotRepository(err) {
			return nil, "", verr.Newf(verr.CodeConfig, "%s is not inside a git repository", dir)
		}
		return nil, "", verr.Wrap(verr.CodeConfig, err, "locate git repository")
	}
	read := func(file string) ([]byte, error) {
		rel, rerr := repoRelative(root, file)
		if rerr != nil {
			return nil, fs.ErrNotExist
		}
		return vcs.ReadIndex(root, rel)
	}
	return read, root, nil
}

// scanDotenv checks KEY=VALUE lines of a .env.example: a credential-named
// key with a value, or a token-shaped value, is a real secret.
func scanDotenv(file string, data []byte) []secretscan.Finding {
	var out []secretscan.Finding
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if placeholder.MatchString(v) {
			continue
		}
		if found, _ := secretscan.Check(k, v, true); found {
			out = append(out, secretscan.Finding{File: file, Line: i + 1, Path: []string{k}, Key: k, Reason: "example", EnvVar: k})
		}
	}
	return out
}

// secretsRefusal is the commit error for a staged scan that is not clean.
// The message names each finding and never includes the secret value.
func secretsRefusal(scan *StagedScan) error {
	var b strings.Builder
	b.WriteString("Commit refused: plaintext secrets are staged.")
	n := 0
	if scan != nil {
		for _, f := range scan.Findings {
			n++
			b.WriteByte('\n')
			if f.File != "" {
				fmt.Fprintf(&b, "%s:%d: %s", f.File, f.Line, f.Message())
			} else {
				b.WriteString(f.Message())
			}
		}
		for _, f := range scan.EnvFiles {
			n++
			fmt.Fprintf(&b, "\n%s: dotenv files hold real secret values and must not be committed", f)
		}
	}
	b.WriteString("\nNothing was committed.")
	return verr.New(verr.CodeValidation, b.String()).WithDetail("secrets", n)
}
