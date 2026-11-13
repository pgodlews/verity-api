package model

import (
	"testing"
)

func TestValidateAssertionOpAndTransferFrom(t *testing.T) {
	p := &Project{
		SchemaVersion: "1.0", Name: "p",
		Environments: []Environment{{Name: "e", Default: true}},
		Suites: []Suite{{Name: "s", Cases: []TestCase{{Name: "c", Steps: []Step{{
			Name: "st", Protocol: "rest", Request: map[string]any{"url": "http://127.0.0.1/"},
			Assertions: []Assertion{{Type: "status", Op: "equals", Value: 200}, {Type: "contains", Op: "not_contains", Value: "x"}},
			Transfers:  []TransferSpec{{Name: "v", From: "jsonPath", Expr: "$.a"}},
		}}}}}},
	}
	paths := map[string]bool{}
	for _, e := range p.Validate() {
		paths[e.Path] = true
	}
	for _, want := range []string{"suites[0].cases[0].steps[0].assertions[0].op", "suites[0].cases[0].steps[0].transfers[0].from"} {
		if !paths[want] {
			t.Errorf("no validation error at %s (got %v)", want, paths)
		}
	}
	if paths["suites[0].cases[0].steps[0].assertions[1].op"] {
		t.Error("not_contains rejected")
	}
}
