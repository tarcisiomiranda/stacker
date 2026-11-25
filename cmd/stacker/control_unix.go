//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"
)

func tryLockInstanceFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errors.New("stacker is already running or starting")
	}
	return err
}

func processSignalAlive(proc *os.Process) bool {
	err := proc.Signal(syscall.Signal(0))
	return err == nil
}
