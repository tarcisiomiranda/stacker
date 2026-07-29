package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestApplyConfigDiffAddRemoveReorder(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  a:
    command: "true"
  b:
    command: "true"
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg)
	m.configPath = path
	if len(m.processes) != 2 || m.numProcesses != 2 {
		t.Fatalf("want 2 processes, got %d num=%d", len(m.processes), m.numProcesses)
	}

	// Reorder + add c (overwrite same path)
	if err := os.WriteFile(path, []byte(strings.TrimSpace(`
version: 1
processes:
  b:
    command: "true"
  c:
    command: "true"
    autostart: false
  a:
    command: "true"
`)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m.applyConfigDiff(cfg2)
	if m.numProcesses != 3 {
		t.Fatalf("want 3 processes, got %d", m.numProcesses)
	}
	names := []string{m.processes[0].Name, m.processes[1].Name, m.processes[2].Name}
	if names[0] != "b" || names[1] != "c" || names[2] != "a" {
		t.Fatalf("order = %v, want [b c a]", names)
	}

	// Remove c while stopped
	if err := os.WriteFile(path, []byte(strings.TrimSpace(`
version: 1
processes:
  b:
    command: "true"
  a:
    command: "true"
`)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg3, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m.applyConfigDiff(cfg3)
	if m.numProcesses != 2 {
		t.Fatalf("want 2 after remove, got %d", m.numProcesses)
	}
	if m.processes[0].Name != "b" || m.processes[1].Name != "a" {
		t.Fatalf("order after remove = %s %s", m.processes[0].Name, m.processes[1].Name)
	}
}

func TestApplyConfigDiffOrphanRunning(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  keep:
    command: "true"
  gone:
    command: "sleep 60"
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg)
	m.configPath = path

	gone := m.processByName("gone")
	if gone == nil {
		t.Fatal("missing gone")
	}
	if err := gone.Start(func() {}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return gone.Status() == StatusRunning })

	if err := os.WriteFile(path, []byte(strings.TrimSpace(`
version: 1
processes:
  keep:
    command: "true"
`)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m.applyConfigDiff(cfg2)

	if m.numProcesses != 1 {
		t.Fatalf("yaml processes want 1, got %d", m.numProcesses)
	}
	if len(m.processes) < 2 {
		t.Fatalf("want orphan kept in list, got %d entries", len(m.processes))
	}
	orphan := m.processByName("gone")
	if orphan == nil || !orphan.orphaned {
		t.Fatal("expected orphaned gone process")
	}

	_ = orphan.Stop(func() {})
	waitFor(t, 3*time.Second, func() bool {
		st := orphan.Status()
		return st == StatusStopped || st == StatusFailed
	})
	m.pruneOrphans()
	if m.processByName("gone") != nil {
		t.Fatal("orphan should be pruned after stop")
	}
	if len(m.processes) != 1 || m.processes[0].Name != "keep" {
		t.Fatalf("want only keep, got %v", processNames(m))
	}
}

func TestApplyConfigDiffAutostartNew(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  a:
    command: "true"
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg)

	if err := os.WriteFile(path, []byte(strings.TrimSpace(`
version: 1
processes:
  a:
    command: "true"
  sleeper:
    command: "sleep 30"
    autostart: true
`)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m.applyConfigDiff(cfg2)
	p := m.processByName("sleeper")
	if p == nil {
		t.Fatal("sleeper missing")
	}
	waitFor(t, 2*time.Second, func() bool { return p.Status() == StatusRunning })
	_ = p.Stop(func() {})
}

func TestApplyConfigDiffInvalidKeepsOld(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  a:
    command: "true"
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg)
	m.configPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m.setConfigHash(hashBytes(data))

	// Empty processes is rejected by loadConfig.
	if err := os.WriteFile(path, []byte("version: 1\nprocesses: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.tryReloadConfig()
	m.applyPendingConfig()
	if m.processByName("a") == nil {
		t.Fatal("invalid reload should not drop existing process")
	}
	if !strings.HasPrefix(m.statusText, "config reload failed") {
		t.Fatalf("want config reload failed status, got %q", m.statusText)
	}
}

func processNames(m *model) []string {
	out := make([]string, len(m.processes))
	for i, p := range m.processes {
		out[i] = p.Name
	}
	return out
}
