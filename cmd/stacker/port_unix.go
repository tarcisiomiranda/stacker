//go:build unix

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var ssUsersRe = regexp.MustCompile(`pid=(\d+)`)

// listenersOnPort finds PIDs with a TCP LISTEN socket on port.
//
// Important: an empty result from lsof/ss is NOT definitive — those tools can
// miss sockets (permissions, network namespaces, older builds). We try every
// method and only trust "nothing listening" when a connect probe also fails.
func listenersOnPort(port int) ([]int, error) {
	seen := map[int]struct{}{}
	var pids []int
	add := func(list []int) {
		for _, pid := range list {
			if pid <= 0 {
				continue
			}
			if _, ok := seen[pid]; ok {
				continue
			}
			seen[pid] = struct{}{}
			pids = append(pids, pid)
		}
	}

	var toolErrs []string
	for _, step := range []struct {
		name string
		fn   func(int) ([]int, error)
	}{
		{"lsof", listenersLsof},
		{"ss", listenersSS},
		{"fuser", listenersFuser},
		{"procfs", listenersProcfs},
	} {
		list, err := step.fn(port)
		if err != nil {
			toolErrs = append(toolErrs, step.name+": "+err.Error())
			continue
		}
		add(list)
	}

	if len(pids) > 0 {
		return pids, nil
	}

	// No PIDs from any tool. Confirm the port is actually free.
	if !portInUseProbe(port) {
		return nil, nil
	}

	// Port answers TCP but we found no owner — report a useful error so
	// freePort / Start surface it instead of silently starting and failing.
	detail := "no listener pids found"
	if len(toolErrs) > 0 {
		detail = strings.Join(toolErrs, "; ")
	}
	return nil, fmt.Errorf("port %d is in use but could not identify the process (%s)", port, detail)
}

func listenersLsof(port int) ([]int, error) {
	if _, err := exec.LookPath("lsof"); err != nil {
		return nil, err
	}
	cmd := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN", "-t")
	out, err := cmd.Output()
	if err != nil {
		// lsof exits 1 when no match — empty is fine, caller will probe.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	return parsePIDs(string(out)), nil
}

func listenersSS(port int) ([]int, error) {
	if _, err := exec.LookPath("ss"); err != nil {
		return nil, err
	}
	// Prefer a filtered query; fall back to full listen table + parse.
	for _, args := range [][]string{
		{"-lptn", fmt.Sprintf("sport = :%d", port)},
		{"-lptnH", fmt.Sprintf("( sport = :%d )", port)},
		{"-lptn"},
	} {
		cmd := exec.Command("ss", args...)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		pids := parseSSOutput(out, port, len(args) == 1 /* full table needs port filter */)
		if len(pids) > 0 {
			return pids, nil
		}
		// Filtered query with no pids but may still have a LISTEN line without
		// users= (permissions). Treat as empty and let other tools try.
		if len(args) > 1 {
			return nil, nil
		}
	}
	return nil, nil
}

func parseSSOutput(out []byte, port int, filterByPort bool) []int {
	// users:(("name",pid=123,fd=4))
	if !filterByPort {
		matches := ssUsersRe.FindAllSubmatch(out, -1)
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
		return pids
	}

	// Full table: only take lines that include our port in the local address.
	portSuffix := fmt.Sprintf(":%d", port)
	var pids []int
	seen := map[int]struct{}{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "LISTEN") && !strings.Contains(strings.ToLower(line), "listen") {
			// ss -H may omit the state word depending on version; also match :port.
			if !strings.Contains(line, portSuffix) {
				continue
			}
		}
		if !lineHasLocalPort(line, port) {
			continue
		}
		for _, m := range ssUsersRe.FindAllStringSubmatch(line, -1) {
			pid, err := strconv.Atoi(m[1])
			if err != nil || pid <= 0 {
				continue
			}
			if _, ok := seen[pid]; ok {
				continue
			}
			seen[pid] = struct{}{}
			pids = append(pids, pid)
		}
	}
	return pids
}

func lineHasLocalPort(line string, port int) bool {
	// Match :PORT as a local port, not a PID or random number.
	// Examples: 0.0.0.0:8014  *:8014  [::]:8014  127.0.0.1:8014
	needle := fmt.Sprintf(":%d", port)
	idx := strings.Index(line, needle)
	for idx >= 0 {
		end := idx + len(needle)
		// Next char should be end/space if it's the port field.
		if end >= len(line) || line[end] == ' ' || line[end] == '\t' {
			return true
		}
		// Also allow trailing comma in some formats.
		if line[end] == ',' {
			return true
		}
		next := strings.Index(line[end:], needle)
		if next < 0 {
			return false
		}
		idx = end + next
	}
	return false
}

func listenersFuser(port int) ([]int, error) {
	if _, err := exec.LookPath("fuser"); err != nil {
		return nil, err
	}
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
		pids := parsePIDs(combined)
		if len(pids) > 0 {
			return pids, nil
		}
		return nil, err
	}
	return parsePIDs(combined), nil
}

// listenersProcfs walks /proc/net/tcp{,6} for LISTEN sockets on port, then
// resolves the socket inode to a PID via /proc/*/fd. Pure Go — no lsof/ss.
func listenersProcfs(port int) ([]int, error) {
	inodes := map[uint64]struct{}{}
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if err := collectListenInodes(path, port, inodes); err != nil {
			// Missing file (e.g. no IPv6) is fine.
			if !os.IsNotExist(err) {
				return nil, err
			}
		}
	}
	if len(inodes) == 0 {
		return nil, nil
	}

	seen := map[int]struct{}{}
	var pids []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(ent.Name())
		if err != nil || pid <= 0 {
			continue
		}
		fdDir := filepath.Join("/proc", ent.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // permission or raced exit
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			// socket:[12345]
			if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
				continue
			}
			inoStr := target[len("socket:[") : len(target)-1]
			ino, err := strconv.ParseUint(inoStr, 10, 64)
			if err != nil {
				continue
			}
			if _, ok := inodes[ino]; !ok {
				continue
			}
			if _, ok := seen[pid]; ok {
				continue
			}
			seen[pid] = struct{}{}
			pids = append(pids, pid)
			break
		}
	}
	return pids, nil
}

// collectListenInodes parses a /proc/net/tcp{,6} table for LISTEN entries on port.
// Line format: sl local_address rem_address st ... inode
// local_address is hex IP:port (port is last 4 hex digits, big-endian).
// st 0A = LISTEN.
func collectListenInodes(path string, port int, inodes map[uint64]struct{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	wantPort := fmt.Sprintf("%04X", port)
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if i == 0 {
			continue // header
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		// fields[1] = local_address, fields[3] = st, fields[9] = inode
		local := fields[1]
		st := fields[3]
		if !strings.EqualFold(st, "0A") {
			continue
		}
		colon := strings.LastIndex(local, ":")
		if colon < 0 || colon+1 >= len(local) {
			continue
		}
		if !strings.EqualFold(local[colon+1:], wantPort) {
			continue
		}
		ino, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || ino == 0 {
			continue
		}
		inodes[ino] = struct{}{}
	}
	return nil
}

// terminatePID terminates the process and, when it lives in its own process
// group, the whole group — supervisor trees like npm→node or mise→uvicorn
// hold the port through children, so killing only the listener lets the
// supervisor respawn it.
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
