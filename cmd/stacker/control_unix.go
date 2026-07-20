//go:build unix

package main

import (
	"os"
	"syscall"
)

func processSignalAlive(proc *os.Process) bool {
	err := proc.Signal(syscall.Signal(0))
	return err == nil
}
