//go:build unix

package main

import (
	"errors"
	"os/exec"
	"syscall"
)

var errProcessNotFound = syscall.ESRCH

const (
	signalTerm = syscall.SIGTERM
	signalKill = syscall.SIGKILL
)

func shellName() string   { return "/bin/sh" }
func shellRunArg() string { return "-c" }

func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalProcessGroup(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return errProcessNotFound
		}
		// Fall back to the process itself when it is not a group leader.
		if err2 := syscall.Kill(pid, sig); err2 != nil {
			if errors.Is(err2, syscall.ESRCH) {
				return errProcessNotFound
			}
			return err2
		}
	}
	return nil
}
