package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The victim's log is the only place this is visible from the other side: with
// no note, a service just dies and its log shows nothing but the exit.
func TestControlPlaneNoteAppendsToLog(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := t.TempDir()
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
	cs, err := startControlServer(m, cfgPath)
	if err != nil {
		t.Fatalf("startControlServer: %v", err)
	}
	defer cs.Close()

	st, err := findRunningInstance(cfgPath)
	if err != nil || st == nil {
		t.Fatalf("findRunningInstance: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Post("http://"+st.Addr+"/v1/processes/demo/note", "application/json",
		strings.NewReader(`{"text": "port 8080 reclaimed by /srv/other/stacker.yml"}`))
	if err != nil {
		t.Fatalf("note: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("note status %d", resp.StatusCode)
	}

	logs := strings.Join(m.processes[0].Logs(), "\n")
	if !strings.Contains(logs, "port 8080 reclaimed by /srv/other/stacker.yml") {
		t.Fatalf("note not appended to the log:\n%s", logs)
	}
	if !strings.Contains(logs, "[stacker]") {
		t.Fatalf("note should be marked as coming from stacker:\n%s", logs)
	}
}

func TestControlPlaneNoteRejectsEmptyText(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, `
version: 1
processes:
  demo:
    command: true
`)
	cfg, _ := loadConfig(cfgPath)
	m := newModel(cfg)
	cs, err := startControlServer(m, cfgPath)
	if err != nil {
		t.Fatalf("startControlServer: %v", err)
	}
	defer cs.Close()
	st, _ := findRunningInstance(cfgPath)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post("http://"+st.Addr+"/v1/processes/demo/note", "application/json",
		strings.NewReader(`{"text": "   "}`))
	if err != nil {
		t.Fatalf("note: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty note status %d, want 400", resp.StatusCode)
	}
}

// Before free-port terminates a listener, Stacker has to know whether it belongs
// to another instance — that is the difference between killing a stray npm and
// killing a colleague project's service.
func TestFindPortOwnerAcrossInstances(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	victimDir := t.TempDir()
	victimCfg := writeConfig(t, victimDir, `
version: 1
processes:
  frontend:
    command: "trap 'exit 0' TERM; while :; do sleep 1; done"
    port: 8080
    graceful_timeout: 2s
`)
	cfg, err := loadConfig(victimCfg)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	victim := newModel(cfg)
	victim.mode = "serve"
	cs, err := startControlServer(victim, victimCfg)
	if err != nil {
		t.Fatalf("startControlServer: %v", err)
	}
	defer cs.Close()

	// A stopped process does not own anything yet.
	if claim := findPortOwner(8080, "/srv/mine/stacker.yml"); claim != nil {
		t.Fatalf("stopped process should not own the port, got %+v", claim)
	}

	if err := victim.processes[0].Start(victim.notify); err != nil {
		t.Fatalf("start victim: %v", err)
	}
	defer func() { _ = victim.processes[0].Stop(victim.notify) }()
	waitFor(t, 3*time.Second, func() bool { return victim.processes[0].Status() == StatusRunning })

	claim := findPortOwner(8080, "/srv/mine/stacker.yml")
	if claim == nil {
		t.Fatal("a running process on 8080 in another instance must be found")
	}
	if claim.Process != "frontend" {
		t.Fatalf("claim.Process = %q, want frontend", claim.Process)
	}
	if claim.PID <= 0 {
		t.Fatalf("claim.PID = %d, want a real pid", claim.PID)
	}
	if !strings.Contains(claim.Config, "stacker.yml") || claim.Label == "" {
		t.Fatalf("claim should identify the instance: %+v", claim)
	}

	// Asking as the owning instance itself finds nothing: Stacker never warns
	// about its own processes, free-port has always handled those.
	if self := findPortOwner(8080, victim.configPath); self != nil {
		t.Fatalf("own instance should not be reported as a foreign owner: %+v", self)
	}

	// A port nobody declares is nobody's.
	if none := findPortOwner(59999, "/srv/mine/stacker.yml"); none != nil {
		t.Fatalf("unclaimed port reported an owner: %+v", none)
	}
}

// The whole point of the chosen behaviour: kill anyway, but leave a trail on
// both sides so the death is auditable instead of mysterious.
func TestClaimPortLogsOnBothSides(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	victimDir := t.TempDir()
	victimCfg := writeConfig(t, victimDir, `
version: 1
processes:
  frontend:
    command: "trap 'exit 0' TERM; while :; do sleep 1; done"
    port: 8080
    graceful_timeout: 2s
`)
	cfg, err := loadConfig(victimCfg)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	victim := newModel(cfg)
	victim.mode = "serve"
	cs, err := startControlServer(victim, victimCfg)
	if err != nil {
		t.Fatalf("startControlServer: %v", err)
	}
	defer cs.Close()
	if err := victim.processes[0].Start(victim.notify); err != nil {
		t.Fatalf("start victim: %v", err)
	}
	defer func() { _ = victim.processes[0].Stop(victim.notify) }()
	waitFor(t, 3*time.Second, func() bool { return victim.processes[0].Status() == StatusRunning })

	var mine []string
	claimed := claimPortFromOtherInstances(8080, "/srv/mine/stacker.yml", func(line string) {
		mine = append(mine, line)
	})
	if !claimed {
		t.Fatal("claimPortFromOtherInstances should report the claim")
	}

	joined := strings.Join(mine, "\n")
	if !strings.Contains(joined, "8080") || !strings.Contains(joined, "frontend") {
		t.Fatalf("claiming side log should name port and process:\n%s", joined)
	}

	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(strings.Join(victim.processes[0].Logs(), "\n"), "8080")
	})
	victimLogs := strings.Join(victim.processes[0].Logs(), "\n")
	if !strings.Contains(victimLogs, "/srv/mine/stacker.yml") {
		t.Fatalf("victim log should name who reclaimed the port:\n%s", victimLogs)
	}
}
