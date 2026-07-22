package main

import (
	"fmt"
	"os"
	"time"
)

// freePort terminates processes listening on the given TCP port.
// Returns the PIDs that were signaled. Safe to call when nothing holds the port.
//
// It runs up to three rounds because supervisors (nodemon, npm, air, watchdog
// reloaders) often respawn the listener under a new PID: each round kills the
// current holders and re-checks. If the port is still held at the end, that is
// reported as an error instead of a silent partial success.
func freePort(port int) ([]int, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d (must be 1-65535)", port)
	}

	self := os.Getpid()
	seen := map[int]struct{}{}
	var killed []int
	for round := 0; round < 3; round++ {
		pids, err := listenersOnPort(port)
		if err != nil {
			return killed, err
		}
		pids = filterSelf(pids, self)
		if len(pids) == 0 {
			return killed, nil
		}
		for _, pid := range pids {
			if err := terminatePID(pid); err != nil {
				return killed, fmt.Errorf("failed to terminate pid %d on port %d: %w", pid, port, err)
			}
			if _, dup := seen[pid]; !dup {
				seen[pid] = struct{}{}
				killed = append(killed, pid)
			}
		}
		// Brief wait so the kernel releases the bind before re-checking.
		waitPortReleased(port, self, 2*time.Second)
	}

	remaining, err := listenersOnPort(port)
	if err != nil {
		return killed, err
	}
	if remaining = filterSelf(remaining, self); len(remaining) > 0 {
		return killed, fmt.Errorf("port %d is still in use by pids %v (a supervisor may keep restarting the listener)", port, remaining)
	}
	return killed, nil
}

func waitPortReleased(port, self int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		remaining, err := listenersOnPort(port)
		if err != nil || len(filterSelf(remaining, self)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func filterSelf(pids []int, self int) []int {
	out := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid > 0 && pid != self {
			out = append(out, pid)
		}
	}
	return out
}

func parsePIDs(text string) []int {
	seen := map[int]struct{}{}
	var pids []int
	for _, field := range splitFields(text) {
		pid, err := parseInt(field)
		if err != nil || pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}
	return pids
}

func splitFields(text string) []string {
	var fields []string
	start := -1
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' {
			if start >= 0 {
				fields = append(fields, text[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		fields = append(fields, text[start:])
	}
	return fields
}

func parseInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not an int")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
