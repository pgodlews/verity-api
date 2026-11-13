package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"github.com/pgodlews/verity-api/core/verr"
)

// cmdPlugin handles `verity plugin <subcommand>`. Currently supports:
//
//	verity plugin new <type> <name> [--out DIR] [--force] [--json]
//
// which scaffolds a working out-of-process plugin (interface stubs + tests +
// README + manifest) for the given extension type.
func cmdPlugin(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "plugin: usage: verity plugin new <type> <name> [--out DIR] [--force] [--json]")
		return exitUsage
	}
	switch args[0] {
	case "new":
		return cmdPluginNew(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "plugin: unknown subcommand %q\n", args[0])
		return exitUsage
	}
}

// pluginNamePattern is a plugin name: it becomes a directory, a Go module
// path element, a binary name, and (camel-cased) a Go identifier.
var pluginNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// exportedIdent camel-cases name on '-' and '_' into an exported Go
// identifier: "my-plugin" -> "MyPlugin".
func exportedIdent(name string) string {
	var b strings.Builder
	upper := true
	for _, r := range name {
		if r == '-' || r == '_' {
			upper = true
			continue
		}
		if upper {
			b.WriteRune(unicode.ToUpper(r))
			upper = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var validPluginTypes = map[string]bool{
	"protocol": true, "assertion": true, "reporter": true,
	"auth": true, "datasource": true,
}

func cmdPluginNew(args []string) int {
	fs := flag.NewFlagSet("plugin new", flag.ContinueOnError)
	out := fs.String("out", "", "output directory (default ./<name>)")
	force := fs.Bool("force", false, "write into a non-empty output directory, replacing scaffold files")
	jsonOut := fs.Bool("json", false, "emit a structured JSON result")
	fs.SetOutput(os.Stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) < 2 {
		fmt.Fprintln(os.Stderr, "plugin new: usage: verity plugin new <protocol|assertion|reporter|auth|datasource> <name> [--out DIR] [--force] [--json]")
		return exitUsage
	}
	ptype, name := pos[0], pos[1]
	if !validPluginTypes[ptype] {
		fmt.Fprintf(os.Stderr, "plugin new: unknown type %q (want protocol|assertion|reporter|auth|datasource)\n", ptype)
		return exitUsage
	}
	if !pluginNamePattern.MatchString(name) {
		fmt.Fprintf(os.Stderr, "plugin new: invalid name %q (want lowercase letters, digits, '-' or '_', starting with a letter)\n", name)
		return exitUsage
	}

	dir := *out
	if dir == "" {
		dir = name
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 && !*force {
		return failJSON(*jsonOut, exitConfig, "plugin new",
			verr.Newf(verr.CodeConfig, "output directory %s is not empty; pass --force to overwrite the scaffold files", dir))
	}
	data := scaffoldData{
		Name:    name,
		Type:    ptype,
		Title:   exportedIdent(name),
		TypeCap: exportedIdent(ptype),
		Module:  "example.com/" + name,
	}

	files := scaffoldFiles(ptype)
	written := make([]string, 0, len(files))
	for rel, tmpl := range files {
		target := filepath.Join(dir, rel)
		if err := writeTemplate(target, tmpl, data); err != nil {
			return failJSON(*jsonOut, exitInternal, "plugin new", verr.Wrap(verr.CodeInternal, err, "write scaffold"))
		}
		written = append(written, target)
	}
	sort.Strings(written)

	if *jsonOut {
		emitJSON(map[string]any{"ok": true, "type": ptype, "name": name, "dir": dir, "files": written})
		return exitOK
	}
	fmt.Printf("Scaffolded %s plugin %q in %s\n", ptype, name, dir)
	fmt.Printf("Next:\n  cd %s\n  go mod tidy\n  go test ./...\n  go build -o %s .\n", dir, name)
	fmt.Printf("Then register it with the host via the plugins config (see README.md).\n")
	return exitOK
}

type scaffoldData struct {
	Name    string
	Type    string
	Title   string
	TypeCap string
	Module  string
}

func writeTemplate(path, tmpl string, data scaffoldData) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	t, err := template.New(filepath.Base(path)).Parse(tmpl)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := t.Execute(f, data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
