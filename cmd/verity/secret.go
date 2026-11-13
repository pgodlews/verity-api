package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pgodlews/verity-api/core/secrets"
	_ "github.com/pgodlews/verity-api/core/secrets/oskeychain" // OS credential store for ${secret:keychain:...}
	"github.com/pgodlews/verity-api/core/verr"
)

func cmdSecret(args []string) int {
	// stdin only: never put a value in argv, shell history, or an echoing prompt.
	if len(args) > 0 && args[0] == "set" {
		if stat, err := os.Stdin.Stat(); err == nil && stat.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprintln(os.Stderr, "secret set: supply the value through piped or redirected stdin")
			return exitUsage
		}
	}
	return secretCommand(args, os.Stdin, os.Stdout, os.Stderr, &secrets.Keychain{})
}

func secretCommand(args []string, in io.Reader, out, stderr io.Writer, store *secrets.Keychain) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: verity secret <set|get|rm> NAME [--json]; set reads stdin, get requires --reveal to print the value")
		return exitUsage
	}
	action := args[0]
	fs := flag.NewFlagSet("secret "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit structured JSON")
	force := fs.Bool("force", false, "replace an existing item (set only)")
	reveal := fs.Bool("reveal", false, "print the plaintext value (get only)")
	pos, err := parsePermuted(fs, args[1:])
	if err != nil {
		return exitUsage
	}
	if action != "set" && action != "get" && action != "rm" {
		fmt.Fprintln(stderr, "secret: expected set, get, or rm")
		return exitUsage
	}
	if len(pos) != 1 || (*reveal && action != "get") || (*force && action != "set") {
		fmt.Fprintln(stderr, "secret: expected one NAME; --reveal is for get only and --force is for set only; values must come from stdin")
		return exitUsage
	}
	reply := func(value any) {
		emitJSONTo(out, value)
	}
	fail := func(err error) int {
		return failJSONTo(out, stderr, *jsonOut, exitConfig, "secret "+action, err)
	}
	name := pos[0]
	if err := secrets.ValidateKeychainName(name); err != nil {
		return fail(err)
	}
	value := ""
	switch action {
	case "set":
		const maxSecretBytes = 1 << 20
		data, err := io.ReadAll(io.LimitReader(in, maxSecretBytes+1))
		if err != nil || len(data) > maxSecretBytes {
			return fail(verr.New(verr.CodeConfig, "cannot read keychain value from stdin (limit 1 MiB)"))
		}
		// Remove the pipe line ending, then normalize whitespace on every platform.
		value = string(data)
		if strings.HasSuffix(value, "\n") {
			value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return fail(verr.New(verr.CodeConfig, "keychain value from stdin must not be empty"))
		}
		exists, err := store.Exists(name)
		if err != nil {
			return fail(err)
		}
		if exists && !*force {
			return fail(verr.Newf(verr.CodeConfig, "keychain item %q already exists; use --force to replace it", name))
		}
		if err := store.Set(name, value); err != nil {
			return fail(err)
		}
		value = ""
	case "get":
		var err error
		value, _, err = store.Resolve(name)
		if err != nil {
			return fail(err)
		}
		if *reveal {
			fmt.Fprintln(stderr, "warning: --reveal prints a plaintext secret; terminal output and logs may retain it")
		} else {
			value = secrets.Mask
		}
	case "rm":
		if err := store.Delete(name); err != nil {
			return fail(err)
		}
	}
	if *jsonOut {
		result := map[string]any{"ok": true, "name": name}
		if action == "get" {
			result["value"] = value
		}
		reply(result)
	} else if action == "get" {
		fmt.Fprintln(out, value)
	} else {
		fmt.Fprintf(out, "%s: %s\n", action, name)
	}
	return exitOK
}
