//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
)

var errProcessNotFound = errors.New("process not found")

// Placeholder values; Windows uses taskkill instead of POSIX signals.
const (
	signalTerm syscall.Signal = 15
	signalKill syscall.Signal = 9
)

func shellName() string   { return "cmd" }
func shellRunArg() string { return "/C" }

func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	if len(cmd.Args) == 3 && cmd.Args[1] == "/C" {
		cmd.SysProcAttr.CmdLine = syscall.EscapeArg(cmd.Path) + " /D /S /C \"" + cmd.Args[2] + "\""
	}
}

func signalProcessGroup(pid int, sig syscall.Signal) error {
	args := []string{"/PID", strconv.Itoa(pid), "/T"}
	if sig == signalKill {
		args = append([]string{"/F"}, args...)
	}
	cmd := exec.Command("taskkill", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// 128: not found
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 128 {
			return errProcessNotFound
		}
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 255 && sig == signalTerm {
			return nil
		}
		return fmt.Errorf("taskkill: %w (%s)", err, string(out))
	}
	return nil
}
