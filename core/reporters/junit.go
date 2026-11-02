package reporters

import (
	"encoding/xml"
	"fmt"
	"io"

	"github.com/pgodlews/verity-api/core/spi"
)

// JUnitReporter emits JUnit-style XML consumable by CI systems (Jenkins,
// GitLab, GitHub Actions test reporters). Each Verity suite maps to a
// <testsuite> and each step maps to a <testcase>; failed assertions/errors
// become <failure>/<error> children.
type JUnitReporter struct{}

func (JUnitReporter) Type() string { return "junit" }

type junitTestsuites struct {
	XMLName  xml.Name         `xml:"testsuites"`
	Name     string           `xml:"name,attr"`
	Tests    int              `xml:"tests,attr"`
	Failures int              `xml:"failures,attr"`
	Errors   int              `xml:"errors,attr"`
	Skipped  int              `xml:"skipped,attr"`
	Time     float64          `xml:"time,attr"`
	Suites   []junitTestsuite `xml:"testsuite"`
}

type junitTestsuite struct {
	Name     string          `xml:"name,attr"`
	Tests    int             `xml:"tests,attr"`
	Failures int             `xml:"failures,attr"`
	Errors   int             `xml:"errors,attr"`
	Skipped  int             `xml:"skipped,attr"`
	Time     float64         `xml:"time,attr"`
	Cases    []junitTestcase `xml:"testcase"`
}

type junitTestcase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      float64       `xml:"time,attr"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	Error     *junitMessage `xml:"error,omitempty"`
	Skipped   *junitSkipped `xml:"skipped,omitempty"`
	// SystemOut records retries. JUnit has no attempts attribute; CI viewers
	// still show system-out on the testcase.
	SystemOut string `xml:"system-out,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr,omitempty"`
	Text    string `xml:",chardata"`
}

// junitSkipped is a <skipped> element. Message is "aborted" when the step or
// case never started because the run was cancelled. A deliberate skip omits it.
type junitSkipped struct {
	Message string `xml:"message,attr,omitempty"`
}

func (r *JUnitReporter) Report(result *spi.RunResult, out string) (err error) {
	w, closeFn, err := openOut(out)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := closeFn(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	red := result.Redactor
	root := junitTestsuites{
		Name: result.Project,
		Time: float64(result.DurationMS) / 1000.0,
	}

	for _, suite := range result.Suites {
		ts := junitTestsuite{Name: suite.Name, Time: float64(suite.DurationMS) / 1000.0}
		for _, c := range suite.Cases {
			if len(c.Steps) == 0 {
				// A case that never started (--bail, or a cancelled run with
				// no steps to list) has no steps. Emit one skipped testcase
				// so the CI report still lists it. Aborted cases say why.
				if c.Status == spi.StatusSkipped || c.Status == spi.StatusAborted {
					skipped := &junitSkipped{}
					if c.Status == spi.StatusAborted {
						skipped.Message = "aborted"
					}
					ts.Cases = append(ts.Cases, junitTestcase{
						Name:      c.Name,
						Classname: suite.Name,
						Skipped:   skipped,
					})
					ts.Tests++
					root.Tests++
					ts.Skipped++
					root.Skipped++
				}
				continue
			}
			for _, st := range c.Steps {
				tc := junitTestcase{
					Name:      stepName(c, st),
					Classname: fmt.Sprintf("%s.%s", suite.Name, c.Name),
					Time:      float64(st.DurationMS) / 1000.0,
				}
				if st.Attempts > 1 {
					tc.SystemOut = fmt.Sprintf("%d attempts", st.Attempts)
				}
				ts.Tests++
				root.Tests++

				switch st.Status {
				case spi.StatusError:
					msg := "step error"
					detail := ""
					if st.Error != nil {
						msg = st.Error.Code
						detail = redact(red, st.Error.Message)
					}
					tc.Error = &junitMessage{Message: msg, Type: "error", Text: detail}
					ts.Errors++
					root.Errors++
				case spi.StatusFailed:
					tc.Failure = &junitMessage{
						Message: "assertion failure",
						Type:    "assertion",
						Text:    redact(red, failureText(st)),
					}
					ts.Failures++
					root.Failures++
				case spi.StatusSkipped:
					tc.Skipped = &junitSkipped{}
					ts.Skipped++
					root.Skipped++
				case spi.StatusAborted:
					tc.Skipped = &junitSkipped{Message: "aborted"}
					ts.Skipped++
					root.Skipped++
				}
				ts.Cases = append(ts.Cases, tc)
			}
		}
		root.Suites = append(root.Suites, ts)
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(root); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n")
	return err
}

func stepName(c spi.CaseResult, st spi.StepResult) string {
	if c.Iteration >= 0 {
		return fmt.Sprintf("%s[%d]", st.Name, c.Iteration)
	}
	return st.Name
}

func failureText(st spi.StepResult) string {
	out := ""
	if st.Attempts > 1 {
		out += fmt.Sprintf("after %d attempts\n", st.Attempts)
	}
	for _, a := range st.Assertions {
		if !a.Passed {
			desc := a.Description
			if desc == "" {
				desc = a.Type
			}
			out += fmt.Sprintf("- %s: %s\n", desc, a.Message)
		}
	}
	return out
}
