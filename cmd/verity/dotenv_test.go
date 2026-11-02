package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

// secretSentinel is embedded in malformed lines. Errors must not echo it.
const secretSentinel = "do-not-print-this-secret"

func TestParseDotenv(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     map[string]string
		wantErr  string
		wantLine int
	}{
		{
			name:  "comments blanks and plain assignment",
			input: "# comment\n\n  \nKEY=value\n",
			want:  map[string]string{"KEY": "value"},
		},
		{
			name:  "export prefix and spaces around equals",
			input: "export KEY = value\nexport\tOTHER=x\n",
			want:  map[string]string{"KEY": "value", "OTHER": "x"},
		},
		{
			name:  "export word is not stripped from a name",
			input: "exportKEY=value\n",
			want:  map[string]string{"exportKEY": "value"},
		},
		{
			name:  "double quotes escapes and interior hash",
			input: "MSG=\"hello\\nworld\"\nPATH=\"C:\\\\foo\"\nRAW=\"C:\\foo\"\nHASH=\"hash # inside\"\nTAB=\"a\\tb\"\n",
			want: map[string]string{
				"MSG":  "hello\nworld",
				"PATH": "C:\\foo",
				"RAW":  "C:\\foo",
				"HASH": "hash # inside",
				"TAB":  "a\tb",
			},
		},
		{
			name:  "double quote escaped quote and carriage return",
			input: "MSG=\"say \\\"hi\\\"\\r\"\n",
			want:  map[string]string{"MSG": "say \"hi\"\r"},
		},
		{
			name:  "single quotes are literal",
			input: "MSG='hello\\nworld' # trailing\nHASH='hash # inside'\n",
			want:  map[string]string{"MSG": "hello\\nworld", "HASH": "hash # inside"},
		},
		{
			name:  "unquoted trailing comment needs whitespace",
			input: "A=hello world # comment\nB=hello#not-comment\nC=#fff\nD= # just a comment\nE=\nF=\"\"\nG=''\n",
			want: map[string]string{
				"A": "hello world",
				"B": "hello#not-comment",
				"C": "#fff",
				"D": "",
				"E": "",
				"F": "",
				"G": "",
			},
		},
		{
			name:  "quoted value with surrounding spaces and trailing comment",
			input: "KEY = \" spaced \" # comment\n",
			want:  map[string]string{"KEY": " spaced "},
		},
		{
			name:  "later duplicate wins and bom is ignored",
			input: "\uFEFFA=1\n# comment\nA=2\nB=x\n",
			want:  map[string]string{"A": "2", "B": "x"},
		},
		{
			name:  "crlf",
			input: "KEY=value\r\n# c\r\n",
			want:  map[string]string{"KEY": "value"},
		},
		{
			name:     "unclosed double quote does not echo the value",
			input:    "OK=1\n\nTOKEN=\"" + secretSentinel + "\n",
			wantErr:  "unclosed quote",
			wantLine: 3,
		},
		{
			name:     "unclosed single quote",
			input:    "TOKEN='" + secretSentinel,
			wantErr:  "unclosed quote",
			wantLine: 1,
		},
		{
			name:     "missing equals",
			input:    "# c\nJUSTKEY\n",
			wantErr:  "missing '='",
			wantLine: 2,
		},
		{
			name:     "invalid name",
			input:    "1BAD=" + secretSentinel + "\n",
			wantErr:  "invalid name",
			wantLine: 1,
		},
		{
			name:     "hyphenated name",
			input:    "HAS-DASH=value\n",
			wantErr:  "invalid name",
			wantLine: 1,
		},
		{
			name:     "empty name",
			input:    "=" + secretSentinel + "\n",
			wantErr:  "invalid name",
			wantLine: 1,
		},
		{
			name:     "export without a key",
			input:    "export # " + secretSentinel + "\n",
			wantErr:  "missing key",
			wantLine: 1,
		},
		{
			name:     "trailing junk after a quote",
			input:    "KEY=\"v\" " + secretSentinel + "\n",
			wantErr:  "trailing characters",
			wantLine: 1,
		},
		{
			name:     "incomplete escape",
			input:    "KEY=\"" + secretSentinel + "\\\n",
			wantErr:  "incomplete escape",
			wantLine: 1,
		},
		{
			name:     "single quote cannot contain a quote",
			input:    "KEY='it\\'s'\n",
			wantErr:  "trailing characters",
			wantLine: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDotenv("secrets.env", strings.NewReader(tt.input))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				msg := err.Error()
				if !strings.Contains(msg, tt.wantErr) {
					t.Fatalf("error %q does not contain %q", msg, tt.wantErr)
				}
				loc := fmt.Sprintf("secrets.env:%d", tt.wantLine)
				if !strings.Contains(msg, loc) {
					t.Fatalf("error %q missing %s", msg, loc)
				}
				if strings.Contains(tt.input, secretSentinel) && strings.Contains(msg, secretSentinel) {
					t.Fatalf("error leaked a value: %s", msg)
				}
				ve, ok := verr.As(err)
				if !ok || ve.Code != verr.CodeConfig {
					t.Fatalf("expected config_error, got %v", err)
				}
				if ve.Path != loc {
					t.Fatalf("path %q, want %q", ve.Path, loc)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v want %#v", got, tt.want)
			}
		})
	}
}

