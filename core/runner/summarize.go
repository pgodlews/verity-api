package runner

import "github.com/pgodlews/verity-api/core/spi"

// rollupSteps computes a case status from its step statuses.
func rollupSteps(steps []spi.StepResult) spi.Status {
	return rollup(len(steps), func(i int) spi.Status { return steps[i].Status })
}

// rollupCases computes a suite status from its case statuses.
func rollupCases(cases []spi.CaseResult) spi.Status {
	return rollup(len(cases), func(i int) spi.Status { return cases[i].Status })
}

// rollup combines n child statuses: error beats failed, failed beats
// aborted, and a mix of passes with an aborted child is aborted (the parent
// did not finish). No children, or all skipped, is skipped.
func rollup(n int, status func(int) spi.Status) spi.Status {
	out := spi.StatusPassed
	allSkipped := true
	sawAborted := false
	for i := 0; i < n; i++ {
		s := status(i)
		if s != spi.StatusSkipped {
			allSkipped = false
		}
		switch s {
		case spi.StatusError:
			return spi.StatusError
		case spi.StatusFailed:
			out = spi.StatusFailed
		case spi.StatusAborted:
			sawAborted = true
		}
	}
	if allSkipped {
		return spi.StatusSkipped
	}
	if out == spi.StatusPassed && sawAborted {
		return spi.StatusAborted
	}
	return out
}

// summarize aggregates counts and the overall status across the run.
func summarize(result *spi.RunResult) spi.Summary {
	s := spi.Summary{Status: spi.StatusPassed}
	worst := spi.StatusPassed
	for _, suite := range result.Suites {
		s.Suites++
		for _, c := range suite.Cases {
			s.Cases++
			switch c.Status {
			case spi.StatusPassed:
				s.CasesPassed++
			case spi.StatusFailed:
				s.CasesFailed++
			case spi.StatusError:
				s.CasesErrored++
			case spi.StatusSkipped:
				s.CasesSkipped++
			case spi.StatusAborted:
				s.CasesAborted++
			}
			for _, st := range c.Steps {
				s.Steps++
				switch st.Status {
				case spi.StatusError:
					s.Errors++
					s.StepsErrored++
					worst = worse(worst, spi.StatusError)
				case spi.StatusFailed:
					s.StepsFailed++
					worst = worse(worst, spi.StatusFailed)
				case spi.StatusSkipped:
					s.Skipped++
					s.StepsSkipped++
				case spi.StatusAborted:
					s.StepsAborted++
					worst = worse(worst, spi.StatusAborted)
				}
				for _, a := range st.Assertions {
					s.Assertions++
					if a.Passed {
						s.Passed++
						s.AssertionsPassed++
					} else {
						s.Failed++
						s.AssertionsFailed++
						worst = worse(worst, spi.StatusFailed)
					}
				}
			}
		}
	}
	s.Status = worst
	return s
}

// worse returns the more severe of two statuses
// (error > failed > aborted > passed/skipped).
func worse(a, b spi.Status) spi.Status {
	if statusRank(b) > statusRank(a) {
		return b
	}
	return a
}

// statusRank orders statuses by severity without building a map per call.
func statusRank(s spi.Status) int {
	switch s {
	case spi.StatusAborted:
		return 1
	case spi.StatusFailed:
		return 2
	case spi.StatusError:
		return 3
	default: // passed, skipped
		return 0
	}
}
