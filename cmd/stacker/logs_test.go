package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseByteSize(t *testing.T) {
	ok := map[string]int64{
		"1048576": 1 << 20,
		"256MB":   256 << 20,
		"256mb":   256 << 20,
		"512kb":   512 << 10,
		"1G":      1 << 30,
		"1GiB":    1 << 30,
		"1.5MB":   1536 << 10,
		"64 MB":   64 << 20,
		"0":       0,
		"":        0,
	}
	for in, want := range ok {
		got, err := parseByteSize(in)
		if err != nil {
			t.Fatalf("parseByteSize(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("parseByteSize(%q) = %d, want %d", in, got, want)
		}
	}
	for _, in := range []string{"abc", "-1", "-1MB", "MB", "1XB"} {
		if got, err := parseByteSize(in); err == nil {
			t.Fatalf("parseByteSize(%q) = %d, want an error", in, got)
		}
	}
}

func TestLoadConfigAcceptsMaxLogBytes(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
ui:
  max_log_bytes: 8MB
processes:
  app:
    command: "true"
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := int64(cfg.UI.MaxLogBytes); got != 8<<20 {
		t.Fatalf("max_log_bytes = %d, want %d", got, 8<<20)
	}
	m := newModel(cfg)
	if got := m.processes[0].maxLogBytes; got != 8<<20 {
		t.Fatalf("process budget = %d, want %d", got, 8<<20)
	}

	// A plain byte count is the same field.
	path = writeConfig(t, t.TempDir(), `
version: 1
ui:
  max_log_bytes: 1048576
processes:
  app:
    command: "true"
`)
	cfg, err = loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := int64(cfg.UI.MaxLogBytes); got != 1<<20 {
		t.Fatalf("max_log_bytes = %d, want %d", got, 1<<20)
	}
}

func TestLoadConfigDefaultsAndRejectsBadMaxLogBytes(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  app:
    command: "true"
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	m := newModel(cfg)
	if got := m.processes[0].maxLogBytes; got != defaultMaxLogBytes {
		t.Fatalf("default budget = %d, want %d (256MB)", got, defaultMaxLogBytes)
	}

	for name, contents := range map[string]string{
		"negative": `
version: 1
ui:
  max_log_bytes: -1
processes:
  app:
    command: "true"
`,
		"below the floor": `
version: 1
ui:
  max_log_bytes: 100
processes:
  app:
    command: "true"
`,
		"not a size": `
version: 1
ui:
  max_log_bytes: banana
processes:
  app:
    command: "true"
`,
	} {
		t.Run(name, func(t *testing.T) {
			p := writeConfig(t, t.TempDir(), contents)
			if _, err := loadConfig(p); err == nil {
				t.Fatal("expected the config to be rejected")
			}
		})
	}
}

func TestProcessLogRespectsByteBudget(t *testing.T) {
	const budget = int64(1024)
	p := NewProcess("app", ProcessConfig{}, 10_000)
	p.setLogLimits(0, budget)

	for i := 0; i < 500; i++ {
		p.appendLog(fmt.Sprintf("line-%04d", i))
	}

	logs := p.Logs()
	if len(logs) == 0 {
		t.Fatal("everything was trimmed")
	}
	if len(logs) >= 500 {
		t.Fatalf("byte budget never trimmed: %d lines retained", len(logs))
	}
	var total int64
	for _, line := range logs {
		total += logLineCost(line)
	}
	if total > budget {
		t.Fatalf("retained %d bytes, budget is %d", total, budget)
	}
	if got := logs[len(logs)-1]; got != "line-0499" {
		t.Fatalf("newest line = %q, want line-0499", got)
	}

	// Absolute indexes must stay coherent after trimming: the tail of the log
	// is what --since relies on.
	start, lines, next := p.TailLogs(0)
	if start != 500-len(logs) || len(lines) != len(logs) || next != 500 {
		t.Fatalf("TailLogs(0) = (%d, %d lines, %d), want (%d, %d, 500)",
			start, len(lines), next, 500-len(logs), len(logs))
	}
}

// A single line bigger than the whole budget is still worth showing; an empty
// log would help nobody.
func TestProcessKeepsNewestLineOverBudget(t *testing.T) {
	p := NewProcess("app", ProcessConfig{}, 10_000)
	p.setLogLimits(0, 128)
	p.appendLog("small")
	p.appendLog(strings.Repeat("x", 4096))

	logs := p.Logs()
	if len(logs) != 1 {
		t.Fatalf("retained %d lines, want only the oversized newest one", len(logs))
	}
	if len(logs[0]) != 4096 {
		t.Fatalf("retained the wrong line (%d bytes)", len(logs[0]))
	}
}

func TestParseLogsArgs(t *testing.T) {
	opts, err := parseLogsArgs([]string{"backend"}, false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.name != "backend" || opts.tail != defaultLogTail || opts.since != -1 || opts.follow {
		t.Fatalf("defaults wrong: %+v", opts)
	}

	opts, err = parseLogsArgs([]string{"-n", "10", "--since=42", "-f", "api"}, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.name != "api" || opts.tail != 10 || opts.since != 42 || !opts.follow || !opts.jsonOut {
		t.Fatalf("flags wrong: %+v", opts)
	}

	if opts, err := parseLogsArgs([]string{"--all", "api"}, false); err != nil || opts.tail != 0 {
		t.Fatalf("--all = %+v, %v", opts, err)
	}
	if opts, err := parseLogsArgs([]string{"--supervisor"}, false); err != nil || !opts.supervisor {
		t.Fatalf("--supervisor = %+v, %v", opts, err)
	}

	for _, args := range [][]string{
		{"--bogus"},
		{"-n"},
		{"-n", "abc"},
		{"--since", "-5"},
		{"one", "two"},
	} {
		if _, err := parseLogsArgs(args, false); err == nil {
			t.Fatalf("parseLogsArgs(%v) should have failed", args)
		}
	}
}

// startLogInstance brings up an isolated supervisor and returns its config path
// plus the process whose log the tests write into.
func startLogInstance(t *testing.T) (string, *Process) {
	t.Helper()
	cfgPath := writeConfig(t, t.TempDir(), `
version: 1
processes:
  demo:
    command: "true"
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
	return cfgPath, m.processes[0]
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

func TestCliLogsTailSinceAndJSON(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	cfgPath, p := startLogInstance(t)
	for i := 0; i < 10; i++ {
		p.appendLog(fmt.Sprintf("line-%d", i))
	}

	// Default tail prints everything when the log is shorter than the window.
	out := captureStdout(t, func() {
		if code := cliLogs(cfgPath, []string{"demo"}, false); code != 0 {
			t.Fatalf("cliLogs exit = %d", code)
		}
	})
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 10 {
		t.Fatalf("got %d lines, want 10:\n%s", len(lines), out)
	}

	// -n keeps only the end.
	out = captureStdout(t, func() {
		if code := cliLogs(cfgPath, []string{"demo", "-n", "3"}, false); code != 0 {
			t.Fatalf("cliLogs exit = %d", code)
		}
	})
	got := strings.Split(strings.TrimSpace(out), "\n")
	want := []string{"line-7", "line-8", "line-9"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tail = %v, want %v", got, want)
	}

	// --json carries the resume index, and --since replays only what is newer.
	out = captureStdout(t, func() {
		if code := cliLogs(cfgPath, []string{"demo", "--json"}, true); code != 0 {
			t.Fatalf("cliLogs exit = %d", code)
		}
	})
	var resp struct {
		OK      bool     `json:"ok"`
		Process string   `json:"process"`
		Status  string   `json:"status"`
		Next    int      `json:"next"`
		Lines   []string `json:"lines"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("json: %v (%s)", err, out)
	}
	if !resp.OK || resp.Process != "demo" || resp.Next != 10 || len(resp.Lines) != 10 {
		t.Fatalf("json response = %+v", resp)
	}
	if resp.Status == "" {
		t.Fatal("json response should carry the process status")
	}

	p.appendLog("after-resume")
	out = captureStdout(t, func() {
		if code := cliLogs(cfgPath, []string{"demo", "--since", fmt.Sprint(resp.Next)}, false); code != 0 {
			t.Fatalf("cliLogs exit = %d", code)
		}
	})
	if strings.TrimSpace(out) != "after-resume" {
		t.Fatalf("--since printed %q, want only the new line", out)
	}
}

func TestCliLogsRequiresProcessName(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	cfgPath, _ := startLogInstance(t)
	if code := cliLogs(cfgPath, nil, false); code != 2 {
		t.Fatalf("exit = %d, want 2 for a missing process name", code)
	}
	if code := cliLogs(cfgPath, []string{"nope"}, false); code == 0 {
		t.Fatal("an unknown process should not exit 0")
	}
}

func TestCliLogsSupervisor(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	cfgPath := writeConfig(t, t.TempDir(), `
version: 1
processes:
  demo:
    command: "true"
`)

	// No daemon log yet: say so instead of printing nothing.
	if code := cliLogs(cfgPath, []string{"--supervisor"}, false); code != 1 {
		t.Fatalf("exit = %d, want 1 when there is no daemon log", code)
	}

	logPath := daemonLogPath(cfgPath)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("notice: one\nnotice: two\nnotice: three\n"), 0o600); err != nil {
		t.Fatalf("write daemon log: %v", err)
	}

	out := captureStdout(t, func() {
		if code := cliLogs(cfgPath, []string{"--supervisor", "-n", "2"}, false); code != 0 {
			t.Fatalf("cliLogs --supervisor exit = %d", code)
		}
	})
	if strings.TrimSpace(out) != "notice: two\nnotice: three" {
		t.Fatalf("supervisor tail = %q", out)
	}

	// It reads the file directly, so it works with no supervisor running —
	// which is exactly when this log is the only one left.
	out = captureStdout(t, func() {
		if code := cliLogs(cfgPath, []string{"--supervisor", "--json"}, true); code != 0 {
			t.Fatalf("cliLogs --supervisor --json exit = %d", code)
		}
	})
	var resp struct {
		OK    bool     `json:"ok"`
		Path  string   `json:"path"`
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("json: %v (%s)", err, out)
	}
	if !resp.OK || resp.Path != logPath || len(resp.Lines) != 3 {
		t.Fatalf("supervisor json = %+v", resp)
	}

	if code := cliLogs(cfgPath, []string{"--supervisor", "demo"}, false); code != 2 {
		t.Fatal("--supervisor with a process name should be rejected")
	}
}

// The complaint that started this: nothing on the entry screens said how to
// reach a log, so both the picker and the plain listing must name the way in.
func TestPickerSurfacesLogs(t *testing.T) {
	m := newPickerModel(pickerRows(), "/srv/stacker/stacker.yml")
	m.width, m.height = 100, 30
	view := m.View()
	for _, want := range []string{"logs", "enter", "quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view missing %q:\n%s", want, view)
		}
	}
}

