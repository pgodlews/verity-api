package runner

import (
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/secrets"
)

func TestCaptureBodyIsCappedAndRedactedAcrossTheCut(t *testing.T) {
	old := captureMaxBody
	captureMaxBody = 100
	t.Cleanup(func() { captureMaxBody = old })
	const secret = "straddling-secret-value-12345"
	red := secrets.NewRedactor(secret)

	// The secret starts before the cut and ends after it.
	body := strings.Repeat("a", 90) + secret + strings.Repeat("b", 500)
	got, truncated := captureBody(body, red)
	if !truncated || !strings.Contains(got, "capture truncated") {
		t.Fatalf("not truncated: %q", got)
	}
	if strings.Contains(got, "straddling") || strings.Contains(got, "bbbb") {
		t.Fatalf("partial secret or tail kept: %q", got)
	}
	if small, cut := captureBody("short "+secret, red); cut || strings.Contains(small, secret) {
		t.Fatalf("small body = %q, %v", small, cut)
	}
}
