package main

import (
	"fmt"
	"os"
	"time"
)

// freePort terminates processes listening on the given TCP port.
// Returns the PIDs that were signaled. Safe to call when nothing holds the port.
func freePort(port int) ([]int, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d (must be 1-65535)", port)
	}

	pids, err := listenersOnPort(port)
	if err != nil {
		return nil, err
	}

	self := os.Getpid()
	var killed []int
	for _, pid := range pids {
		if pid <= 0 || pid == self {
			continue
		}
		if err := terminatePID(pid); err != nil {
			return killed, fmt.Errorf("failed to terminate pid %d on port %d: %w", pid, port, err)
		}
		killed = append(killed, pid)
	}

	// Brief wait so the kernel releases the bind before a restart.
	if len(killed) > 0 {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			remaining, err := listenersOnPort(port)
			if err != nil || len(filterSelf(remaining, self)) == 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return killed, nil
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
