//go:build windows

package vcs

import (
	"bytes"
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

// Windows has no SIGINT process group. Git is started in a new process
// group so CTRL_BREAK_EVENT reaches it and the children that share that
// group, and it is placed in a Job Object that ends those children when
// the job is terminated. os.Interrupt is never used: Go's Signal returns
// an error for it and does not signal the group. A nil error from that
// call would be a false success, so cancelSucceeded("os.Interrupt", …)
// is always false.

const (
	createNewProcessGroup = 0x00000200
	ctrlBreakEvent        = 1

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
)

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

func ownProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
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
	err := sendCtrlBreak(uint32(cmd.Process.Pid))
	if !cancelSucceeded("ctrl-break", err) {
		// Do not report os.Interrupt, and do not claim the break was
		// delivered. End the tree now; Wait still reap the process.
		terminateGitTree(cmd)
		if err == nil {
			return errors.New("CTRL_BREAK_EVENT was not delivered")
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

func sendCtrlBreak(pid uint32) error {
	if pid == 0 {
		return errors.New("git process is not running")
	}
	r1, _, err := procGenerateConsoleCtrlEvent.Call(uintptr(ctrlBreakEvent), uintptr(pid))
	if r1 == 0 {
		if err != nil && err != syscall.Errno(0) {
			return err
		}
		return errors.New("CTRL_BREAK_EVENT was not delivered")
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
	proc, _, err := procOpenProcess.Call(uintptr(processTerminate|processSetQuota), 0, uintptr(cmd.Process.Pid))
	if proc == 0 {
		procCloseHandle.Call(uintptr(handle))
		return
	}
	defer procCloseHandle.Call(proc)
	r1, _, err = procAssignProcessToJobObject.Call(uintptr(handle), proc)
	if r1 == 0 {
		procCloseHandle.Call(uintptr(handle))
		return
	}
	winJobs.Store(cmd, handle)
	_ = err
}
