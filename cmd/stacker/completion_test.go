package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletionScriptsAreEmbedded(t *testing.T) {
	markers := map[string]string{
		"bash": "complete -F _stacker_complete stacker",
		// The #compdef line is what makes the file autoloadable from $fpath.
		"zsh":  "#compdef stacker",
		"fish": "complete -c stacker",
	}
	for shell, marker := range markers {
		out := captureStdout(t, func() {
			if code := cliCompletion([]string{shell}); code != 0 {
				t.Fatalf("completion %s exit = %d", shell, code)
			}
		})
		if !strings.Contains(out, marker) {
			t.Fatalf("%s script missing %q:\n%s", shell, marker, out)
		}
		if !strings.Contains(out, "__complete") {
			t.Fatalf("%s script never asks the binary for candidates", shell)
		}
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	if code := cliCompletion([]string{"tcsh"}); code != 2 {
		t.Fatalf("exit = %d, want 2 for an unsupported shell", code)
	}
	if code := cliCompletion(nil); code != 2 {
		t.Fatalf("exit = %d, want 2 with no shell named", code)
	}
}

func TestCompleteCommandsAndFlags(t *testing.T) {
	out := captureStdout(t, func() { cliComplete("", []string{"commands"}) })
	for _, want := range []string{"logs\t", "start\t", "completion\t"} {
		if !strings.Contains(out, want) {
			t.Fatalf("commands missing %q:\n%s", want, out)
		}
	}

	out = captureStdout(t, func() { cliComplete("", []string{"flags", "logs"}) })
	for _, want := range []string{"--supervisor", "--since", "--tail"} {
		if !strings.Contains(out, want) {
			t.Fatalf("logs flags missing %q:\n%s", want, out)
		}
	}
	for _, command := range []string{"start", "stop", "restart"} {
		actionFlags := captureStdout(t, func() { cliComplete("", []string{"flags", command}) })
		for _, want := range []string{"--group\t", "-g\t"} {
			if !strings.Contains(actionFlags, want) {
				t.Fatalf("%s flags missing %q:\n%s", command, want, actionFlags)
			}
		}
	}

	// Aliases share the flags of the command they stand for.
	alias := captureStdout(t, func() { cliComplete("", []string{"flags", "log"}) })
	if alias != out {
		t.Fatalf("alias `log` got different flags:\n%s", alias)
	}

	// An unknown command falls back to the global flags instead of nothing.
	out = captureStdout(t, func() { cliComplete("", []string{"flags", "nonsense"}) })
	if !strings.Contains(out, "--config") {
		t.Fatalf("unknown command should still offer global flags:\n%s", out)
	}

	out = captureStdout(t, func() { cliComplete("", []string{"shells"}) })
	for _, want := range []string{"bash", "fish", "zsh"} {
		if !strings.Contains(out, want) {
			t.Fatalf("shells missing %q:\n%s", want, out)
		}
	}
}

func TestCompleteProcessesFromLiveInstance(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	cfgPath, _ := startLogInstance(t)

	out := captureStdout(t, func() {
		if code := cliComplete(cfgPath, []string{"processes"}); code != 0 {
			t.Fatalf("exit = %d", code)
		}
	})
	if !strings.HasPrefix(out, "demo\t") {
		t.Fatalf("processes = %q, want the live process with a description", out)
	}
}

func TestCompleteGroupsFromLiveInstance(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	cfgPath := startCompletionInstance(t)

	out := captureStdout(t, func() {
		if code := cliComplete(cfgPath, []string{"groups"}); code != 0 {
			t.Fatalf("exit = %d", code)
		}
	})
	if want := "hub\noperations\nOther\n"; out != want {
		t.Fatalf("groups = %q, want %q", out, want)
	}
}

func startCompletionInstance(t *testing.T) string {
	t.Helper()
	cfgPath := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: "true"
    group: hub
  worker:
    command: "true"
  scheduler:
    command: "true"
    group: operations
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

// A TAB press must never print an error or hang, however broken the setup is.
func TestCompleteStaysSilentWithoutInstance(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	missing := filepath.Join(t.TempDir(), "stacker.yml")

	for _, args := range [][]string{
		{"processes"},
		{"groups"},
		{"tasks", "whatever"},
		{"tasks"},
		{"bogus-kind"},
		nil,
	} {
		out := captureStdout(t, func() {
			if code := cliComplete(missing, args); code != 0 {
				t.Fatalf("cliComplete(%v) exit = %d, want 0", args, code)
			}
		})
		if out != "" {
			t.Fatalf("cliComplete(%v) printed %q, want nothing", args, out)
		}
	}
}

func TestDescribeForCompletion(t *testing.T) {
	cases := []struct {
		name string
		in   ProcessInfo
		want string
	}{
		{"running with port", ProcessInfo{Status: "running", Port: 3001}, "running · :3001"},
		{"running with group", ProcessInfo{Status: "running", Group: "hub"}, "running · hub"},
		{"plain stopped", ProcessInfo{Status: "stopped"}, "stopped"},
		{
			// "disabled" alone reads like a choice; say why it cannot run.
			"disabled explains itself",
			ProcessInfo{Status: string(StatusDisabled)},
			"disabled · cwd unavailable",
		},
		{"task marker", ProcessInfo{Status: "stopped", OneShot: true}, "stopped · task"},
		{"orphan marker", ProcessInfo{Status: "running", Orphaned: true}, "running · removed from YAML"},
		{"unknown status", ProcessInfo{}, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeForCompletion(tc.in); got != tc.want {
				t.Fatalf("describe = %q, want %q", got, tc.want)
			}
		})
	}
}
