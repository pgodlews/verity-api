package reporters

import (
	"fmt"
	"strings"

	"github.com/pgodlews/verity-api/core/spi"
)

// ConsoleReporter writes a human-readable, indented summary to stdout (or a
// file). It uses simple ASCII markers so output is CI-log friendly and free of
// color-control noise when redirected.
type ConsoleReporter struct{}

func (ConsoleReporter) Type() string { return "console" }

func (r *ConsoleReporter) Report(result *spi.RunResult, out string) (err error) {
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
	fmt.Fprintf(w, "Verity run: %s", result.Project)
	if result.Environment != "" {
		fmt.Fprintf(w, " [env: %s]", result.Environment)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, strings.Repeat("=", 60))

	for _, suite := range result.Suites {
		fmt.Fprintf(w, "%s SUITE %s (%dms)\n", mark(suite.Status), suite.Name, suite.DurationMS)
		for _, c := range suite.Cases {
			label := c.Name
			if c.Iteration >= 0 {
				label = fmt.Sprintf("%s [row %d]", c.Name, c.Iteration)
			}
			fmt.Fprintf(w, "  %s case: %s (%dms)\n", mark(c.Status), label, c.DurationMS)
			for _, st := range c.Steps {
				attemptNote := ""
				if st.Attempts > 1 {
					attemptNote = fmt.Sprintf(", %d attempts", st.Attempts)
				}
				fmt.Fprintf(w, "    %s step: %s [%s] (%dms%s)\n",
					mark(st.Status), st.Name, st.Protocol, st.DurationMS, attemptNote)
				if st.Error != nil {
					fmt.Fprintf(w, "      ! error [%s]: %s\n", st.Error.Code,
						redact(red, st.Error.Message))
				}
				for _, a := range st.Assertions {
					m := mark(boolStatus(a.Passed))
					desc := a.Description
					if desc == "" {
						desc = a.Type
					}
					fmt.Fprintf(w, "      %s assert: %s", m, desc)
					if !a.Passed && a.Message != "" {
						fmt.Fprintf(w, " — %s", redact(red, a.Message))
					}
					fmt.Fprintln(w)
				}
			}
		}
	}

	s := result.Summary
	fmt.Fprintln(w, strings.Repeat("-", 60))
	label := strings.ToUpper(string(s.Status))
	if s.Bailed {
		label += " (bailed)"
	}
	if s.Aborted {
		label += " (aborted)"
	}
	fmt.Fprintf(w, "Result: %s | suites=%d cases=%d steps=%d (failed=%d, errors=%d, skipped=%d, aborted=%d) assertions=%d (passed=%d, failed=%d) | %dms\n",
		label, s.Suites, s.Cases, s.Steps, s.StepsFailed, s.Errors, s.Skipped, s.StepsAborted,
		s.Assertions, s.Passed, s.Failed, result.DurationMS)
	return nil
}

func mark(st spi.Status) string {
	switch st {
	case spi.StatusPassed:
		return "[PASS]"
	case spi.StatusFailed:
		return "[FAIL]"
	case spi.StatusError:
		return "[ERR ]"
	case spi.StatusSkipped:
		return "[SKIP]"
	case spi.StatusAborted:
		return "[ABRT]"
	default:
		return "[????]"
	}
}

func boolStatus(ok bool) spi.Status {
	if ok {
		return spi.StatusPassed
	}
	return spi.StatusFailed
}
