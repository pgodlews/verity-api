package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/pgodlews/verity-api/core/spi"
)

type fakeReporter struct {
	typ   string
	fail  map[string]bool
	calls *[]string
}

func (f fakeReporter) Type() string { return f.typ }

func (f fakeReporter) Report(_ *spi.RunResult, out string) error {
	*f.calls = append(*f.calls, f.typ+":"+out)
	if f.fail[f.typ+":"+out] {
		return errors.New("write failed")
	}
	return nil
}

type fakeReporters struct {
	fail  map[string]bool
	calls []string
}

func (f *fakeReporters) Reporter(typ string) (spi.Reporter, error) {
	return fakeReporter{typ: typ, fail: f.fail, calls: &f.calls}, nil
}

// run and watch share one report writer: stdout first, then junit, json,
// html, and --reporter targets. A stdout failure still writes the files; a
// file failure stops the rest.
func TestWriteReportsOrderAndFailures(t *testing.T) {
	result := &spi.RunResult{}
	out := reportOutputs{JUnit: "j.xml", JSON: "r.json", Extra: []reporterTarget{{"tap", "r.tap"}}}

	reg := &fakeReporters{fail: map[string]bool{"console:-": true}}
	stdoutRC, fileRC := writeReports(reg, result, out)
	if stdoutRC == exitOK || fileRC != exitOK {
		t.Fatalf("rc = %d, %d", stdoutRC, fileRC)
	}
	want := []string{"console:-", "junit:j.xml", "json:r.json", "tap:r.tap"}
	if !reflect.DeepEqual(reg.calls, want) {
		t.Fatalf("calls %v, want %v", reg.calls, want)
	}

	reg = &fakeReporters{fail: map[string]bool{"junit:j.xml": true}}
	out.JSONStdout = true
	stdoutRC, fileRC = writeReports(reg, result, out)
	if stdoutRC != exitOK || fileRC == exitOK {
		t.Fatalf("rc = %d, %d", stdoutRC, fileRC)
	}
	if want := []string{"json:-", "junit:j.xml"}; !reflect.DeepEqual(reg.calls, want) {
		t.Fatalf("calls %v, want %v", reg.calls, want)
	}

	reg = &fakeReporters{}
	if _, rc := writeReports(reg, result, reportOutputs{Quiet: true}); rc != exitOK || len(reg.calls) != 0 {
		t.Fatalf("quiet run wrote %v", reg.calls)
	}
}
