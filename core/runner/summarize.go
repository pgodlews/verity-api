package runner

import "github.com/pgodlews/verity-api/core/spi"

// rollupSteps computes a case status from its step statuses.
// error beats failed, failed beats aborted, and a mix of passes with an
// aborted step is aborted (the case did not finish). All-skipped stays skipped.
func rollupSteps(steps []spi.StepResult) spi.Status {
	if len(steps) == 0 {
		return spi.StatusSkipped
	}
	status := spi.StatusPassed
	allSkipped := true
	sawAborted := false
	for _, s := range steps {
		if s.Status != spi.StatusSkipped {
			allSkipped = false
		}
		switch s.Status {
		case spi.StatusError:
			return spi.StatusError
		case spi.StatusFailed:
			status = spi.StatusFailed
		case spi.StatusAborted:
			sawAborted = true
		}
	}
	if allSkipped {
		return spi.StatusSkipped
	}
	if status == spi.StatusPassed && sawAborted {
		return spi.StatusAborted
	}
	return status
}

// rollupCases computes a suite status from its case statuses.
func rollupCases(cases []spi.CaseResult) spi.Status {
	if len(cases) == 0 {
		return spi.StatusSkipped
	}
	status := spi.StatusPassed
	allSkipped := true
	sawAborted := false
	for _, c := range cases {
		if c.Status != spi.StatusSkipped {
			allSkipped = false
		}
		switch c.Status {
		case spi.StatusError:
			return spi.StatusError
		case spi.StatusFailed:
			status = spi.StatusFailed
		case spi.StatusAborted:
			sawAborted = true
		}
	}
	if allSkipped {
		return spi.StatusSkipped
	}
	if status == spi.StatusPassed && sawAborted {
		return spi.StatusAborted
	}
	return status
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
	rank := map[spi.Status]int{
		spi.StatusPassed:  0,
		spi.StatusSkipped: 0,
		spi.StatusAborted: 1,
		spi.StatusFailed:  2,
		spi.StatusError:   3,
	}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
