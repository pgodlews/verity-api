// dotenv.go loads --env-file values into the process environment.
//
// The parser lives in the CLI rather than the runner. ${env:NAME} already
// reads the process environment when a run interpolates secrets, so loading a
// file is process setup the CLI does before runner.Run. The runner stays free
// of dotenv syntax and file paths, and no new dependency is required.
//
// Grammar, one assignment per line:
//
//	# comment, or a blank line, is ignored
//	[export] KEY=VALUE
//	optional spaces or tabs around '='
//	VALUE may be unquoted, 'single-quoted', or "double-quoted"
//	double quotes interpret \n \r \t \\ \" \'; any other \X is kept as the two characters \ and X
//	single quotes are literal, so a ' cannot appear inside them
//	a trailing comment is whitespace plus '#' outside quotes
//	KEY matches [A-Za-z_][A-Za-z0-9_]*
//
// The last assignment of a key in a file wins. Across repeated --env-file
// flags, later files win. Values are never included in errors.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pgodlews/verity-api/core/verr"
)

// dotenvMaxLine is the longest physical line accepted. It is large enough for
// a PEM blob in a quoted value and small enough that a runaway line fails
// closed instead of consuming memory.
const dotenvMaxLine = 1 << 20

// loadEnvFiles parses each path and applies the merged result to the process
// environment. Parsing finishes before any variable is set, so a malformed
// later file does not leave a partial update. When override is false, a
// variable that is already set (including set to empty) is left alone.
func loadEnvFiles(paths []string, override bool) error {
	if len(paths) == 0 {
		return nil
	}
	merged := make(map[string]string)
	for _, p := range paths {
		kv, err := parseDotenvFile(p)
		if err != nil {
			return err
		}
		for k, v := range kv {
			merged[k] = v
		}
	}
	for k, v := range merged {
		if !override {
			if _, exists := os.LookupEnv(k); exists {
				continue
			}
		}
		if err := os.Setenv(k, v); err != nil {
			return verr.Newf(verr.CodeConfig, "cannot set environment variable %s", k)
		}
	}
	return nil
}

func parseDotenvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, verr.Newf(verr.CodeConfig, "cannot read env file: %v", err).WithPath(path)
	}
	defer f.Close()
	return parseDotenv(path, f)
}

// parseDotenv reads a dotenv document from r. filename is only used to locate
// a malformed line; it is not opened.
func parseDotenv(filename string, r io.Reader) (map[string]string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), dotenvMaxLine)
	out := make(map[string]string)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if lineNo == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		key, val, skip, err := parseDotenvLine(line)
		if err != nil {
			return nil, verr.Newf(verr.CodeConfig, "malformed dotenv line: %s", err.Error()).
				WithPath(fmt.Sprintf("%s:%d", filename, lineNo))
		}
		if skip {
			continue
		}
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, verr.Newf(verr.CodeConfig, "cannot read env file: %v", err).WithPath(filename)
	}
	return out, nil
}

func parseDotenvLine(line string) (key, value string, skip bool, err error) {
	line = strings.TrimRight(line, "\r")
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", true, nil
	}
	rest, err := stripExport(trimmed)
	if err != nil {
		return "", "", false, err
	}
	if rest == "" || !isEnvNameStart(rest[0]) {
		return "", "", false, errors.New("invalid name")
	}
	i := 1
	for i < len(rest) && isEnvNameCont(rest[i]) {
		i++
	}
	key = rest[:i]
	if i < len(rest) && rest[i] != ' ' && rest[i] != '\t' && rest[i] != '=' {
		return "", "", false, errors.New("invalid name")
	}
	rest = strings.TrimLeft(rest[i:], " \t")
	if rest == "" || rest[0] != '=' {
		return "", "", false, errors.New("missing '='")
	}
	value, err = parseDotenvValue(rest[1:])
	if err != nil {
		return "", "", false, err
	}
	return key, value, false, nil
}

// stripExport removes a leading "export" word. "exportKEY" is a name, not a prefix.
func stripExport(s string) (string, error) {
	if strings.HasPrefix(s, "export") {
		rest := s[len("export"):]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			rest = strings.TrimSpace(rest)
			if rest == "" || strings.HasPrefix(rest, "#") {
				return "", errors.New("missing key")
			}
			return rest, nil
		}
	}
	return s, nil
}

func parseDotenvValue(s string) (string, error) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i >= len(s) || (i > 0 && s[i] == '#') {
		return "", nil
	}
	if s[i] == '"' || s[i] == '\'' {
		val, rest, err := scanQuoted(s[i:])
		if err != nil {
			return "", err
		}
		rest = strings.TrimLeft(rest, " \t")
		if rest != "" && rest[0] != '#' {
			return "", errors.New("trailing characters")
		}
		return val, nil
	}
	return strings.TrimSpace(stripInlineComment(s)), nil
}

// stripInlineComment drops a trailing " #" comment from an unquoted value.
// A '#' that is not preceded by whitespace is part of the value.
func stripInlineComment(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && i > 0 && (s[i-1] == ' ' || s[i-1] == '\t') {
			return s[:i]
		}
	}
	return s
}

func scanQuoted(s string) (string, string, error) {
	quote := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if quote == '"' && c == '\\' {
			if i+1 >= len(s) {
				return "", "", errors.New("incomplete escape")
			}
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '\\', '"', '\'':
				b.WriteByte(s[i])
			default:
				// Keep both characters so an unknown escape cannot swallow a secret.
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
			continue
		}
		if c == quote {
			return b.String(), s[i+1:], nil
		}
		b.WriteByte(c)
	}
	return "", "", errors.New("unclosed quote")
}

func isEnvNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isEnvNameCont(c byte) bool {
	return isEnvNameStart(c) || (c >= '0' && c <= '9')
}
