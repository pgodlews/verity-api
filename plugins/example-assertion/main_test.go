package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pgodlews/verity-api/core/spi"
)

func TestArrayLenEq(t *testing.T) {
	a := arrayLenAssertion{}
	actx := &spi.AssertionContext{Response: &spi.Response{Body: []byte(`{"tags":["a","b"]}`)}}
	res, err := a.Evaluate(context.Background(), actx, spi.AssertionSpec{Expr: "tags", Op: "eq", Value: float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Fatalf("expected pass, got %+v", res)
	}
}

func TestArrayLenMismatch(t *testing.T) {
	a := arrayLenAssertion{}
	actx := &spi.AssertionContext{Response: &spi.Response{Body: []byte(`{"tags":["a"]}`)}}
	res, _ := a.Evaluate(context.Background(), actx, spi.AssertionSpec{Expr: "tags", Op: "eq", Value: float64(2)})
	if res.Passed {
		t.Fatal("expected fail")
	}
}

func TestToIntJSONNumber(t *testing.T) {
	if got := toInt(json.Number("2")); got != 2 {
		t.Fatalf("toInt(json.Number) = %d, want 2", got)
	}
}

func TestArrayLenNotArray(t *testing.T) {
	a := arrayLenAssertion{}
	actx := &spi.AssertionContext{Response: &spi.Response{Body: []byte(`{"tags":"nope"}`)}}
	res, _ := a.Evaluate(context.Background(), actx, spi.AssertionSpec{Expr: "tags", Op: "eq", Value: float64(2)})
	if res.Passed {
		t.Fatal("expected fail for non-array")
	}
}
