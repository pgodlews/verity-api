package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

func TestRedactURLCredentials(t *testing.T) {
	in := "fatal: unable to access 'https://user:s3cret@h/r.git/': refused"
	out := RedactURLCredentials(in)
	if strings.Contains(out, "s3cret") || strings.Contains(out, "user:s3cret") {
		t.Fatalf("userinfo leaked: %q", out)
	}
	if !strings.Contains(out, Mask+"@h/r.git") {
		t.Fatalf("host was not kept: %q", out)
	}
	plain := "already up to date"
	if RedactURLCredentials(plain) != plain {
		t.Fatalf("plain text changed: %q", RedactURLCredentials(plain))
	}
}

func TestRedactGitOutput(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		secret string
		keep   string
	}{
		{"userinfo", "fatal: unable to access 'https://user:s3cret@h/r.git/'", "s3cret", "h/r.git"},
		{"query token", "fatal: 'https://h/r.git?token=querysecret&x=1'", "querysecret", "x=1"},
		{"access_token", "error: https://h/r.git?access_token=querysecret", "querysecret", "h/r.git"},
		{"extraHeader", "http.extraheader=Authorization: Bearer headersecret", "headersecret", "extraheader"},
		{"authorization", "Authorization: Basic aGVhZGVyc2VjcmV0", "aGVhZGVyc2VjcmV0", "Authorization"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RedactGitOutput(tc.in)
			if strings.Contains(out, tc.secret) {
				t.Fatalf("leaked %s: %q", tc.secret, out)
			}
			if !strings.Contains(out, Mask) {
				t.Fatalf("mask missing: %q", out)
			}
			if tc.keep != "" && !strings.Contains(out, tc.keep) {
				t.Fatalf("lost %q in %q", tc.keep, out)
			}
		})
	}
	plain := "Counting objects: 100% (1/1), done."
	if RedactGitOutput(plain) != plain {
		t.Fatalf("progress text changed: %q", RedactGitOutput(plain))
	}
}

func TestRedactKnownLiteral(t *testing.T) {
	r := NewRedactor("super-secret-token-value")
	out := r.Redact("token=super-secret-token-value end")
	if contains(out, "super-secret-token-value") {
		t.Fatalf("literal secret leaked: %q", out)
	}
	if !contains(out, Mask) {
		t.Fatalf("expected mask in output, got %q", out)
	}
}

func TestRedactAuthorizationHeader(t *testing.T) {
	r := NewRedactor()
	cases := []string{
		"Authorization: Bearer abc.def.ghi",
		`"authorization": "Bearer abc.def.ghi"`,
		"Authorization: Basic dXNlcjpwYXNz",
	}
	for _, c := range cases {
		out := r.Redact(c)
		if contains(out, "abc.def.ghi") || contains(out, "dXNlcjpwYXNz") {
			t.Fatalf("authorization value leaked: %q", out)
		}
	}
}

func TestRedactDigestResponse(t *testing.T) {
	const response = "0123456789abcdef0123456789abcdef"
	for _, in := range []string{
		`Digest username="user", response="` + response + `", algorithm=MD5`,
		`Authorization: Digest username="user", response="` + response + `"`,
		`{"authorization":"Digest username=\"user\", response=\"` + response + `\"}`,
		`Digest username="u", realm="response=\"0\"", response="` + response + `"`,
	} {
		if out := NewRedactor().Redact(in); strings.Contains(out, response) || !strings.Contains(out, Mask) {
			t.Fatalf("digest response was not masked: %q", out)
		}
	}
	for _, in := range []string{
		`digest file response="abcdef"`,
		`digest file response="` + response + `"`,
		`Digest realm="response=\"0\"", nonce="n"`,
	} {
		if got := NewRedactor().Redact(in); got != in {
			t.Fatalf("unrelated text was over-masked:\n got %q\nwant %q", got, in)
		}
	}
}

func TestRedactJSONSecretFields(t *testing.T) {
	r := NewRedactor()
	in := `{"password":"hunter2pass","client_secret":"sek-1234567890","ok":"visible"}`
	out := r.Redact(in)
	if contains(out, "hunter2pass") || contains(out, "sek-1234567890") {
		t.Fatalf("secret field leaked: %q", out)
	}
	if !contains(out, "visible") {
		t.Fatalf("non-secret value wrongly removed: %q", out)
	}
}

func TestRedactPEMPrivateKey(t *testing.T) {
	r := NewRedactor()
	in := "-----BEGIN PRIVATE KEY-----\nMIIBVQIBADAN\n-----END PRIVATE KEY-----"
	out := r.Redact(in)
	if contains(out, "MIIBVQIBADAN") {
		t.Fatalf("private key leaked: %q", out)
	}
}

func TestRedactShortSecretsStandaloneOnly(t *testing.T) {
	r := NewRedactor("ab", "x7")
	in := `value=ab name="ab" tab=cab abc x7,x7 x70`
	out := r.Redact(in)
	want := `value=` + Mask + ` name="` + Mask + `" tab=cab abc ` + Mask + `,` + Mask + ` x70`
	if out != want {
		t.Fatalf("short secrets:\n got %q\nwant %q", out, want)
	}
}

