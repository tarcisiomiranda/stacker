package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestProcessKeepsOnlyConfiguredNumberOfLogs(t *testing.T) {
	p := NewProcess("test", ProcessConfig{}, 2)
	p.appendLog("one")
	p.appendLog("two")
	p.appendLog("three")

	logs := p.Logs()
	if len(logs) != 2 || logs[0] != "two" || logs[1] != "three" {
		t.Fatalf("unexpected retained logs: %#v", logs)
	}
}

func TestNewModelSelectsFirstAutostartProcess(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"backend": {Command: "true"},
		"demo":    {Command: "true", Autostart: true},
	}})

	if current := m.current(); current == nil || current.Name != "demo" {
		t.Fatalf("expected demo to be selected, got %#v", current)
	}
}

func TestProcessListRowsFitPanel(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"backend-with-a-long-name": {Command: "true"},
		"demo":                     {Command: "true", Autostart: true},
	}})
	m.width = 80

	maxWidth := m.leftWidth() - 5
	for _, line := range strings.Split(m.processList(), "\n") {
		if width := lipgloss.Width(line); width > maxWidth {
			t.Fatalf("line width %d exceeds panel content width %d: %q", width, maxWidth, line)
		}
	}
}

func TestCaptureSanitizesTerminalControlSequences(t *testing.T) {
	p := NewProcess("test", ProcessConfig{}, 10)
	p.capture(strings.NewReader("\x1b[31mINFO\x1b[0m\trequest\rrewrite\a\n"), "stderr", func() {})

	logs := p.Logs()
	if len(logs) != 1 {
		t.Fatalf("expected one log line, got %#v", logs)
	}
	if strings.ContainsAny(logs[0], "\x1b\t\r\a") {
		t.Fatalf("log still contains terminal control characters: %q", logs[0])
	}
	if logs[0] != "[stderr] INFO    request rewrite " {
		t.Fatalf("unexpected sanitized log: %q", logs[0])
	}
}

func TestLogViewLinesStayWithinPanelWidth(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"demo": {Command: "true"},
	}})
	m.width = 100
	m.height = 20
	for range 100 {
		m.current().appendLog(sanitizeLogLine("\x1b[34mINFO\x1b[0m\t" + strings.Repeat("界", 100)))
	}
	m.scrollToBottom()

	maxWidth := m.width - m.leftWidth() - 6
	lines := strings.Split(m.logView(), "\n")
	if len(lines) > m.logHeight() {
		t.Fatalf("log view height %d exceeds available height %d", len(lines), m.logHeight())
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > maxWidth {
			t.Fatalf("line width %d exceeds panel width %d: %q", width, maxWidth, line)
		}
	}
}

func TestStartFailureSetsFailedStatus(t *testing.T) {
	p := NewProcess("test", ProcessConfig{
		Command: "true",
		Cwd:     filepath.Join(t.TempDir(), "missing"),
	}, 10)

	err := p.Start(func() {})
	if err == nil {
		t.Fatal("expected start to fail")
	}
	if p.Status() != StatusFailed {
		t.Fatalf("expected failed status, got %q", p.Status())
	}
	if logs := strings.Join(p.Logs(), "\n"); !strings.Contains(logs, "start failed") {
		t.Fatalf("expected start failure in logs, got %q", logs)
	}
}

func TestStartInheritsUserPath(t *testing.T) {
	dir := t.TempDir()
	probePath := filepath.Join(dir, "stacker-path-probe")
	if err := os.WriteFile(probePath, []byte("#!/bin/sh\necho inherited-path\n"), 0o755); err != nil {
		t.Fatalf("write path probe: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := NewProcess("test", ProcessConfig{
		Command: "stacker-path-probe",
		Cwd:     dir,
	}, 10)
	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		status := p.Status()
		return status == StatusStopped || status == StatusFailed
	})

	if p.Status() != StatusStopped {
		t.Fatalf("expected stopped status, got %q; logs: %q", p.Status(), p.Logs())
	}
	if logs := strings.Join(p.Logs(), "\n"); !strings.Contains(logs, "inherited-path") {
		t.Fatalf("command was not found through inherited PATH; logs: %q", logs)
	}
}

func TestStopWaitsForProcessExit(t *testing.T) {
	p := NewProcess("test", ProcessConfig{
		Command:         "trap 'exit 0' TERM; while :; do sleep 1; done",
		Cwd:             t.TempDir(),
		GracefulTimeout: "2s",
	}, 20)

	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if err := p.Stop(func() {}); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if p.Status() != StatusStopped {
		t.Fatalf("expected stopped status, got %q", p.Status())
	}
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd != nil {
		t.Fatal("process command was not cleared after stop")
	}
}

func TestStopTerminatesChildProcessGroup(t *testing.T) {
	dir := t.TempDir()
	startedFile := filepath.Join(dir, "child.started")
	stoppedFile := filepath.Join(dir, "child.stopped")
	p := NewProcess("test", ProcessConfig{
		Command:         `sh -c 'trap "echo stopped > child.stopped; exit 0" TERM; echo started > child.started; while :; do sleep 1; done' & wait`,
		Cwd:             dir,
		GracefulTimeout: "2s",
	}, 20)

	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, err := os.Stat(startedFile)
		return err == nil
	})

	if err := p.Stop(func() {}); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, err := os.Stat(stoppedFile)
		return err == nil
	})
}

func TestLoadConfigResolvesRelativeWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	workingDir := filepath.Join(dir, "app")
	if err := os.Mkdir(workingDir, 0o755); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	path := writeConfig(t, dir, `
version: 1
ui:
  wheel_lines: 3
  max_log_lines: 100
processes:
  app:
    command: echo ok
    cwd: ./app
    graceful_timeout: 1s
`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Processes["app"].Cwd; got != workingDir {
		t.Fatalf("expected cwd %q, got %q", workingDir, got)
	}
}

func TestLoadConfigRejectsInvalidInput(t *testing.T) {
	tests := map[string]string{
		"unknown field": `
version: 1
unexpected: true
processes:
  app:
    command: true
`,
		"unsupported version": `
version: 2
processes:
  app:
    command: true
`,
		"empty command": `
version: 1
processes:
  app:
    command: ""
`,
		"invalid timeout": `
version: 1
processes:
  app:
    command: true
    graceful_timeout: later
`,
		"missing working directory": `
version: 1
processes:
  app:
    command: true
    cwd: ./missing
`,
		"multiple documents": `
version: 1
processes:
  app:
    command: true
---
version: 1
processes:
  other:
    command: true
`,
	}

	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), contents)
			if _, err := loadConfig(path); err == nil {
				t.Fatal("expected config to be rejected")
			}
		})
	}
}

func writeConfig(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "stacker.yml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
