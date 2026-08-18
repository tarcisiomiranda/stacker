package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A cwd that does not exist disables only its own entry: a workspace where the
// user cloned some of the repos must still load.
func TestLoadConfigDisablesMissingWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	path := writeConfig(t, dir, `
version: 1
processes:
  present:
    command: "true"
    cwd: ./app
  absent:
    command: "true"
    cwd: ./missing
    autostart: true
tasks:
  deploy:
    command: "true"
    cwd: ./gone
`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if reason, bad := cfg.unavailable["present"]; bad {
		t.Fatalf("process with an existing cwd must stay enabled, got %q", reason)
	}
	for _, name := range []string{"absent", "deploy"} {
		if reason := cfg.unavailable[name]; !strings.Contains(reason, "does not exist") {
			t.Fatalf("%s reason = %q, want a missing-cwd reason", name, reason)
		}
	}

	m := newModel(cfg)
	byName := make(map[string]*Process, len(m.processes))
	for _, p := range m.processes {
		byName[p.Name] = p
	}
	for _, name := range []string{"absent", "deploy"} {
		if got := byName[name].Status(); got != StatusDisabled {
			t.Fatalf("%s status = %q, want %q", name, got, StatusDisabled)
		}
	}
	if got := byName["present"].Status(); got != StatusStopped {
		t.Fatalf("present status = %q, want %q", got, StatusStopped)
	}
	if logs := strings.Join(byName["absent"].Logs(), "\n"); !strings.Contains(logs, "disabled: cwd") {
		t.Fatalf("expected the reason in the log, got %#v", byName["absent"].Logs())
	}

	// A disabled entry never autostarts, however the YAML asks.
	m.Init()
	time.Sleep(50 * time.Millisecond)
	if got := byName["absent"].Status(); got != StatusDisabled {
		t.Fatalf("disabled process autostarted: status = %q", got)
	}

	notice := disabledNotice(cfg)
	if !strings.Contains(notice, "absent") || !strings.Contains(notice, "deploy") {
		t.Fatalf("notice = %q, want both disabled names", notice)
	}
}

func TestDisabledProcessRefusesToStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	p := NewProcess("app", ProcessConfig{
		Command: "true",
		Cwd:     missing,
		Port:    65123,
		Tasks:   map[string]string{"migrate": "true"},
	}, 50)
	p.Disable(cwdProblem(missing))

	if err := p.Start(func() {}); err == nil {
		t.Fatal("expected start to be refused")
	}
	if got := p.Status(); got != StatusDisabled {
		t.Fatalf("status = %q, want %q", got, StatusDisabled)
	}
	logs := strings.Join(p.Logs(), "\n")
	if !strings.Contains(logs, "refused to start") {
		t.Fatalf("logs = %q, want the refusal reason", logs)
	}
	// free-port must not run: it would terminate whatever holds a port this
	// process can never bind.
	if strings.Contains(logs, "port 65123") {
		t.Fatalf("free-port ran for a disabled process: %q", logs)
	}

	err := p.RunTask("migrate", func() {})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("RunTask error = %v, want a disabled error", err)
	}

	if kind := processStatusKind(StatusDisabled, 3); kind != "disabled" {
		t.Fatalf("status kind = %q, want disabled even with error lines", kind)
	}
}

// Cloning the missing repo is the fix, and it must not require restarting
// Stacker: Start re-checks the directory.
func TestDisabledProcessStartsAfterCwdAppears(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "late")
	p := NewProcess("app", ProcessConfig{Command: "true", Cwd: cwd, GracefulTimeout: "1s"}, 50)
	p.Disable(cwdProblem(cwd))

	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start after the directory appeared: %v", err)
	}
	if got := p.Unavailable(); got != "" {
		t.Fatalf("unavailable = %q, want cleared", got)
	}
	waitFor(t, 2*time.Second, func() bool {
		st := p.Status()
		return st == StatusRunning || st == StatusStopped
	})
}

func TestConfigReloadTogglesDisabledEntry(t *testing.T) {
	dir := t.TempDir()
	late := filepath.Join(dir, "late")
	path := writeConfig(t, dir, `
version: 1
processes:
  app:
    command: "true"
    cwd: ./late
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	m := newModel(cfg)
	m.configPath = path
	if got := m.processes[0].Status(); got != StatusDisabled {
		t.Fatalf("status = %q, want %q", got, StatusDisabled)
	}

	if err := os.Mkdir(late, 0o755); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	enabled, err := loadConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	m.applyConfigDiff(enabled)
	if got := m.processes[0].Status(); got != StatusStopped {
		t.Fatalf("status after the cwd appeared = %q, want %q", got, StatusStopped)
	}
	if got := m.processes[0].Unavailable(); got != "" {
		t.Fatalf("unavailable = %q, want cleared", got)
	}

	if err := os.Remove(late); err != nil {
		t.Fatalf("remove working directory: %v", err)
	}
	disabled, err := loadConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	m.applyConfigDiff(disabled)
	if got := m.processes[0].Status(); got != StatusDisabled {
		t.Fatalf("status after the cwd vanished = %q, want %q", got, StatusDisabled)
	}
}
