package main

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFreePortKillsListener(t *testing.T) {
	cmdPort, stop := startPortHolder(t)
	defer stop()

	killed, err := freePort(cmdPort)
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	if len(killed) == 0 {
		t.Fatal("expected at least one killed pid")
	}

	waitFor(t, 2*time.Second, func() bool {
		pids, err := listenersOnPort(cmdPort)
		return err == nil && len(pids) == 0
	})
}

// TestFreePortKillsSupervisorTree reproduces the mise/npm case: the listener
// is a child of a supervisor shell in its own process group. freePort must
// take down the whole group, or the supervisor would respawn the listener.
func TestFreePortKillsSupervisorTree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	script := fmt.Sprintf(
		`python3 -c 'import socket,time;s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(("127.0.0.1",%d));s.listen(1);time.sleep(60)' & wait`,
		port,
	)
	cmd := exec.Command("sh", "-c", script)
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start supervisor tree: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() {
		_ = signalProcessGroup(cmd.Process.Pid, signalKill)
		<-done
	}()

	waitFor(t, 3*time.Second, func() bool {
		pids, err := listenersOnPort(port)
		return err == nil && len(pids) > 0
	})

	killed, err := freePort(port)
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	if len(killed) == 0 {
		t.Fatal("expected at least one killed pid")
	}

	// The supervisor shell must die with its group, not linger and respawn.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor shell still alive after freePort")
	}
	waitFor(t, 2*time.Second, func() bool {
		pids, err := listenersOnPort(port)
		return err == nil && len(pids) == 0
	})
}

func TestFreePortNothingListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	killed, err := freePort(port)
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	if len(killed) != 0 {
		t.Fatalf("expected no kills, got %v", killed)
	}
}

func TestListenersOnPortDoesNotTrustEmptyLsof(t *testing.T) {
	// Regression: empty/failed lsof must not hide a live listener.
	port, stop := startPortHolder(t)
	defer stop()

	pids, err := listenersOnPort(port)
	if err != nil {
		t.Fatalf("listenersOnPort: %v", err)
	}
	if len(pids) == 0 {
		t.Fatal("expected at least one listener pid")
	}
	if !portInUseProbe(port) {
		t.Fatal("probe should report port in use")
	}
}

func TestParsePIDs(t *testing.T) {
	pids := parsePIDs("123\n456 123\n")
	if len(pids) != 2 || pids[0] != 123 || pids[1] != 456 {
		t.Fatalf("unexpected pids: %v", pids)
	}
}

func TestLoadConfigRejectsInvalidPort(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  app:
    command: true
    port: 99999
`)
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected invalid port to be rejected")
	}
}

func TestParseArgs(t *testing.T) {
	cfg, explicit, rest := parseArgs([]string{"-config", "app.yml", "restart", "backend", "--json"})
	if cfg != "app.yml" || !explicit {
		t.Fatalf("config: %q explicit: %v", cfg, explicit)
	}
	if len(rest) != 3 || rest[0] != "restart" || rest[1] != "backend" || rest[2] != "--json" {
		t.Fatalf("rest: %#v", rest)
	}

	cfg2, explicit2, rest2 := parseArgs([]string{"--config", "./stacker.yml", "a"})
	if cfg2 != "./stacker.yml" || !explicit2 {
		t.Fatalf("--config path: %q explicit: %v", cfg2, explicit2)
	}
	if len(rest2) != 1 || rest2[0] != "a" {
		t.Fatalf("rest for attach alias: %#v", rest2)
	}

	cfg3, explicit3, rest3 := parseArgs([]string{"--config=proj.yml", "attach"})
	if cfg3 != "proj.yml" || !explicit3 {
		t.Fatalf("--config= path: %q explicit: %v", cfg3, explicit3)
	}
	if len(rest3) != 1 || rest3[0] != "attach" {
		t.Fatalf("rest for attach: %#v", rest3)
	}
}

// Honouring an explicit --config requires knowing it was passed at all: the
// default value is the same string a user types, so without this flag `stacker
// --config stacker.yml` was indistinguishable from bare `stacker` and silently
// attached to an unrelated project.
func TestParseArgsReportsWhetherConfigWasExplicit(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantPath string
		wantSet  bool
	}{
		{"no args", nil, "stacker.yml", false},
		{"subcommand only", []string{"list"}, "stacker.yml", false},
		{"json flag only", []string{"list", "--json"}, "stacker.yml", false},
		{"single dash separate", []string{"-config", "a.yml"}, "a.yml", true},
		{"double dash separate", []string{"--config", "a.yml"}, "a.yml", true},
		{"single dash equals", []string{"-config=a.yml"}, "a.yml", true},
		{"double dash equals", []string{"--config=a.yml"}, "a.yml", true},
		// Typing the default path by hand is still an explicit choice.
		{"explicit default name", []string{"--config", "stacker.yml"}, "stacker.yml", true},
		// A dangling flag with no value must not claim to be explicit.
		{"dangling flag", []string{"--config"}, "stacker.yml", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, explicit, _ := parseArgs(tc.args)
			if path != tc.wantPath || explicit != tc.wantSet {
				t.Fatalf("parseArgs(%#v) = (%q, %v), want (%q, %v)",
					tc.args, path, explicit, tc.wantPath, tc.wantSet)
			}
		})
	}
}

func TestStartFreesConfiguredPort(t *testing.T) {
	port, stop := startPortHolder(t)
	defer stop()

	dir := t.TempDir()
	p := NewProcess("api", ProcessConfig{
		Command: "true",
		Cwd:     dir,
		Port:    port,
	}, 20)
	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		status := p.Status()
		return status == StatusStopped || status == StatusFailed
	})

	logs := strings.Join(p.Logs(), "\n")
	if !strings.Contains(logs, "freed port "+strconv.Itoa(port)) {
		t.Fatalf("expected free-port log, got %q", logs)
	}
	if p.Status() == StatusFailed {
		t.Fatalf("start failed after free-port; logs=%q", logs)
	}
}

// startPortHolder runs a child process that listens on an ephemeral TCP port.
func startPortHolder(t *testing.T) (port int, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port = ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	script := fmt.Sprintf(
		`import socket,time;s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(("127.0.0.1",%d));s.listen(1);time.sleep(60)`,
		port,
	)
	cmd := exec.Command("python3", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start port holder: %v", err)
	}
	stop = func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}

	waitFor(t, 3*time.Second, func() bool {
		pids, err := listenersOnPort(port)
		return err == nil && len(pids) > 0
	})
	return port, stop
}
