package sdk

import (
	"context"
	"testing"

	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/spi"
)

// redactingAssertion uses the context redactor like a built-in does.
type redactingAssertion struct{}

func (redactingAssertion) Type() string { return "redacting" }
func (redactingAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, _ spi.AssertionSpec) (*spi.AssertionResult, error) {
	return &spi.AssertionResult{Passed: true, Message: actx.Redactor.Redact("ok")}, nil
}

func TestSDKContextsHaveRedactor(t *testing.T) {
	resp := handleAssertion(redactingAssertion{}, plugin.Request{Params: map[string]any{"response": map[string]any{}, "spec": map[string]any{}}})
	if resp.Error != nil {
		t.Fatalf("assertion with redactor: %v", resp.Error)
	}
	_, cancel, pctx := protocolCallContext(nil)
	defer cancel()
	if pctx.Redactor == nil {
		t.Fatal("protocol context has a nil redactor")
	}
}
