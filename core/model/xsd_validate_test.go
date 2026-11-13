package model

import "testing"

func TestValidateRejectsSchemaOnXSDAssertion(t *testing.T) {
	step := func(a Assertion) *Project {
		return &Project{
			SchemaVersion: "1.0", Name: "p",
			Environments: []Environment{{Name: "e", Default: true}},
			Suites: []Suite{{Name: "s", Cases: []TestCase{{Name: "c", Steps: []Step{{
				Name: "st", Protocol: "soap", Request: map[string]any{"url": "http://127.0.0.1/"},
				Assertions: []Assertion{a},
			}}}}}},
		}
	}
	for _, a := range []Assertion{
		{Type: "xsd", Expr: "schema.xsd"},
		{Type: "xsd", Config: map[string]any{"schemaFile": "schema.xsd"}},
	} {
		errs := step(a).Validate()
		if len(errs) != 1 || errs[0].Path != "suites[0].cases[0].steps[0].assertions[0]" {
			t.Errorf("%+v: errors = %v", a, errs)
		}
	}
	if errs := step(Assertion{Type: "xsd", Config: map[string]any{"rootElement": "Envelope"}}).Validate(); len(errs) != 0 {
		t.Fatalf("rootElement rejected: %v", errs)
	}
}
