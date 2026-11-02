package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

type requestKeychain struct{ value string }

func (f requestKeychain) Get(string, string) (string, error) { return f.value, nil }
func (f requestKeychain) Set(string, string, string) error   { return nil }
func (f requestKeychain) Delete(string, string) error        { return nil }

func TestRequestErrorsRedactResolvedSecrets(t *testing.T) {
	for _, source := range []string{"env", "keychain"} {
		for _, protocol := range []string{"soap", "rest"} {
			t.Run(source+"/"+protocol, func(t *testing.T) {
				const value = "bad sekret-value"
				t.Setenv("REQUEST_SECRET", value)
				ref := "${env:REQUEST_SECRET}"
				if source == "keychain" {
					secrets.RegisterExternalProvider("keychain", &secrets.Keychain{Backend: requestKeychain{value}})
					t.Cleanup(func() { secrets.RegisterExternalProvider("keychain", &secrets.Keychain{}) })
					ref = "${secret:keychain:project/REQUEST_SECRET}"
				}
				step := model.Step{Name: "request", Protocol: protocol, Request: map[string]any{"url": "http://127.0.0.1/x"}}
				if protocol == "soap" {
					step.Request["operation"] = ref
				} else {
					step.Request["url"] = "http://" + ref + "/x"
				}
				var events []spi.RunEvent
				result := retryRun(t, nil, oneStepProject(step), Options{OnEvent: func(ev spi.RunEvent) { events = append(events, ev) }})
				got := result.Suites[0].Cases[0].Steps[0]
				if got.Error == nil || got.Status != spi.StatusError || !strings.Contains(got.Error.Message, secrets.Mask) {
					t.Fatalf("missing masked step error: %+v", got.Error)
				}
				for _, report := range []any{result, events} {
					data, err := json.Marshal(report)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(data), value) {
						t.Fatal("resolved credential leaked into JSON or event")
					}
				}
			})
		}
	}
}

func TestTargetResolverRegistersSecretsOnError(t *testing.T) {
	const value = "target-secret-value"
	t.Setenv("TARGET_SECRET", value)
	step := model.Step{Name: "target", Protocol: "rest", Request: map[string]any{"url": "http://127.0.0.1"}}
	p := oneStepProject(step)
	x, err := New(bootstrap.Default()).Prepare(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer x.CloseIdleTransports()
	binding := x.Cases()[0]
	step = binding.Case.Steps[0]
	// A single variable ensures interpolation produces the credential before failing.
	step.Vars = map[string]string{"host": "${env:TARGET_SECRET}${fn:unknown}"}
	if _, err := x.InterpolateRequest(binding.Suite, binding.Case, &step, nil, "${host}"); err == nil {
		t.Fatal("expected interpolation error")
	}
	if x.Redactor().Redact(value) != secrets.Mask {
		t.Fatal("temporary step-var resolver lost secret on error")
	}
	x, err = New(bootstrap.Default()).Prepare(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer x.CloseIdleTransports()
	if _, err := x.InterpolateStep(nil, nil, nil, "${env:TARGET_SECRET}${fn:unknown}"); err == nil {
		t.Fatal("expected interpolation error")
	}
	if x.Redactor().Redact(value) != secrets.Mask {
		t.Fatal("temporary step resolver lost secret on error")
	}
	// Exercise the same build failure through the load-facing CaseExec path.
	binding = x.Cases()[0]
	binding.Case.Steps[0].Protocol = "soap"
	binding.Case.Steps[0].Request["operation"] = "${env:TARGET_SECRET} invalid"
	results, err := x.RunCase(context.Background(), CaseRequest{Suite: binding.Suite, Case: binding.Case, Resolver: x.NewResolver(binding.Suite, binding.Case, nil)})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(results)
	if strings.Contains(string(data), value) || !strings.Contains(string(data), secrets.Mask) {
		t.Fatal("CaseExec error was not redacted")
	}
}