func TestRedactorLiteralOrdering(t *testing.T) {
	r := NewRedactor("dddd", "bbbbbb", "cccc", "aaaaaa")
	want := []string{"aaaaaa", "bbbbbb", "cccc", "dddd"}
	if !slices.Equal(r.ordered, want) {
		t.Fatalf("literal order = %q, want %q", r.ordered, want)
	}
}

func TestRedactorDeterministicLongestPrefix(t *testing.T) {
	tests := []struct {
		name     string
		literals []string
	}{
		{name: "shortest registered first", literals: []string{"abc", "abcd", "abcdef"}},
		{name: "longest registered first", literals: []string{"abcdef", "abcd", "abc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRedactor(tt.literals...)
			want := Mask + " " + Mask + " " + Mask
			for i := 0; i < 20; i++ {
				if got := r.Redact("abcdef abcd abc"); got != want {
					t.Fatalf("Redact() = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestRedactorConcurrentAddAndRedact(t *testing.T) {
	r := NewRedactor("abc", "abcd", "abcdef")
	want := Mask + " " + Mask + " " + Mask
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if got := r.Redact("abcdef abcd abc"); got != want {
					t.Errorf("Redact() = %q, want %q", got, want)
					return
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				r.Add(fmt.Sprintf("concurrent-%d-%d", worker, j))
			}
		}(i)
	}
	wg.Wait()
}

func TestLooksLikeSecret(t *testing.T) {
	tests := map[string]bool{
		"${env:TOKEN}": false,
		"short":        false,
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc": true,
		"AKIAIOSFODNN7EXAMPLEKEY123456":                                        true,
		"hello world":                                                          false,
	}
	for in, want := range tests {
		if got := LooksLikeSecret(in); got != want {
			t.Errorf("LooksLikeSecret(%q) = %v, want %v", in, got, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestRedactOverlappingSecretsLeaveNoFragment(t *testing.T) {
	// Both at least four characters, so both mask as plain substrings.
	r := NewRedactor("zzab", "abcdef")
	for _, in := range []string{"zzabcdef", "abcdefzzab", "xx zzabcdef yy"} {
		out := r.Redact(in)
		for _, frag := range []string{"cdef", "zzab", "abc"} {
			if strings.Contains(out, frag) {
				t.Errorf("Redact(%q) = %q leaks %q", in, out, frag)
			}
		}
	}
	if got := r.Redact("xx zzabcdef yy"); got != "xx "+Mask+" yy" {
		t.Errorf("overlapping secrets should merge into one mask, got %q", got)
	}
	// Overlapping occurrences of one secret.
	if got := NewRedactor("aaaa").Redact("aaaaaa-b"); got != Mask+"-b" {
		t.Errorf("got %q", got)
	}
}

func TestRedactStructuralKeywordsCaseInsensitive(t *testing.T) {
	r := NewRedactor()
	for _, in := range []string{
		`AUTHORIZATION: Bearer abc.def`,
		`{"Password": "hunter2"}`,
		`?API_KEY=xyz`,
		`{"paſsword": "hunter2"}`,
	} {
		if out := r.Redact(in); !strings.Contains(out, Mask) {
			t.Errorf("Redact(%q) = %q, want a mask", in, out)
		}
	}
}

func TestJSONSecretLeaves(t *testing.T) {
	got := JSONSecretLeaves([]byte(`{"token":{"value":"abc12345","n":3},"password":"plain","items":[{"secret":["s1-xxxx"]}],"name":"ada"}`))
	slices.Sort(got)
	// "plain" is a string directly under a key; the structural patterns
	// already mask it, so it is returned too and masking is harmless.
	want := []string{"abc12345", "plain", "s1-xxxx"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if JSONSecretLeaves([]byte("not json")) != nil || JSONSecretLeaves([]byte(`{"a":`)) != nil {
		t.Fatal("non-JSON must return nil")
	}
}

func TestRedactErrorPreservesSafeStructure(t *testing.T) {
	const value = "sensitive-error-value"
	red := NewRedactor(value)
	original := verr.Wrap(verr.CodeConfig, errors.New("cause "+value), "message "+value).
		WithPath("path/"+value).
		WithDetail("hosts", []string{value}).
		WithDetail("nested", map[string]string{"message": value})
	safe := red.RedactError(original)
	ve, ok := verr.As(safe)
	if !ok || ve.Code != verr.CodeConfig || ve.Unwrap() != nil {
		t.Fatal("unsafe or unstructured redacted error")
	}
	data, err := json.Marshal(safe)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(safe.Error()+string(data), value) || !strings.Contains(ve.Message, "cause "+Mask) {
		t.Fatal("error message, path, cause or details leaked")
	}
	if !strings.Contains(original.Error(), value) {
		t.Fatal("redaction mutated original error")
	}
	if red.RedactError(nil) != nil {
		t.Fatal("nil error changed")
	}
	if got := red.RedactError(errors.New(value)); got.Error() != Mask {
		t.Fatal("plain error not masked")
	}
}
