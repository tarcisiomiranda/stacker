//go:build windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func processSignalAlive(proc *os.Process) bool {
	// Signal(0) is not portable on Windows; use tasklist.
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(proc.Pid), "/NH")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(proc.Pid))
}
