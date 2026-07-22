//go:build unix

package main

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

var ssUsersRe = regexp.MustCompile(`pid=(\d+)`)

func listenersOnPort(port int) ([]int, error) {
	if pids, err := listenersLsof(port); err == nil {
		return pids, nil
	}
	if pids, err := listenersSS(port); err == nil {
		return pids, nil
	}
	if pids, err := listenersFuser(port); err == nil {
		return pids, nil
	}
	// Nothing listening is success with empty set when tools report no match.
	if !portInUseProbe(port) {
		return nil, nil
	}
	return nil, fmt.Errorf("could not determine listeners on port %d (install lsof or ss)", port)
}

func listenersLsof(port int) ([]int, error) {
	cmd := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN", "-t")
	out, err := cmd.Output()
	if err != nil {
		// lsof exits 1 when no match.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	return parsePIDs(string(out)), nil
}

func listenersSS(port int) ([]int, error) {
	cmd := exec.Command("ss", "-lptn", fmt.Sprintf("sport = :%d", port))
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	matches := ssUsersRe.FindAllSubmatch(out, -1)
	if len(matches) == 0 {
		return nil, nil
	}
	var pids []int
	seen := map[int]struct{}{}
	for _, m := range matches {
		pid, err := strconv.Atoi(string(m[1]))
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

func listenersFuser(port int) ([]int, error) {
	cmd := exec.Command("fuser", fmt.Sprintf("%d/tcp", port))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// fuser prints PIDs on stdout/stderr depending on version.
	out, err := cmd.Output()
	combined := string(out) + stderr.String()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		// Still try to parse any PIDs that appeared.
		pids := parsePIDs(combined)
		if len(pids) > 0 {
			return pids, nil
		}
		return nil, err
	}
	return parsePIDs(combined), nil
}

// portInUseProbe checks whether anything accepts on the port. A plain TCP
// dial works on both Linux and macOS (the previous ss-based probe does not
// exist on macOS).
func portInUseProbe(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
	}
	return false
}

// terminatePID terminates the process and, when it lives in its own process
// group, the whole group — supervisor trees like npm→node or mise→uvicorn
// hold the port through children, so killing only the listener lets the
// supervisor respawn it. `kill(-pid)` alone (the previous behavior) misses
// this: the listener is usually a child, not the group leader, so the group
// signal failed with ESRCH and only the child died.
func terminatePID(pid int) error {
	target, group := killTarget(pid)

	signalTarget := func(sig syscall.Signal) error {
		if group {
			if err := syscall.Kill(-target, sig); err == nil {
				return nil
			}
		}
		if err := syscall.Kill(pid, sig); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}

	if err := signalTarget(syscall.SIGTERM); err != nil {
		return err
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return nil // gone
		}
		time.Sleep(50 * time.Millisecond)
	}

	return signalTarget(syscall.SIGKILL)
}

// killTarget resolves the real process group of pid. Falls back to the pid
// itself, and never targets Stacker's own group (that would kill the TUI).
func killTarget(pid int) (target int, group bool) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid <= 1 {
		return pid, false
	}
	if own, err := syscall.Getpgid(0); err == nil && pgid == own {
		return pid, false
	}
	return pgid, true
}
