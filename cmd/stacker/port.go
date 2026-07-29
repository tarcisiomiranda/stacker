package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
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
			// Still in use but we could not map a PID — do not pretend success.
			return killed, err
		}
		pids = filterSelf(pids, self)
		if len(pids) == 0 {
			// Double-check: tools said free; probe may still see a race.
			if !portInUseProbe(port) {
				return killed, nil
			}
			// Port still answers — wait a moment and re-list.
			waitPortReleased(port, self, 500*time.Millisecond)
			continue
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

	if !portInUseProbe(port) {
		return killed, nil
	}
	remaining, err := listenersOnPort(port)
	if err != nil {
		return killed, err
	}
	if remaining = filterSelf(remaining, self); len(remaining) > 0 {
		return killed, fmt.Errorf("port %d is still in use by pids %v (a supervisor may keep restarting the listener)", port, remaining)
	}
	return killed, fmt.Errorf("port %d is still in use after freeing pids %v", port, killed)
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

// portInUseProbe checks whether anything accepts on the port. A plain TCP
// dial/bind works on Linux, macOS, and Windows. We try several addresses
// because a process may listen only on 0.0.0.0, ::, or a loopback interface.
func portInUseProbe(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
	}
	for _, network := range []string{"tcp4", "tcp6"} {
		ln, err := net.Listen(network, net.JoinHostPort("", strconv.Itoa(port)))
		if err == nil {
			ln.Close()
			continue
		}
		if isAddrInUse(err) {
			return true
		}
	}
	return false
}

func isAddrInUse(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// Linux/macOS: "address already in use"
	// Windows: "Only one usage of each socket address..."
	return strings.Contains(s, "address already in use") ||
		strings.Contains(s, "Only one usage of each socket address")
}
