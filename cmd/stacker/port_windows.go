//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func listenersOnPort(port int) ([]int, error) {
	// netstat -ano; look for LISTENING lines with :port
	cmd := exec.Command("netstat", "-ano", "-p", "tcp")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("netstat: %w", err)
	}

	needle := fmt.Sprintf(":%d", port)
	seen := map[int]struct{}{}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		// Proto LocalAddress ForeignAddress State PID
		if !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		local := fields[1]
		state := fields[3]
		if !strings.EqualFold(state, "LISTENING") {
			continue
		}
		if !portMatchWindows(local, port, needle) {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}
	return pids, nil
}

func portMatchWindows(local string, port int, needle string) bool {
	// local is like 0.0.0.0:8080 or [::]:8080 or 127.0.0.1:8080
	if strings.HasSuffix(local, needle) {
		// avoid matching :8080 against :80801
		idx := strings.LastIndex(local, ":")
		if idx < 0 {
			return false
		}
		p, err := strconv.Atoi(local[idx+1:])
		return err == nil && p == port
	}
	return false
}

func terminatePID(pid int) error {
	// Graceful attempt first (no tree kill API as portable as taskkill /T).
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid))
	_ = cmd.Run()

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		still, err := listenersHavePID(pid)
		if err != nil || !still {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	cmd = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	if out, err := cmd.CombinedOutput(); err != nil {
		// Exit code 128: process not found.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 128 {
			return nil
		}
		return fmt.Errorf("taskkill: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func listenersHavePID(pid int) (bool, error) {
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH")
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), strconv.Itoa(pid)), nil
}
