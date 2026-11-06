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
	cmd := exec.Command(launcher, "-test.run=^TestWindowsGitConsoleLauncher$")
	cmd.Env = append(os.Environ(), "VERITY_WINDOWS_LAUNCHER_HELPER="+consoleExe)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v", err)
	}
	if strings.TrimSpace(string(out)) != "visible=0" {
		t.Fatalf("child acquired a visible console: %q", out)
	}
}

func TestWindowsGitConsoleLauncher(t *testing.T) {
	exe := os.Getenv("VERITY_WINDOWS_LAUNCHER_HELPER")
	if exe == "" {
		return
	}
	cmd := exec.Command(exe, "-test.run=^TestWindowsGitConsoleHelper$")
	cmd.Env = append(os.Environ(), "VERITY_WINDOWS_CONSOLE_HELPER=report")
	ownProcessGroup(cmd)
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
	ownProcessGroup(cmd)
	cmd.SysProcAttr.CreationFlags |= 0x10 // CREATE_NEW_CONSOLE, like a GUI parent.
	return cmd
}

func TestWindowsGitConsoleHelper(t *testing.T) {
	mode := os.Getenv("VERITY_WINDOWS_CONSOLE_HELPER")
	if mode == "" {
		return
	}
	getConsoleWindow := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")
	h, _, _ := getConsoleWindow.Call()
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
