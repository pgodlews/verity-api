package reporters

import (
	"encoding/xml"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

var updateGolden = flag.Bool("update", false, "update reporter golden files")

func TestReporterGoldens(t *testing.T) {
	for _, tc := range []struct {
		name string
		rep  spi.Reporter
	}{
		{name: "console", rep: &ConsoleReporter{}},
		{name: "json", rep: &JSONReporter{}},
		{name: "junit", rep: &JUnitReporter{}},
		{name: "html", rep: &HTMLReporter{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := render(t, tc.rep, goldenResult())
			path := filepath.Join("testdata", tc.name+".golden")
			if *updateGolden {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if out != string(want) {
				t.Fatalf("%s output differs from %s; run go test ./core/reporters -update\n--- got ---\n%s\n--- want ---\n%s", tc.name, path, out, want)
			}
			if tc.name == "junit" {
				var parsed junitTestsuites
				if err := xml.Unmarshal([]byte(out), &parsed); err != nil {
					t.Fatalf("JUnit golden is not well-formed XML: %v", err)
				}
			}
		})
	}
}

func goldenResult() *spi.RunResult {
	res := &spi.RunResult{
		Project: "Demo <&> 東京", Environment: `dev "quoted"`,
		Redactor: secrets.NewRedactor(),
		Suites: []spi.SuiteResult{{
			Name: "Suite <&> ☃", Status: spi.StatusAborted,
			Cases: []spi.CaseResult{
				{
					Name: "feed case", Status: spi.StatusFailed, Iteration: 2,
					Steps: []spi.StepResult{
						{
							Name: "pass ✓", Protocol: "rest", Status: spi.StatusPassed, Attempts: 3,
							Request:    &spi.RequestCapture{Method: "GET", URL: "${BASE_URL}/widgets?note=<&>", Body: "secret=" + secrets.Mask},
							Response:   &spi.ResponseCapture{StatusCode: 200, Body: `{"message":"héllo <&>","token":"` + secrets.Mask + `"}`},
							Assertions: []spi.AssertionResult{{Passed: true, Type: "status", Description: "status ✓"}},
						},
						{
							Name: "fail <&>", Protocol: "graphql", Status: spi.StatusFailed, Attempts: 2,
							Assertions: []spi.AssertionResult{{
								Passed: false, Type: "jsonpath", Description: `value <expected> & "quoted"`,
								Message: "got 東京; token=" + secrets.Mask, Expected: "<&>", Actual: secrets.Mask,
							}},
						},
					},
				},
				{
					Name: "errors & skips", Status: spi.StatusAborted, Iteration: -1,
					Steps: []spi.StepResult{
						{Name: "error", Protocol: "soap", Status: spi.StatusError, Error: &spi.ErrorInfo{Code: "transport_error", Message: "bad <request> & " + secrets.Mask}},
						{Name: "skip", Protocol: "rest", Status: spi.StatusSkipped},
						{Name: "abort", Protocol: "rest", Status: spi.StatusAborted},
					},
				},
			},
		}},
		Summary: spi.Summary{
			Suites: 1, Cases: 2, Steps: 5, Assertions: 2, Passed: 1, Failed: 1,
			Errors: 1, Skipped: 1, StepsFailed: 1, StepsAborted: 1,
			Status: spi.StatusAborted, Aborted: true,
		},
	}
	res.StartedAt = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	res.FinishedAt = res.StartedAt.Add(2 * time.Second)
	res.DurationMS = 2000
	for si := range res.Suites {
		res.Suites[si].DurationMS = int64(100 * (si + 1))
		for ci := range res.Suites[si].Cases {
			c := &res.Suites[si].Cases[ci]
			c.DurationMS = int64(100 * (ci + 1))
			for sti := range c.Steps {
				st := &c.Steps[sti]
				st.DurationMS = int64(10 * (sti + 1))
				if st.Response != nil {
					st.Response.DurationMS = st.DurationMS
				}
			}
		}
	}
	return res
}
