package verr

import (
	"errors"
	"io/fs"
	"testing"
)

func TestContextKeepsInnerClassification(t *testing.T) {
	inner := New(CodeValidation, "unknown field").WithPath("suites[0]")
	got := Context(CodeConfig, inner, "include \"a.yaml\"")
	if got.Code != CodeValidation || got.Path != "suites[0]" || got.Message != "include \"a.yaml\": unknown field" {
		t.Fatalf("context over verr = %+v", got)
	}
	if !errors.Is(got, inner) {
		t.Fatal("inner error not reachable")
	}
	plain := Context(CodeConfig, fs.ErrNotExist, "read \"p.yaml\"")
	if plain.Code != CodeConfig || plain.Message != "read \"p.yaml\": file does not exist" || !errors.Is(plain, fs.ErrNotExist) {
		t.Fatalf("context over plain error = %+v", plain)
	}
}
