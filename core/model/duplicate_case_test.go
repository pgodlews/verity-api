package model

import "testing"

func TestValidateRejectsDuplicateCaseNames(t *testing.T) {
	step := Step{Name: "s", Protocol: "rest", Request: map[string]any{"url": "http://127.0.0.1/"}}
	p := &Project{
		SchemaVersion: "1.0", Name: "p",
		Environments: []Environment{{Name: "e", Default: true}},
		Suites: []Suite{
			{Name: "a", Cases: []TestCase{{Name: "same", Steps: []Step{step}}, {Name: "same", Steps: []Step{step}}}},
			{Name: "b", Cases: []TestCase{{Name: "same", Steps: []Step{step}}}}, // other suite: fine
		},
	}
	errs := p.Validate()
	if len(errs) != 1 || errs[0].Path != "suites[0].cases[1].name" {
		t.Fatalf("errors = %v", errs)
	}
}
