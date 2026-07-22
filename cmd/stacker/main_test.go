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

func TestLogViewWordWrapShowsFullLines(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"demo": {Command: "true"},
	}})
	m.width = 100
	m.height = 30
	m.wrap = true
	long := strings.Repeat("abc ", 60)
	m.current().appendLog(long)
	m.scrollToBottom()

	maxWidth := m.logWidth()
	view := m.logView()
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > maxWidth {
			t.Fatalf("wrapped line width %d exceeds panel width %d: %q", width, maxWidth, line)
		}
	}
	if strings.Contains(view, "…") {
		t.Fatal("wrap mode must not truncate lines")
	}
	joined := strings.ReplaceAll(strings.Join(strings.Split(view, "\n")[1:], ""), "\n", "")
	if !strings.Contains(strings.ReplaceAll(joined, " ", ""), strings.ReplaceAll(long, " ", "")) {
		t.Fatal("wrapped view lost log content")
	}
}

func TestVisualLinesShareLogicalIndexWhenWrapped(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"demo": {Command: "true"},
	}})
	m.wrap = true
	vis := m.visualLines([]string{"short", strings.Repeat("x", 25)}, 10)
	if len(vis) != 4 {
		t.Fatalf("expected 4 visual lines, got %d: %#v", len(vis), vis)
	}
	if vis[0].idx != 0 || vis[1].idx != 1 || vis[2].idx != 1 || vis[3].idx != 1 {
		t.Fatalf("unexpected logical indexes: %#v", vis)
	}
}

func TestUpdateConfigColorPreservesCommentsAndFields(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1

# UI tuning
ui:
  wheel_lines: 3

processes:
  # backend service
  api:
    command: echo ok
    graceful_timeout: 1s
  web:
    command: echo ok
    color: "#0af"
`)

	if err := updateConfigColor(path, "api", "#38bdf8"); err != nil {
		t.Fatalf("set color: %v", err)
	}
	if err := updateConfigColor(path, "web", ""); err != nil {
		t.Fatalf("remove color: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{"# UI tuning", "# backend service", `"#38bdf8"`, "\n\nprocesses:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in rewritten config:\n%s", want, text)
		}
	}
	if strings.Contains(text, "#0af") {
		t.Fatalf("expected web color removed:\n%s", text)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("rewritten config must still load: %v", err)
	}
	if got := cfg.Processes["api"].Color; got != "#38bdf8" {
		t.Fatalf("expected api color #38bdf8, got %q", got)
	}
	if got := cfg.Processes["web"].Color; got != "" {
		t.Fatalf("expected web color removed, got %q", got)
	}
}

func TestUpdateConfigColorUnknownProcess(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
`)
	if err := updateConfigColor(path, "missing", "#fff"); err == nil {
		t.Fatal("expected error for unknown process")
	}
}

func TestLoadConfigAcceptsWordWrap(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
ui:
  word_wrap: true
processes:
  api:
    command: echo ok
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.UI.WordWrap {
		t.Fatal("expected word_wrap to be true")
	}
	if m := newModel(cfg); !m.wrap {
		t.Fatal("expected model wrap to start enabled")
	}
}

func TestErrorDetectionCountsAndMarkClears(t *testing.T) {
	p := NewProcess("test", ProcessConfig{}, 100)
	p.detectErrors = true
	p.capture(strings.NewReader(
		"Traceback (most recent call last):\n"+
			`  File "app.py", line 1, in <module>`+"\n"+
			"ValueError: boom\n"+
			"normal line\n"), "stderr", func() {})

	if got := p.Errors(); got != 2 {
		t.Fatalf("expected 2 error lines, got %d; logs: %q", got, p.Logs())
	}
	p.Mark()
	if got := p.Errors(); got != 0 {
		t.Fatalf("expected mark to clear errors, got %d", got)
	}
}

func TestErrorDetectionDisabledByDefault(t *testing.T) {
	p := NewProcess("test", ProcessConfig{}, 100)
	p.capture(strings.NewReader("panic: boom\n"), "stderr", func() {})
	if got := p.Errors(); got != 0 {
		t.Fatalf("expected no detection when disabled, got %d", got)
	}
}

func TestErrorLinePatterns(t *testing.T) {
	match := []string{
		"Traceback (most recent call last):",
		"ValueError: invalid literal",
		"panic: runtime error: index out of range",
		"fatal error: all goroutines are asleep",
		"TypeError: Cannot read properties of undefined",
		"Error: connect ECONNREFUSED",
		"npm ERR! code ELIFECYCLE",
		"error[E0308]: mismatched types",
		"2026-07-22 10:00:00 ERROR something broke",
		"app.py:10: error: something",
		"java.lang.NullPointerException",
		"UnhandledPromiseRejection: oops",
	}
	noMatch := []string{
		"0 failed, 12 passed",
		"no errors here",
		"GET /api/users 200",
		"compiling without issue",
		"errors: 0",
	}
	for _, line := range match {
		if !errLineRe.MatchString(line) {
			t.Errorf("expected match: %q", line)
		}
	}
	for _, line := range noMatch {
		if errLineRe.MatchString(line) {
			t.Errorf("expected no match: %q", line)
		}
	}
}

func TestStartClearsErrorCount(t *testing.T) {
	p := NewProcess("test", ProcessConfig{Command: "true", Cwd: t.TempDir()}, 10)
	p.detectErrors = true
	p.errCount = 5
	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return p.Status() == StatusStopped })
	if got := p.Errors(); got != 0 {
		t.Fatalf("expected start to clear errors, got %d", got)
	}
}

func TestLoadConfigAcceptsHighlightErrors(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
ui:
  highlight_errors: true
processes:
  api:
    command: echo ok
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.UI.HighlightErrors {
		t.Fatal("expected highlight_errors to be true")
	}
	m := newModel(cfg)
	if !m.processByName("api").detectErrors {
		t.Fatal("expected process detectErrors enabled")
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
		"invalid color": `
version: 1
processes:
  app:
    command: true
    color: "#12345g"
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
