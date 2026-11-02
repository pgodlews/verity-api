package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/pgodlews/verity-api/core/secrets"
)

type cliKeychain struct {
	items map[string]string
	err   error
	calls int
}

func (f *cliKeychain) Get(service, name string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if value, ok := f.items[service+":"+name]; ok {
		return value, nil
	}
	return "", secrets.ErrKeychainNotFound
}

func (f *cliKeychain) Set(service, name, value string) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.items[service+":"+name] = value
	return nil
}

func (f *cliKeychain) Delete(service, name string) error {
	if _, err := f.Get(service, name); err != nil {
		return err
	}
	delete(f.items, service+":"+name)
	return nil
}

func TestSecretCommandLifecycle(t *testing.T) {
	fake := &cliKeychain{items: map[string]string{}}
	store := &secrets.Keychain{Backend: fake}
	const value = "test-sensitive-value"
	for _, tc := range []struct {
		args   []string
		input  string
		code   int
		reveal bool
	}{
		{[]string{"set", "project/TOKEN", "--json"}, value + "\r\n", exitOK, false},
		{[]string{"get", "project/TOKEN", "--json"}, "", exitOK, false},
		{[]string{"get", "project/TOKEN", "--reveal", "--json"}, "", exitOK, true},
		{[]string{"get", "project/TOKEN"}, "", exitOK, false},
		{[]string{"get", "project/TOKEN", "--reveal"}, "", exitOK, true},
		{[]string{"list", "--json"}, "", exitConfig, false},
		{[]string{"rm", "project/TOKEN", "--json"}, "", exitOK, false},
		{[]string{"get", "project/TOKEN", "--json"}, "", exitConfig, false},
		{[]string{"rm", "project/TOKEN"}, "", exitConfig, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, stderr bytes.Buffer
			code := secretCommand(tc.args, strings.NewReader(tc.input), &out, &stderr, store)
			if code != tc.code {
				t.Fatalf("exit %d, want %d; %s", code, tc.code, stderr.String())
			}
			if strings.Contains(out.String(), value) != tc.reveal || strings.Contains(stderr.String(), value) {
				t.Fatal("secret was leaked or explicit reveal failed")
			}
			if tc.reveal && !strings.Contains(stderr.String(), "warning:") {
				t.Fatal("reveal did not warn")
			}
			if strings.Contains(strings.Join(tc.args, " "), "--json") {
				var result map[string]any
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if tc.code == exitConfig {
					problem, ok := result["error"].(map[string]any)
					if !ok || problem["code"] != "config_error" {
						t.Fatal("expected structured config error")
					}
				}
			}
		})
	}
}

func TestSecretCommandRejectsUsageAndBadInput(t *testing.T) {
	fake := &cliKeychain{items: map[string]string{}}
	for _, args := range [][]string{nil, {"set"}, {"set", "TOKEN", "value"}, {"list", "TOKEN"}, {"rm", "TOKEN", "--reveal"}, {"get", "TOKEN", "--force"}, {"unknown"}} {
		if code := secretCommand(args, strings.NewReader("value"), io.Discard, io.Discard, &secrets.Keychain{Backend: fake}); code != exitUsage {
			t.Errorf("%v: exit %d, want usage error", args, code)
		}
	}
	for _, tc := range []struct {
		name, input string
	}{
		{"TOKEN", ""}, {"TOKEN", "\n"}, {"TOKEN", " \t\r\n"}, {"TOKEN", strings.Repeat("x", (1<<20)+1)}, {"bad:name", "value"},
	} {
		if code := secretCommand([]string{"set", tc.name}, strings.NewReader(tc.input), io.Discard, io.Discard, &secrets.Keychain{Backend: fake}); code != exitConfig {
			t.Errorf("bad input for %q: exit %d", tc.name, code)
		}
	}
	if fake.calls != 0 {
		t.Fatal("invalid input accessed the keychain")
	}
}

func TestSecretCommandUnavailableStore(t *testing.T) {
	store := &secrets.Keychain{Backend: &cliKeychain{err: errors.New("backend echoed test-sensitive-value")}}
	for _, action := range []string{"set", "get", "rm"} {
		var out, stderr bytes.Buffer
		if code := secretCommand([]string{action, "TOKEN", "--json"}, strings.NewReader("test-sensitive-value"), &out, &stderr, store); code != exitConfig {
			t.Fatalf("%s: exit %d", action, code)
		}
		if !strings.Contains(out.String(), "TOKEN") || !strings.Contains(out.String(), "unavailable or locked") || strings.Contains(out.String()+stderr.String(), "test-sensitive-value") {
			t.Fatal("missing or unsafe error diagnostic")
		}
	}
}

