//go:build unix

package vcs

import (
	"errors"
	"os/exec"
	"syscall"
)

func ownProcessGroup(cmd *exec.Cmd, _ bool) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func commandOutput(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.Output()
	releaseGit(cmd)
	return out, err
}

func releaseGitExtra(cmd *exec.Cmd) {}

// cancelGit signals the process group. Git removes index.lock on SIGINT.
// The group includes a clean filter, a hook, or an editor git started.
// The follow-up SIGKILL is cancelled when Wait returns, and it is not
// retried against the bare pid once the process has exited.
func cancelGit(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("git process is not running")
	}
	pid := cmd.Process.Pid
	_ = signalGroup(pid, syscall.SIGINT, func() bool { return false })
	beginKillTimer(cmd, func() {
		_ = signalGroup(pid, syscall.SIGKILL, func() bool { return gitExited(cmd) })
	})
	return nil
}

func signalGroup(pid int, sig syscall.Signal, exited func() bool) error {
	if pid <= 0 || (exited != nil && exited()) {
		return nil
	}
	err := syscall.Kill(-pid, sig)
	if err == nil {
		return nil
	}
	gone := exited != nil && exited()
	if !fallbackToPidKill(errors.Is(err, syscall.ESRCH), gone) {
		return err
	}
	if exited != nil && exited() {
		return err
	}
	return syscall.Kill(pid, sig)
}
