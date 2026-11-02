package runner

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

func TestCaptureRequestUsesBodySummary(t *testing.T) {
	const sentinel = "RAW-FILE-BYTES"
	const secret = "upload-token-value-123456"
	summary := "text: name=token value=" + secret + "\nfile: name=doc filename=a.txt size=14\n"
	got := captureRequest(&spi.Request{
		Method:      "POST",
		URL:         "/up",
		Headers:     http.Header{"Content-Type": {"multipart/form-data; boundary=b"}},
		Body:        []byte(sentinel + secret),
		BodySummary: &summary,
	}, secrets.NewRedactor(secret))
	if strings.Contains(got.Body, sentinel) || strings.Contains(got.Body, secret) {
		t.Fatal("capture included file bytes or the secret")
	}
	if !strings.Contains(got.Body, secrets.Mask) || !strings.Contains(got.Body, "filename=a.txt") || !strings.Contains(got.Body, "size=14") {
		t.Fatalf("summary %q", got.Body)
	}
}
