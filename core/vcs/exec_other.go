//go:build !unix && !windows

package vcs

import (
	"errors"
	"os"
	"os/exec"
)

func ownProcessGroup(cmd *exec.Cmd) {}

func commandOutput(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.Output()
	releaseGit(cmd)
	return out, err
}

func releaseGitExtra(cmd *exec.Cmd) {}

// cancelGit does not treat os.Interrupt as a successful signal. On platforms
// other than Unix and Windows, Go may accept the signal without delivering
// it to a process group.
func cancelGit(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("git process is not running")
	}
	err := cmd.Process.Signal(os.Interrupt)
	if !cancelSucceeded("os.Interrupt", err) {
		if err == nil {
			return errors.New("os.Interrupt does not cancel a git process group")
		}
		return err
	}
	return err
}
