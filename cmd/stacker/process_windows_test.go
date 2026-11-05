//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsShellPreservesQuotedExecutable(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "space directory")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(directory, "hello.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\necho quoted-path-ok\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	process := NewProcess("quoted", ProcessConfig{Command: `"` + script + `"`, Cwd: directory}, 20)
	if err := process.Start(func() {}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Stop(func() {}) })
	waitFor(t, 3*time.Second, func() bool { return process.Status() != StatusRunning })
	if process.Status() != StatusStopped || !strings.Contains(strings.Join(process.Logs(), "\n"), "quoted-path-ok") {
		t.Fatalf("quoted command failed: %v", process.Logs())
	}
}
