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
	cfg, rest := parseArgs([]string{"-config", "app.yml", "restart", "backend", "--json"})
	if cfg != "app.yml" {
		t.Fatalf("config: %q", cfg)
	}
	if len(rest) != 3 || rest[0] != "restart" || rest[1] != "backend" || rest[2] != "--json" {
		t.Fatalf("rest: %#v", rest)
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
