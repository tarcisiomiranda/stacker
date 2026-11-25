//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

func tryLockInstanceFile(f *os.File) error {
	var overlap windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errors.New("stacker is already running or starting")
	}
	return err
}

func processSignalAlive(proc *os.Process) bool {
	// Signal(0) is not portable on Windows; use tasklist.
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(proc.Pid), "/NH")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(proc.Pid))
}
