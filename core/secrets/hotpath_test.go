package secrets

import (
	"strings"
	"testing"
)

func TestRedactHotPathAllocations(t *testing.T) {
	r := NewRedactor("long-secret-value", "ab1")
	text := "nothing sensitive in this line at all"
	if got := r.Redact("x ab1 y long-secret-value"); strings.Contains(got, "ab1") || strings.Contains(got, "long-secret-value") {
		t.Fatalf("not masked: %q", got)
	}
	if raceEnabled {
		t.Skip("allocation counts differ under -race")
	}
	allocs := testing.AllocsPerRun(200, func() { _ = r.Redact(text) })
	if allocs > 3 { // 4 before E-3 (a fresh slice of short-secret patterns per call)
		t.Fatalf("Redact of clean text allocates %.0f times per call", allocs)
	}
	digestText := strings.Repeat("a digest of the release notes ", 200)
	allocs = testing.AllocsPerRun(50, func() { _ = maskDigestResponses(digestText) })
	if allocs > 1 { // before E-3 an index slice of len(text)+1 was allocated first
		t.Fatalf("maskDigestResponses allocates %.0f times for text without a Digest header", allocs)
	}
	if got := RedactGitOutput("fatal: password=hunter2"); strings.Contains(got, "hunter2") {
		t.Fatalf("git output not redacted: %q", got)
	}
}
