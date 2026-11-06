//go:build windows

package vcs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"
)

// Git for Windows cleans up locks on CTRL_C_EVENT, not CTRL_BREAK_EVENT.
// CTRL_C cannot be scoped to a process group, so each Git command gets a
// separate hidden console. A short-lived re-exec helper joins that console
// to signal only Git and its children, leaving the host's console and signal
// handlers untouched. A Job Object still supplies the termination fallback.

const (
	createNewConsole = 0x00000010
	createNoWindow   = 0x08000000

	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x2000

	processTerminate = 0x0001
	processSetQuota  = 0x0100
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
	procOpenProcess              = kernel32.NewProc("OpenProcess")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procCloseHandle              = kernel32.NewProc("CloseHandle")
	procAttachConsole            = kernel32.NewProc("AttachConsole")
	procFreeConsole              = kernel32.NewProc("FreeConsole")
	procSetConsoleCtrlHandler    = kernel32.NewProc("SetConsoleCtrlHandler")
)

const gitInterruptHelper = "--verity-internal-git-interrupt"

func init() {
	// This mode is internal to runGit. Handle it before either the desktop or
	// CLI starts; the helper never opens a window or reads project data.
	if len(os.Args) != 3 || os.Args[1] != gitInterruptHelper {
		return
	}
	pid, err := strconv.ParseUint(os.Args[2], 10, 32)
	if err != nil || pid == 0 {
		os.Exit(2)
	}
	if err := interruptConsole(uint32(pid)); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func interruptConsole(pid uint32) error {
	attached, _, err := procAttachConsole.Call(uintptr(pid))
	if attached == 0 {
		return err
	}
	defer procFreeConsole.Call()
	// Ignore CTRL_C in this helper only. Git has its own enabled handler.
	ignored, _, err := procSetConsoleCtrlHandler.Call(0, 1)
	if ignored == 0 {
		return err
	}
	delivered, _, err := procGenerateConsoleCtrlEvent.Call(0, 0)
	if delivered == 0 {
		return err
	}
	return nil
}

// Field order matches the Windows x64 SDK structures. If SetInformationJobObject
// rejects the struct, job setup fails and cancel still uses the process
// group, then TerminateProcess after the grace period.
type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimits struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var winJobs sync.Map
var winProcesses sync.Map

func ownProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// CREATE_NO_WINDOW and CREATE_NEW_PROCESS_GROUP both prevent delivery of
	// the CTRL_C Git needs to clean up locks. Use a private hidden console.
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNewConsole
}

func commandOutput(cmd *exec.Cmd) ([]byte, error) {
	var stdout bytes.Buffer
	if cmd.Stdout != nil {
		return nil, errors.New("git stdout is already set")
	}
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Assign before git has a chance to spawn a clean filter. The window
	// is the time between Start returning and this call.
	attachJob(cmd)
	err := cmd.Wait()
	releaseGit(cmd)
	return stdout.Bytes(), err
}

func releaseGitExtra(cmd *exec.Cmd) {
	if v, ok := winProcesses.LoadAndDelete(cmd); ok {
		procCloseHandle.Call(uintptr(v.(syscall.Handle)))
	}
	v, ok := winJobs.LoadAndDelete(cmd)
	if !ok {
		return
	}
	h, ok := v.(syscall.Handle)
	if !ok || h == 0 {
		return
	}
	procCloseHandle.Call(uintptr(h))
}

func cancelGit(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("git process is not running")
	}
	err := sendGitInterrupt(cmd)
	if !cancelSucceeded("ctrl-c", err) {
		// Do not claim a signal was delivered. Wait still reaps the process.
		terminateGitTree(cmd)
		if err == nil {
			return errors.New("git interrupt was not delivered")
		}
		return err
	}
	beginKillTimer(cmd, func() {
		if gitExited(cmd) {
			return
		}
		terminateGitTree(cmd)
	})
	return nil
}

func sendGitInterrupt(cmd *exec.Cmd) error {
	// Retain an OS handle until Wait and cancellation finish so this PID
	// cannot be recycled while the helper is starting.
	if _, ok := winProcesses.Load(cmd); !ok {
		return errors.New("git process is not attached")
	}
	exe, err := os.Executable()
	if err != nil {
		return errors.New("cannot locate git interrupt helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitCancelGrace)
	defer cancel()
	helper := exec.CommandContext(ctx, exe, gitInterruptHelper, strconv.Itoa(cmd.Process.Pid))
	helper.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := helper.Run(); err != nil {
		return errors.New("git interrupt helper failed")
	}
	return nil
}

func terminateGitTree(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if v, ok := winJobs.Load(cmd); ok {
		if h, ok := v.(syscall.Handle); ok && h != 0 {
			procTerminateJobObject.Call(uintptr(h), 1)
		}
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func attachJob(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	proc, _, _ := procOpenProcess.Call(uintptr(processTerminate|processSetQuota), 0, uintptr(cmd.Process.Pid))
	if proc == 0 {
		return
	}
	winProcesses.Store(cmd, syscall.Handle(proc))
	job, _, err := procCreateJobObjectW.Call(0, 0)
	if job == 0 {
		return
	}
	handle := syscall.Handle(job)
	info := jobObjectExtendedLimits{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	r1, _, _ := procSetInformationJobObject.Call(
		uintptr(handle),
		uintptr(jobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
	)
	if r1 == 0 {
		procCloseHandle.Call(uintptr(handle))
		return
	}
	r1, _, err = procAssignProcessToJobObject.Call(uintptr(handle), proc)
	if r1 == 0 {
		procCloseHandle.Call(uintptr(handle))
		return
	}
	winJobs.Store(cmd, handle)
	_ = err
}
