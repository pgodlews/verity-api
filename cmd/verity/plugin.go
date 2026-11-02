package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// cmdPlugin handles `verity plugin <subcommand>`. Currently supports:
//
//	verity plugin new <type> <name> [--out DIR]
//
// which scaffolds a working out-of-process plugin (interface stubs + tests +
// README + manifest) for the given extension type.
func cmdPlugin(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "plugin: usage: verity plugin new <type> <name> [--out DIR]")
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

var validPluginTypes = map[string]bool{
	"protocol": true, "assertion": true, "reporter": true,
	"auth": true, "datasource": true,
}

func cmdPluginNew(args []string) int {
	fs := flag.NewFlagSet("plugin new", flag.ContinueOnError)
	out := fs.String("out", "", "output directory (default ./<name>)")
	fs.SetOutput(os.Stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) < 2 {
		fmt.Fprintln(os.Stderr, "plugin new: usage: verity plugin new <protocol|assertion|reporter|auth|datasource> <name> [--out DIR]")
		return exitUsage
	}
	ptype, name := pos[0], pos[1]
	if !validPluginTypes[ptype] {
		fmt.Fprintf(os.Stderr, "plugin new: unknown type %q (want protocol|assertion|reporter|auth|datasource)\n", ptype)
		return exitUsage
	}

	dir := *out
	if dir == "" {
		dir = name
	}
	data := scaffoldData{
		Name:    name,
		Type:    ptype,
		Title:   strings.Title(name), //nolint:staticcheck // simple ASCII titling is fine here
		TypeCap: strings.Title(ptype),
		Module:  "example.com/" + name,
	}

	files := scaffoldFiles(ptype)
	for rel, tmpl := range files {
		target := filepath.Join(dir, rel)
		if err := writeTemplate(target, tmpl, data); err != nil {
			fmt.Fprintf(os.Stderr, "plugin new: %v\n", err)
			return exitInternal
		}
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
