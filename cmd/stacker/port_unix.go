//go:build unix

package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
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

func portInUseProbe(port int) bool {
	// Best-effort: if ss lists the port as listening without pid info.
	cmd := exec.Command("ss", "-ltn", fmt.Sprintf("sport = :%d", port))
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), fmt.Sprintf(":%d", port))
}

func terminatePID(pid int) error {
	// Prefer process-group kill so children release the port too.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
			return err
		}
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return nil // gone
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
	}
	return nil
}
