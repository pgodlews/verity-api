//go:build windows

package vcs

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Reproduce the desktop launch context: a GUI-subsystem parent starting a
// console-subsystem child. A console test runner alone does not reproduce it.
func TestWindowsGitHiddenConsole(t *testing.T) {
	consoleExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(t.TempDir(), "gui-launcher.exe")
	build := exec.Command("go", "test", "-c", "-ldflags=-H=windowsgui", "-o", launcher, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build GUI launcher: %v\n%s", err, out)
	}
	for _, background := range []string{"false", "true"} {
		cmd := exec.Command(launcher, "-test.run=^TestWindowsGitConsoleLauncher$")
		cmd.Env = append(os.Environ(), "VERITY_WINDOWS_LAUNCHER_HELPER="+consoleExe, "VERITY_WINDOWS_BACKGROUND_READ="+background)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper: %v", err)
		}
		want := "visible=0"
		if background == "true" {
			want = "console=0"
		}
		if strings.TrimSpace(string(out)) != want {
			t.Fatalf("background=%s console state: %q, want %q", background, out, want)
		}
	}
}

func TestWindowsGitConsoleLauncher(t *testing.T) {
	exe := os.Getenv("VERITY_WINDOWS_LAUNCHER_HELPER")
	if exe == "" {
		return
	}
	cmd := exec.Command(exe, "-test.run=^TestWindowsGitConsoleHelper$")
	background := os.Getenv("VERITY_WINDOWS_BACKGROUND_READ") == "true"
	mode := "report"
	if background {
		mode = "no-console"
	}
	cmd.Env = append(os.Environ(), "VERITY_WINDOWS_CONSOLE_HELPER="+mode)
	ownProcessGroup(cmd, background)
	out, err := commandOutput(cmd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(string(out))
	os.Exit(0)
}

func TestWindowsGitCancelHiddenConsole(t *testing.T) {
	cmd := windowsConsoleHelper(t, "wait")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	attachJob(cmd)
	t.Cleanup(func() {
		terminateGitTree(cmd)
		releaseGit(cmd)
	})
	ready := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		if s.Scan() {
			ready <- s.Text()
		} else {
			ready <- "no readiness message"
		}
	}()
	select {
	case line := <-ready:
		if line != "visible=0" {
			t.Fatalf("child console state: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not start")
	}
	// Even a child in a separate console must terminate and be reaped.
	_ = cancelGit(cmd)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("waiting helper unexpectedly succeeded")
		}
	case <-time.After(gitCancelGrace + 5*time.Second):
		t.Fatal("hidden-console process survived cancellation")
	}
}

func windowsConsoleHelper(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestWindowsGitConsoleHelper$")
	cmd.Env = append(os.Environ(), "VERITY_WINDOWS_CONSOLE_HELPER="+mode)
	ownProcessGroup(cmd, false)
	return cmd
}

func TestWindowsGitCancellationIsIsolated(t *testing.T) {
	start := func() (*exec.Cmd, <-chan error) {
		t.Helper()
		cmd := windowsConsoleHelper(t, "wait")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		attachJob(cmd)
		t.Cleanup(func() { terminateGitTree(cmd); releaseGit(cmd) })
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		ready := make(chan bool, 1)
		go func() {
			s := bufio.NewScanner(stdout)
			ready <- s.Scan() && s.Text() == "visible=0"
		}()
		select {
		case ok := <-ready:
			if !ok {
				t.Fatal("hidden helper did not start")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("helper readiness timeout")
		}
		return cmd, done
	}
	first, firstDone := start()
	second, secondDone := start()
	if err := cancelGit(first); err != nil {
		t.Fatalf("signal first private console: %v", err)
	}
	select {
	case <-firstDone:
	case <-time.After(gitCancelGrace + 5*time.Second):
		t.Fatal("first process survived cancellation")
	}
	select {
	case <-secondDone:
		t.Fatal("cancellation reached another private console")
	case <-time.After(100 * time.Millisecond):
	}
	// Also exercise the termination fallback when no signal can be delivered.
	if handle, ok := winProcesses.LoadAndDelete(second); ok {
		procCloseHandle.Call(uintptr(handle.(syscall.Handle)))
	}
	if err := cancelGit(second); err == nil {
		t.Fatal("failed signal was reported as delivered")
	}
	select {
	case <-secondDone:
	case <-time.After(gitCancelGrace + 5*time.Second):
		t.Fatal("fallback did not terminate the process")
	}
}

func TestWindowsGitConsoleHelper(t *testing.T) {
	mode := os.Getenv("VERITY_WINDOWS_CONSOLE_HELPER")
	if mode == "" {
		return
	}
	getConsoleWindow := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")
	h, _, _ := getConsoleWindow.Call()
	if mode == "no-console" {
		fmt.Printf("console=%d\n", h)
		os.Exit(0)
	}
	isWindowVisible := syscall.NewLazyDLL("user32.dll").NewProc("IsWindowVisible")
	visible, _, _ := isWindowVisible.Call(h)
	fmt.Printf("visible=%d\n", visible)
	if mode == "wait" {
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(0)
}

// Run the real Git cancellation regression from a GUI-subsystem parent too.
func TestWindowsGitGUICancel(t *testing.T) {
	launcher := filepath.Join(t.TempDir(), "gui-cancel.exe")
	build := exec.Command("go", "test", "-c", "-ldflags=-H=windowsgui", "-o", launcher, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build GUI launcher: %v\n%s", err, out)
	}
	cmd := exec.Command(launcher, "-test.run=^TestCancelStageRemovesIndexLock$", "-test.v")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("GUI Git cancellation: %v\n%s", err, out)
	}
}
