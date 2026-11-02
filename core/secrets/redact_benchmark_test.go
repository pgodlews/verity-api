package secrets

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkRedactLargeBody(b *testing.B) {
	secrets := make([]string, 50)
	for i := range secrets {
		secrets[i] = fmt.Sprintf("secret-%02d-%012d", i, i)
	}
	r := NewRedactor(secrets...)
	chunk := strings.Join(secrets, " ") + " ordinary payload "
	body := strings.Repeat(chunk, (8<<20)/len(chunk)+1)[:8<<20]

	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Redact(body)
	}
}