func TestPickerLogKeyEmitsCommands(t *testing.T) {
	m := newPickerModel(pickerRows(), "")
	m.rows[0].Sample = "api"
	m.width, m.height = 100, 30
	m.Update(key("l"))
	if m.action != pickerLogHints {
		t.Fatalf("action = %v, want pickerLogHints", m.action)
	}
	hints := formatLogHints(m.chosenRow)
	for _, want := range []string{"logs api", "--supervisor", m.chosenRow.State.Config} {
		if !strings.Contains(hints, want) {
			t.Fatalf("log hints missing %q:\n%s", want, hints)
		}
	}
}

func TestInstanceListShowsLogCommands(t *testing.T) {
	rows := pickerRows()
	rows[0].Sample = "api"
	out := formatInstanceList(rows, "")
	for _, want := range []string{"logs api", "logs --supervisor", "list --json"} {
		if !strings.Contains(out, want) {
			t.Fatalf("instance listing missing %q:\n%s", want, out)
		}
	}
}

func TestTailFileLinesWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.log")
	var b strings.Builder
	for i := 0; i < 200_000; i++ {
		fmt.Fprintf(&b, "line-%06d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	lines, err := tailFileLines(path, 3)
	if err != nil {
		t.Fatalf("tailFileLines: %v", err)
	}
	if len(lines) != 3 || lines[2] != "line-199999" {
		t.Fatalf("tail = %v", lines)
	}
}
