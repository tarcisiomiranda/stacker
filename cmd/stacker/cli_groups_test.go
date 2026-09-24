package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestCLIListGroupColumnUsesSectionDisplayOrder(t *testing.T) {
	configPath, _ := startCLIGroupInstance(t)
	code, stdout, stderr := captureCLI(t, configPath, "list")
	if code != 0 {
		t.Fatalf("list exit = %d, stderr = %q", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 6 {
		t.Fatalf("list lines = %q, want header and five entries", stdout)
	}
	if got := strings.Fields(lines[0]); strings.Join(got, " ") != "NAME STATUS PORT GROUP" {
		t.Fatalf("list header = %q", lines[0])
	}
	want := []string{"api", "worker", "db", "deploy", "plain"}
	for index, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 4 || fields[0] != want[index] {
			t.Fatalf("list row %d = %q, want process %q and four columns", index, line, want[index])
		}
		if fields[2] != "-" {
			t.Fatalf("missing port column = %q, want -", fields[2])
		}
	}
	if got := strings.Fields(lines[4]); got[3] != "release" {
		t.Fatalf("task group column = %q, want release", got[3])
	}
	if got := strings.Fields(lines[5]); got[3] != "-" {
		t.Fatalf("missing group column = %q, want -", got[3])
	}
}

func TestCLIListJSONIncludesGroup(t *testing.T) {
	configPath, _ := startCLIGroupInstance(t)
	code, stdout, stderr := captureCLI(t, configPath, "list", "--json")
	if code != 0 {
		t.Fatalf("list --json exit = %d, stderr = %q", code, stderr)
	}
	var response struct {
		Processes []ProcessInfo `json:"processes"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("decode list JSON: %v", err)
	}
	groups := make(map[string]string, len(response.Processes))
	for _, process := range response.Processes {
		groups[process.Name] = process.Group
	}
	if groups["api"] != "core" || groups["deploy"] != "release" {
		t.Fatalf("list JSON groups = %#v", groups)
	}
}

func TestCLIGroupActionParsesFlagsAndPreservesJSON(t *testing.T) {
	configPath, model := startCLIGroupInstance(t)

	code, stdout, stderr := captureCLI(t, configPath, "start", "api", "--group", "core")
	if code != 2 {
		t.Fatalf("group mode with positional name exit = %d, want 2; stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = captureCLI(t, configPath, "start", "--group", "core")
	if code != 0 {
		t.Fatalf("start --group exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "api") || !strings.Contains(stdout, "worker") {
		t.Fatalf("start --group output = %q, want affected process names", stdout)
	}

	code, stdout, stderr = captureCLI(t, configPath, "restart", "-g", "core")
	if code != 0 {
		t.Fatalf("restart -g exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "api") || !strings.Contains(stdout, "worker") {
		t.Fatalf("restart -g output = %q, want affected process names", stdout)
	}

	code, stdout, stderr = captureCLI(t, configPath, "stop", "--group=core", "--json")
	if code != 0 {
		t.Fatalf("stop --group= --json exit = %d, stderr = %q", code, stderr)
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("decode group action JSON: %v", err)
	}
	if len(response) != 4 || response["ok"] != true || response["group"] != "core" || response["action"] != "stop" {
		t.Fatalf("group action JSON = %#v", response)
	}
	affected, ok := response["affected"].([]any)
	if !ok || len(affected) != 2 || affected[0] != "api" || affected[1] != "worker" {
		t.Fatalf("affected JSON = %#v", response["affected"])
	}
	for _, name := range []string{"api", "worker"} {
		if status := model.processByName(name).Status(); status != StatusStopped {
			t.Fatalf("%s status = %q, want stopped", name, status)
		}
	}
}

func TestCLIGroupActionUnknownGroupListsKnownEffectiveGroups(t *testing.T) {
	configPath, _ := startCLIGroupInstance(t)
	code, _, stderr := captureCLI(t, configPath, "start", "--group", "missing")
	if code == 0 {
		t.Fatal("unknown group returned success")
	}
	for _, group := range []string{"core", "data", "release", "Other"} {
		if !strings.Contains(stderr, group) {
			t.Fatalf("unknown group stderr = %q, missing known group %q", stderr, group)
		}
	}
}

func TestCLIProcessActionStillRequiresExactlyOneName(t *testing.T) {
	configPath, model := startCLIGroupInstance(t)
	code, stdout, stderr := captureCLI(t, configPath, "start", "api")
	if code != 0 {
		t.Fatalf("start api exit = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
	if status := model.processByName("api").Status(); status != StatusRunning {
		t.Fatalf("api status = %q, want running", status)
	}

	code, _, _ = captureCLI(t, configPath, "stop", "api", "worker")
	if code != 2 {
		t.Fatalf("stop with two names exit = %d, want 2", code)
	}
	if status := model.processByName("api").Status(); status != StatusRunning {
		t.Fatalf("api status after invalid stop = %q, want running", status)
	}

	code, stdout, stderr = captureCLI(t, configPath, "stop", "api")
	if code != 0 {
		t.Fatalf("stop api exit = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func startCLIGroupInstance(t *testing.T) (string, *model) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	configPath := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: sleep 30
    group: core
  worker:
    command: sleep 30
    group: core
  db:
    command: sleep 30
    group: data
  plain:
    command: sleep 30
tasks:
  deploy:
    command: true
    group: release
`)
	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	model := newModel(cfg)
	model.mode = "serve"
	server, err := startControlServer(model, configPath)
	if err != nil {
		t.Fatalf("startControlServer: %v", err)
	}
	t.Cleanup(func() {
		model.stopAll()
		server.Close()
	})
	return configPath, model
}

func captureCLI(t *testing.T, configPath string, args ...string) (int, string, string) {
	t.Helper()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout, os.Stderr = stdoutWrite, stderrWrite
	stdoutDone := make(chan string, 1)
	stderrDone := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(stdoutRead)
		stdoutDone <- string(data)
	}()
	go func() {
		data, _ := io.ReadAll(stderrRead)
		stderrDone <- string(data)
	}()
	code := runCLI(configPath, true, args)
	_ = stdoutWrite.Close()
	_ = stderrWrite.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	return code, <-stdoutDone, <-stderrDone
}
