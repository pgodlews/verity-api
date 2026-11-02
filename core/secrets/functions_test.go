package secrets

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/verr"
)

func TestInterpolateFunctions(t *testing.T) {
	fixed := time.Unix(1700000000, 123456789)
	uuidBytes := make([]byte, 16)
	for i := range uuidBytes {
		uuidBytes[i] = byte(i)
	}
	tests := []struct {
		name  string
		input string
		rand  []byte
		want  string
	}{
		{name: "uuid", input: "${fn:uuid}", rand: uuidBytes, want: "00010203-0405-4607-8809-0a0b0c0d0e0f"},
		{name: "timestamp", input: "${fn:timestamp}", want: "1700000000"},
		{name: "timestamp milliseconds", input: "${fn:timestampMs}", want: "1700000000123"},
		{name: "ISO timestamp", input: "${fn:isoTimestamp}", want: "2023-11-14T22:13:20.123Z"},
		{name: "random int default range", input: "${fn:randomInt}", rand: []byte{0, 42}, want: "42"},
		{name: "random int custom range", input: "${fn:randomInt:10:52}", rand: []byte{42}, want: "52"},
		{name: "random hex", input: "${fn:randomHex:6}", rand: []byte{0x00, 0xab, 0xff}, want: "00abff"},
		{name: "random hex odd length", input: "${fn:randomHex:5}", rand: []byte{0x00, 0xab, 0xff}, want: "00abf"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewResolver(nil)
			r.Now = func() time.Time { return fixed }
			r.Rand = bytes.NewReader(tc.rand)
			got, err := r.Interpolate(tc.input, false)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInterpolateFunctionOccurrencesAreIndependent(t *testing.T) {
	random := make([]byte, 32)
	for i := range random {
		random[i] = byte(i)
	}
	r := NewResolver(nil)
	r.Rand = bytes.NewReader(random)
	got, err := r.Interpolate("${fn:uuid} ${fn:uuid}", false)
	if err != nil {
		t.Fatal(err)
	}
	want := "00010203-0405-4607-8809-0a0b0c0d0e0f 10111213-1415-4617-9819-1a1b1c1d1e1f"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	calls := 0
	r.Now = func() time.Time {
		calls++
		return time.Unix(1700000000+int64(calls-1), 0)
	}
	got, err = r.Interpolate("${fn:timestamp} ${fn:timestamp}", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1700000000 1700000001" {
		t.Fatalf("timestamps were not evaluated independently: %q", got)
	}
}

func TestInterpolateFunctionNestedInVariable(t *testing.T) {
	random := make([]byte, 16)
	for i := range random {
		random[i] = byte(i)
	}
	r := NewResolver(map[string]string{"TRACE": "id=${fn:uuid}"})
	r.Rand = bytes.NewReader(random)
	got, err := r.Interpolate("${TRACE}", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "id=00010203-0405-4607-8809-0a0b0c0d0e0f" {
		t.Fatalf("got %q", got)
	}
	if values := r.SecretValues(); len(values) != 0 {
		t.Fatalf("function values must not be registered as secrets: %q", values)
	}
}

func TestInterpolateFunctionErrors(t *testing.T) {
	tests := []struct {
		input   string
		message string
	}{
		{input: "${fn:nope}", message: "nope"},
		{input: "${fn:uuid:1}", message: "uuid"},
		{input: "${fn:timestamp:1}", message: "timestamp"},
		{input: "${fn:timestampMs:1}", message: "timestampMs"},
		{input: "${fn:isoTimestamp:1}", message: "isoTimestamp"},
		{input: "${fn:randomInt:1}", message: "MIN:MAX"},
		{input: "${fn:randomInt:1:2:3}", message: "MIN:MAX"},
		{input: "${fn:randomInt:nope:2}", message: "MIN"},
		{input: "${fn:randomInt:1:nope}", message: "MAX"},
		{input: "${fn:randomInt:2:1}", message: "exceeds"},
		{input: "${fn:randomHex}", message: "N argument"},
		{input: "${fn:randomHex:}", message: "positive integer"},
		{input: "${fn:randomHex:nope}", message: "positive integer"},
		{input: "${fn:randomHex:0}", message: "positive integer"},
		{input: "${fn:randomHex:-1}", message: "positive integer"},
		{input: "${fn:randomHex:1:2}", message: "N argument"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			r := NewResolver(nil)
			_, err := r.Interpolate(tc.input, false)
			if err == nil {
				t.Fatal("expected an error even in non-strict mode")
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error %q does not contain %q", err, tc.message)
			}
			if v, ok := verr.As(err); !ok || v.Code != verr.CodeConfig {
				t.Fatalf("expected config_error, got %v", err)
			}
		})
	}
}
