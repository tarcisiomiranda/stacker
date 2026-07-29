//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// daemonizeServe re-execs detached on Windows and waits for the control plane.
func daemonizeServe(configPath string, withWeb bool) int {
	if st, err := findRunningInstance(configPath); err == nil && st != nil {
		printServeAlreadyRunning(configPath, st)
		return 0
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemonize: executable:", err)
		return 1
	}
	args := []string{"--config", configPath, "serve"}
	if withWeb {
		args = append(args, "--web")
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "STACKER_DAEMON_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
	}

	logPath := daemonLogPath(configPath)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "daemonize: log dir:", err)
		return 1
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemonize: log file:", err)
		return 1
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		fmt.Fprintln(os.Stderr, "daemonize:", err)
		return 1
	}
	childPID := cmd.Process.Pid
	_ = logFile.Close()

	st, err := waitServeReady(configPath, childPID, 5*time.Second)
	if err != nil {
		_, _ = cmd.Process.Wait()
		fmt.Fprintln(os.Stderr, "stacker serve failed to start:", err)
		if tail := tailFile(logPath, 12); tail != "" {
			fmt.Fprintln(os.Stderr, "--- log ---")
			fmt.Fprint(os.Stderr, tail)
		}
		fmt.Fprintf(os.Stderr, "log file: %s\n", logPath)
		return 1
	}
	_ = cmd.Process.Release()
	printServeStarted(configPath, st.PID, logPath)
	return 0
}

func waitServeReady(configPath string, childPID int, timeout time.Duration) (*InstanceState, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st, err := findRunningInstance(configPath); err == nil && st != nil {
			return st, nil
		}
		// Best-effort: if the process vanished, fail early.
		if proc, err := os.FindProcess(childPID); err == nil {
			// On Windows, Signal(0) is not reliable; check via OpenProcess is heavy.
			// Poll until timeout; ProcessState is only set after Wait.
			_ = proc
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st, err := findRunningInstance(configPath); err == nil && st != nil {
		return st, nil
	}
	return nil, fmt.Errorf("timed out waiting for control plane (pid %d)", childPID)
}
