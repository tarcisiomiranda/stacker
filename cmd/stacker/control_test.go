package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
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

func TestControlPlaneRunTask(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, `
version: 1
processes:
  demo:
    command: "trap 'exit 0' TERM; while :; do sleep 1; done"
    cwd: .
    graceful_timeout: 2s
    tasks:
      hello: echo control-task-ok
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

	// tasks appear in the process listing
	listResp, err := client.Get("http://" + st.Addr + "/v1/processes")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var list struct {
		Processes []ProcessInfo `json:"processes"`
	}
	_ = json.NewDecoder(listResp.Body).Decode(&list)
	listResp.Body.Close()
	if len(list.Processes) != 1 || len(list.Processes[0].Tasks) != 1 || list.Processes[0].Tasks[0] != "hello" {
		t.Fatalf("expected task listed, got %+v", list.Processes)
	}

	resp, err := client.Post("http://"+st.Addr+"/v1/tasks/demo/hello", "", nil)
	if err != nil {
		t.Fatalf("run task: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run task status %d", resp.StatusCode)
	}
	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(strings.Join(m.processes[0].Logs(), "\n"), "[task hello] control-task-ok")
	})

	bad, err := client.Post("http://"+st.Addr+"/v1/tasks/demo/nope", "", nil)
	if err != nil {
		t.Fatalf("run unknown task: %v", err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown task, got %d", bad.StatusCode)
	}
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

	// Mark: appends a separator to the logs.
	markResp, err := client.Post("http://"+ws.Addr()+"/api/demo/mark", "", nil)
	if err != nil {
		t.Fatalf("mark: %v", err)
	}
	markResp.Body.Close()
	if markResp.StatusCode != http.StatusOK {
		t.Fatalf("mark status %d", markResp.StatusCode)
	}
	found := false
	for _, line := range m.processes[0].Logs() {
		if strings.Contains(line, "mark ") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("mark separator not appended to logs")
	}

	// Tail: incremental logs plus statuses of every process.
	tailResp, err := client.Get("http://" + ws.Addr() + "/api/demo/tail?from=0")
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	var tail struct {
		OK        bool          `json:"ok"`
		From      int           `json:"from"`
		Next      int           `json:"next"`
		Lines     []string      `json:"lines"`
		Processes []ProcessInfo `json:"processes"`
	}
	if err := json.NewDecoder(tailResp.Body).Decode(&tail); err != nil {
		t.Fatalf("decode tail: %v", err)
	}
	tailResp.Body.Close()
	if !tail.OK || tail.Next == 0 || len(tail.Lines) == 0 || len(tail.Processes) != 1 {
		t.Fatalf("unexpected tail: %+v", tail)
	}
	// Asking from the end returns no lines.
	tailResp2, err := client.Get("http://" + ws.Addr() + fmt.Sprintf("/api/demo/tail?from=%d", tail.Next))
	if err != nil {
		t.Fatalf("tail2: %v", err)
	}
	var tail2 struct {
		Lines []string `json:"lines"`
		Next  int      `json:"next"`
	}
	if err := json.NewDecoder(tailResp2.Body).Decode(&tail2); err != nil {
		t.Fatalf("decode tail2: %v", err)
	}
	tailResp2.Body.Close()
	if len(tail2.Lines) != 0 || tail2.Next != tail.Next {
		t.Fatalf("expected empty incremental tail, got %+v", tail2)
	}

	// Start and stop are accepted (async).
	for _, action := range []string{"start", "stop"} {
		resp, err := client.Post("http://"+ws.Addr()+"/api/demo/"+action, "", nil)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status %d", action, resp.StatusCode)
		}
	}

	// Mark-all: only running processes get the separator. Wait for the async
	// start/stop above to settle before forcing the status.
	waitFor(t, 3*time.Second, func() bool {
		st := m.processes[0].Status()
		return st == StatusStopped || st == StatusFailed
	})
	time.Sleep(100 * time.Millisecond)
	m.processes[0].mu.Lock()
	m.processes[0].status = StatusRunning
	m.processes[0].mu.Unlock()
	before := m.processes[0].LogNext()
	allResp, err := client.Post("http://"+ws.Addr()+"/api/mark-all", "", nil)
	if err != nil {
		t.Fatalf("mark-all: %v", err)
	}
	var all struct {
		OK     bool `json:"ok"`
		Marked int  `json:"marked"`
	}
	if err := json.NewDecoder(allResp.Body).Decode(&all); err != nil {
		t.Fatalf("decode mark-all: %v", err)
	}
	allResp.Body.Close()
	if !all.OK || all.Marked != 1 || m.processes[0].LogNext() != before+3 {
		t.Fatalf("unexpected mark-all: %+v next=%d before=%d", all, m.processes[0].LogNext(), before)
	}
	m.processes[0].mu.Lock()
	m.processes[0].status = StatusStopped
	m.processes[0].mu.Unlock()

	// Restart: accepted for a known process, 404 for unknown.
	restartResp, err := client.Post("http://"+ws.Addr()+"/api/demo/restart", "", nil)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	restartResp.Body.Close()
	if restartResp.StatusCode != http.StatusOK {
		t.Fatalf("restart status %d", restartResp.StatusCode)
	}
	badResp, err := client.Post("http://"+ws.Addr()+"/api/nope/restart", "", nil)
	if err != nil {
		t.Fatalf("restart unknown: %v", err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusNotFound {
		t.Fatalf("restart unknown status %d", badResp.StatusCode)
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

func TestResolveRunningInstanceFallsBackToSingle(t *testing.T) {
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
	m.mode = "serve"
	cs, err := startControlServer(m, cfgPath)
	if err != nil {
		t.Fatalf("startControlServer: %v", err)
	}
	defer cs.Close()

	// Request a config that is not running; only one live instance exists.
	other := filepath.Join(dir, "other.yml")
	st, used, fallback, err := resolveRunningInstance(other)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if st == nil || !fallback {
		t.Fatalf("expected fallback to single instance, st=%v fallback=%v used=%q", st, fallback, used)
	}
	if st.Config == "" {
		t.Fatal("empty Config on resolved state")
	}
	// Direct path still resolves without fallback.
	st2, _, fallback2, err := resolveRunningInstance(cfgPath)
	if err != nil || st2 == nil || fallback2 {
		t.Fatalf("direct resolve: st=%v fallback=%v err=%v", st2, fallback2, err)
	}
}

// A headless supervisor has no TUI, so the web viewer can only be toggled
// through the control plane. Without this endpoint `w` in attach has nothing to
// call and the viewer is unreachable for the whole life of the daemon.
func TestControlPlaneWebToggle(t *testing.T) {
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
	m.mode = "serve"
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

	resp, err := client.Post("http://"+st.Addr+"/v1/web", "application/json",
		strings.NewReader(`{"enabled": true}`))
	if err != nil {
		t.Fatalf("enable web: %v", err)
	}
	var on struct {
		OK      bool   `json:"ok"`
		Enabled bool   `json:"enabled"`
		Addr    string `json:"addr"`
		Error   string `json:"error"`
	}
	err = json.NewDecoder(resp.Body).Decode(&on)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("decode enable: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !on.OK || !on.Enabled || on.Addr == "" {
		t.Fatalf("enable web: status=%d resp=%+v", resp.StatusCode, on)
	}

	// The reported address must be a live listener, not just a bookkeeping flag.
	_, port, err := net.SplitHostPort(on.Addr)
	if err != nil {
		t.Fatalf("addr %q: %v", on.Addr, err)
	}
	page, err := client.Get("http://127.0.0.1:" + port + "/")
	if err != nil {
		t.Fatalf("web index on reported addr: %v", err)
	}
	page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("web index status %d", page.StatusCode)
	}

	// Enabling twice is idempotent and keeps the same listener.
	again, err := client.Post("http://"+st.Addr+"/v1/web", "application/json",
		strings.NewReader(`{"enabled": true}`))
	if err != nil {
		t.Fatalf("re-enable web: %v", err)
	}
	var second struct {
		OK   bool   `json:"ok"`
		Addr string `json:"addr"`
	}
	err = json.NewDecoder(again.Body).Decode(&second)
	again.Body.Close()
	if err != nil {
		t.Fatalf("decode re-enable: %v", err)
	}
	if !second.OK || second.Addr != on.Addr {
		t.Fatalf("re-enable changed the listener: %q → %q", on.Addr, second.Addr)
	}

	off, err := client.Post("http://"+st.Addr+"/v1/web", "application/json",
		strings.NewReader(`{"enabled": false}`))
	if err != nil {
		t.Fatalf("disable web: %v", err)
	}
	var down struct {
		OK      bool `json:"ok"`
		Enabled bool `json:"enabled"`
	}
	err = json.NewDecoder(off.Body).Decode(&down)
	off.Body.Close()
	if err != nil {
		t.Fatalf("decode disable: %v", err)
	}
	if !down.OK || down.Enabled {
		t.Fatalf("disable web: %+v", down)
	}

	// The listener must actually be gone.
	waitFor(t, 3*time.Second, func() bool {
		resp, err := client.Get("http://127.0.0.1:" + port + "/")
		if err != nil {
			return true
		}
		resp.Body.Close()
		return false
	})
}
