//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// daemonizeServe re-execs the current binary as a background child with
// STACKER_DAEMON_CHILD=1, waits until the control plane is up (or the child
// exits with an error), then prints attach/stop hints.
func daemonizeServe(configPath string, withWeb bool) int {
	// Idempotent: already up for this config → print how to attach, exit 0.
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

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
		// Reap the child so it does not stay zombie; show last log lines.
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

// waitServeReady polls until the config's control plane answers, or the child
// process exits (failure).
func waitServeReady(configPath string, childPID int, timeout time.Duration) (*InstanceState, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st, err := findRunningInstance(configPath); err == nil && st != nil {
			return st, nil
		}
		// Non-blocking wait: child exited without becoming ready.
		var status syscall.WaitStatus
		wpid, err := syscall.Wait4(childPID, &status, syscall.WNOHANG, nil)
		if err == nil && wpid == childPID {
			code := status.ExitStatus()
			return nil, fmt.Errorf("background process exited (pid %d, code %d)", childPID, code)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Last chance.
	if st, err := findRunningInstance(configPath); err == nil && st != nil {
		return st, nil
	}
	// Child may still be starting slowly — do not kill it; report timeout.
	return nil, fmt.Errorf("timed out waiting for control plane (pid %d)", childPID)
}
