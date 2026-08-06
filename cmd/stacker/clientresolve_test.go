package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// startTestInstance brings up a supervisor whose state lives in an isolated
// runtime dir, so instance discovery in tests never sees the developer's own
// running Stacker.
func startTestInstance(t *testing.T, dir string) string {
	t.Helper()
	cfgPath := writeConfig(t, dir, `
version: 1
processes:
  demo:
    command: true
`)
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	m := newModel(cfg)
	m.mode = "serve"
	cs, err := startControlServer(m, cfgPath)
	if err != nil {
		t.Fatalf("startControlServer: %v", err)
	}
	t.Cleanup(cs.Close)
	return cfgPath
}

// Read-only commands outside a project directory may adopt the single live
// instance: there is no local config to prefer, and reading cannot break it.
func TestControlClientFallsBackForMissingConfig(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	startTestInstance(t, t.TempDir())

	absent := filepath.Join(t.TempDir(), "not-here.yml")
	client, st, err := newControlClientFor(absent, true)
	if err != nil {
		t.Fatalf("read-only fallback should work for a missing config: %v", err)
	}
	if client == nil || st == nil {
		t.Fatal("expected a client and state")
	}
}

// This is the reported bug: a config that exists in the working directory must
// never lose to an unrelated instance, not even for a read.
func TestControlClientRefusesFallbackWhenConfigExists(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	startTestInstance(t, t.TempDir())

	// A real, on-disk config that simply has no supervisor of its own.
	localDir := t.TempDir()
	local := writeConfig(t, localDir, `
version: 1
processes:
  mine:
    command: true
`)

	if _, _, err := newControlClientFor(local, true); err == nil {
		t.Fatal("a config present on disk must not fall back to another instance")
	} else if !strings.Contains(err.Error(), "no running Stacker") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Acting on an instance the user did not name cannot be undone by reading the
// output afterwards, so mutations never fall back at all.
func TestControlClientNeverFallsBackForMutations(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	startTestInstance(t, t.TempDir())

	absent := filepath.Join(t.TempDir(), "not-here.yml")
	if _, _, err := newControlClientFor(absent, false); err == nil {
		t.Fatal("state-changing commands must refuse to adopt another instance")
	}
}

// The old text promised that a bare `stacker list` would pick up the only
// running instance. That is no longer true once the named config exists on disk,
// and a hint contradicting the behaviour is worse than no hint at all.
func TestErrNoRunningInstanceDoesNotPromiseFallback(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	startTestInstance(t, t.TempDir())

	msg := errNoRunningInstance("stacker.yml").Error()
	if !strings.Contains(msg, "Other running instance") {
		t.Fatalf("the live instance should be listed:\n%s", msg)
	}
	for _, forbidden := range []string{
		"should pick it up",
		"if only one is running",
	} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("message still promises the removed fallback (%q):\n%s", forbidden, msg)
		}
	}
	// It must point at the commands that do work.
	if !strings.Contains(msg, "stacker instances") {
		t.Fatalf("message should point at `stacker instances`:\n%s", msg)
	}
}

// The exact-match path is unaffected: naming a running config always works.
func TestControlClientUsesExactMatch(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	cfgPath := startTestInstance(t, t.TempDir())

	for _, allowFallback := range []bool{false, true} {
		client, st, err := newControlClientFor(cfgPath, allowFallback)
		if err != nil {
			t.Fatalf("exact match failed (allowFallback=%v): %v", allowFallback, err)
		}
		if client == nil || st == nil {
			t.Fatalf("expected client and state (allowFallback=%v)", allowFallback)
		}
	}
}