func TestSecretSetTrimsValueWhitespace(t *testing.T) {
	for _, tc := range []struct {
		input, want string
	}{
		{" value \n", "value"},
		{"value\r\n", "value"},
		{"value\r", "value"},
		{"line1\nline2\n\n", "line1\nline2"},
	} {
		fake := &cliKeychain{items: map[string]string{}}
		if code := secretCommand([]string{"set", "TOKEN"}, strings.NewReader(tc.input), io.Discard, io.Discard, &secrets.Keychain{Backend: fake}); code != exitOK {
			t.Fatalf("set: exit %d", code)
		}
		if fake.items["verity:TOKEN"] != tc.want {
			t.Fatal("stdin whitespace was changed unexpectedly")
		}
	}
}

type failedSecretInput struct{}

func (failedSecretInput) Read([]byte) (int, error) {
	return 0, errors.New("input error containing test-sensitive-value")
}

func TestSecretSetReadErrorDoesNotLeak(t *testing.T) {
	var out, stderr bytes.Buffer
	fake := &cliKeychain{items: map[string]string{}}
	if code := secretCommand([]string{"set", "TOKEN", "--json"}, failedSecretInput{}, &out, &stderr, &secrets.Keychain{Backend: fake}); code != exitConfig {
		t.Fatalf("set: exit %d", code)
	}
	if fake.calls != 0 || strings.Contains(out.String()+stderr.String(), "test-sensitive-value") {
		t.Fatal("stdin error accessed the store or leaked input")
	}
}

// Exercise dispatch and the browser dev bridge against go-keyring's mock.
func TestSecretDispatchAndDevBridge(t *testing.T) {
	keyring.MockInit()
	for _, tc := range []struct {
		args  []string
		input string
		ok    bool
	}{
		{[]string{"secret", "set", "DISPATCH_TOKEN", "--json"}, "test-sensitive-value", true},
		{[]string{"secret", "get", "DISPATCH_TOKEN", "--json"}, "", true},
		{[]string{"secret", "rm", "DISPATCH_TOKEN", "--json"}, "", true},
		{[]string{"gui-call", "StoreKeychainSecret"}, `["project/BRIDGE_TOKEN","test-sensitive-value"]`, true},
		{[]string{"gui-call", "StoreKeychainSecret"}, `["bad:name","test-sensitive-value"]`, false},
		{[]string{"gui-call", "KeychainSecretExists"}, `["project/BRIDGE_TOKEN"]`, true},
		{[]string{"gui-call", "KeychainSecretExists"}, `["MISSING_BRIDGE_TOKEN"]`, true},
		{[]string{"gui-call", "KeychainSecretExists"}, `["bad:name"]`, false},
	} {
		code, output := secretCLIOutput(t, tc.args, tc.input)
		// gui-call reports operation errors in its envelope and always exits 0.
		if code != exitOK {
			t.Fatalf("%v: exit %d, want 0", tc.args, code)
		}
		if strings.Contains(output, "test-sensitive-value") {
			t.Fatal("CLI/bridge output leaked the value")
		}
		var result struct {
			OK    bool            `json:"ok"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil || result.OK != tc.ok {
			t.Fatal("CLI/bridge returned an unexpected envelope")
		}
		if len(tc.args) > 1 && tc.args[1] == "KeychainSecretExists" && tc.ok {
			var exists bool
			if err := json.Unmarshal(result.Value, &exists); err != nil || exists != strings.Contains(tc.input, "project/BRIDGE_TOKEN") {
				t.Fatal("existence binding did not return the expected boolean")
			}
		}
	}
	if value, err := keyring.Get("verity", "project/BRIDGE_TOKEN"); err != nil || value != "test-sensitive-value" {
		t.Fatal("browser bridge did not store the mock credential")
	}
}

func secretCLIOutput(t *testing.T, args []string, input string) (int, string) {
	t.Helper()
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inRead.Close()
	defer inWrite.Close()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outRead.Close()
	defer outWrite.Close()
	if _, err := io.WriteString(inWrite, input); err != nil {
		t.Fatal(err)
	}
	inWrite.Close()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inRead, outWrite
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	code := run(args)
	outWrite.Close()
	output, err := io.ReadAll(outRead)
	if err != nil {
		t.Fatal(err)
	}
	return code, string(output)
}

func TestSecretSetRequiresForceForExistingItem(t *testing.T) {
	fake := &cliKeychain{items: map[string]string{"verity:TOKEN": "original-value"}}
	store := &secrets.Keychain{Backend: fake}
	for _, jsonFlag := range []bool{false, true} {
		args := []string{"set", "TOKEN"}
		if jsonFlag {
			args = append(args, "--json")
		}
		var out, stderr bytes.Buffer
		if code := secretCommand(args, strings.NewReader("new-value"), &out, &stderr, store); code != exitConfig {
			t.Fatalf("unconfirmed overwrite exit %d", code)
		}
		if !strings.Contains(out.String()+stderr.String(), "--force") || fake.items["verity:TOKEN"] != "original-value" {
			t.Fatal("existing item replaced without --force")
		}
	}
	if code := secretCommand([]string{"set", "TOKEN", "--force", "--json"}, strings.NewReader(" replacement \r\n"), io.Discard, io.Discard, store); code != exitOK || fake.items["verity:TOKEN"] != "replacement" {
		t.Fatal("forced replacement failed")
	}
}
