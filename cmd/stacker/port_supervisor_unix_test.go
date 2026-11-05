//go:build unix

package main

import (
	"fmt"
	"net"
	"os/exec"
	"testing"
	"time"
)

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
