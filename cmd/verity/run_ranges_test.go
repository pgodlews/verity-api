package main

import "testing"

func TestRunRejectsOutOfRangeParallelAndUntilStep(t *testing.T) {
	for _, flag := range []string{"--parallel=0", "--parallel=-2", "--until-step=-1"} {
		var code int
		withStderr(t, func() { code = run([]string{"run", "project.yaml", flag}) })
		if code != exitUsage {
			t.Errorf("%s: exit %d, want %d", flag, code, exitUsage)
		}
	}
}