func TestEnvFilePrecedence(t *testing.T) {
	const (
		proc  = "VERITY_TEST_ENVFILE_PROC"
		empty = "VERITY_TEST_ENVFILE_EMPTY"
		only1 = "VERITY_TEST_ENVFILE_ONLY1"
		both  = "VERITY_TEST_ENVFILE_BOTH"
		only2 = "VERITY_TEST_ENVFILE_ONLY2"
	)
	unset := []string{proc, empty, only1, both, only2}
	for _, k := range unset {
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for _, k := range unset {
			os.Unsetenv(k)
		}
	})

	dir := t.TempDir()
	file1 := filepath.Join(dir, "first.env")
	file2 := filepath.Join(dir, "second.env")
	writeEnv := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeEnv(file1, proc+"=from-file-1\n"+empty+"=from-file-1\n"+only1+"=from-file-1\n"+both+"=from-file-1\n")
	writeEnv(file2, proc+"=from-file-2\n"+both+"=from-file-2\n"+only2+"=\"quoted\\nvalue\"\n")

	t.Setenv(proc, "from-process")
	t.Setenv(empty, "")

	if err := loadEnvFiles([]string{file1, file2}, false); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(proc); got != "from-process" {
		t.Fatalf("process env should win, got %q", got)
	}
	if got, ok := os.LookupEnv(empty); !ok || got != "" {
		t.Fatalf("empty process env should win, got %q ok=%v", got, ok)
	}
	if got := os.Getenv(only1); got != "from-file-1" {
		t.Fatalf("key only in the first file: got %q", got)
	}
	if got := os.Getenv(both); got != "from-file-2" {
		t.Fatalf("later file should win, got %q", got)
	}
	if got := os.Getenv(only2); got != "quoted\nvalue" {
		t.Fatalf("quoted value from the later file: got %q", got)
	}

	if err := loadEnvFiles([]string{file1, file2}, true); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(proc); got != "from-file-2" {
		t.Fatalf("override should replace the process env, got %q", got)
	}
	if got := os.Getenv(empty); got != "from-file-1" {
		t.Fatalf("override should replace an empty process env, got %q", got)
	}
	if got := os.Getenv(both); got != "from-file-2" {
		t.Fatalf("later file should still win under override, got %q", got)
	}
}

func TestEnvFileMalformedDoesNotApply(t *testing.T) {
	const key = "VERITY_TEST_ENVFILE_PARTIAL"
	os.Unsetenv(key)
	t.Cleanup(func() { os.Unsetenv(key) })

	dir := t.TempDir()
	good := filepath.Join(dir, "good.env")
	bad := filepath.Join(dir, "bad.env")
	if err := os.WriteFile(good, []byte(key+"=applied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("TOKEN=\""+secretSentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := loadEnvFiles([]string{good, bad}, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secretSentinel) {
		t.Fatalf("error leaked a value: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "bad.env:1") {
		t.Fatalf("error %q missing file:line", err.Error())
	}
	if _, ok := os.LookupEnv(key); ok {
		t.Fatal("a malformed later file must not apply earlier files")
	}
}

func TestEnvFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.env")
	err := loadEnvFiles([]string{path}, false)
	ve, ok := verr.As(err)
	if !ok || ve.Code != verr.CodeConfig {
		t.Fatalf("expected config_error, got %v", err)
	}
	if !strings.Contains(err.Error(), "missing.env") {
		t.Fatalf("error %q missing path", err.Error())
	}
}
