package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestControlPlaneListStartStop(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, `
version: 1
processes:
  demo:
    command: "trap 'exit 0' TERM; while :; do sleep 1; done"
    cwd: .
    graceful_timeout: 2s
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
		t.Fatalf("findRunningInstance: st=%v err=%v", st, err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + st.Addr + "/v1/processes")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer resp.Body.Close()
	var list struct {
		OK        bool          `json:"ok"`
		Processes []ProcessInfo `json:"processes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if !list.OK || len(list.Processes) != 1 || list.Processes[0].Name != "demo" {
		t.Fatalf("unexpected list: %+v", list)
	}

	startResp, err := client.Post("http://"+st.Addr+"/v1/processes/demo/start", "", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("start status %d", startResp.StatusCode)
	}

	waitFor(t, 2*time.Second, func() bool {
		return m.processes[0].Status() == StatusRunning
	})

	stopResp, err := client.Post("http://"+st.Addr+"/v1/processes/demo/stop", "", nil)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	stopResp.Body.Close()

	waitFor(t, 3*time.Second, func() bool {
		return m.processes[0].Status() == StatusStopped
	})
}

func TestControlPlaneRejectsSecondInstance(t *testing.T) {
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

	m2 := newModel(cfg)
	if _, err := startControlServer(m2, cfgPath); err == nil {
		t.Fatal("expected second control plane to fail")
	}
}
