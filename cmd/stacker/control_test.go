package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
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

func TestWebLogsEndpoints(t *testing.T) {
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
	m.processes[0].appendLog("[stdout] hello web")
	ws, err := startWebServer(m, cfgPath)
	if err != nil {
		t.Fatalf("startWebServer: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}

	for path, want := range map[string]string{
		"/":              "demo",
		"/logs/demo":     "hello web",
		"/logs/demo/raw": "[stdout] hello web",
	} {
		resp, err := client.Get("http://" + ws.Addr() + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status %d", path, resp.StatusCode)
		}
		if !strings.Contains(string(body), want) {
			t.Fatalf("%s missing %q in body: %s", path, want, body)
		}
	}

	resp, err := client.Get("http://" + ws.Addr() + "/logs/nope")
	if err != nil {
		t.Fatalf("get unknown: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown process status %d", resp.StatusCode)
	}

	// Toggle off: the server must stop accepting connections.
	addr := ws.Addr()
	ws.Close()
	if _, err := client.Get("http://" + addr + "/"); err == nil {
		t.Fatal("expected request to fail after Close")
	}
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
