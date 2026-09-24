package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
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

func TestControlGroupSnapshotAndMutation(t *testing.T) {
	m, cs, configPath := startControlTest(t, `
version: 1
processes:
  api:
    command: true
    group: hub
tasks:
  deploy:
    command: true
    group: release
`)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + cs.listener.Addr().String() + "/v1/processes")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var list struct {
		OK        bool `json:"ok"`
		Processes []struct {
			Name  string `json:"name"`
			Group string `json:"group"`
		} `json:"processes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		resp.Body.Close()
		t.Fatalf("decode list: %v", err)
	}
	resp.Body.Close()
	if !list.OK || len(list.Processes) != 2 || list.Processes[0].Group != "hub" || list.Processes[1].Group != "release" {
		t.Fatalf("unexpected group snapshot: %+v", list)
	}

	for _, request := range []struct {
		name  string
		group string
		want  string
	}{
		{name: "api", group: `{"group":"  release  "}`, want: "release"},
		{name: "api", group: `{"group":" "}`, want: ""},
		{name: "deploy", group: `{"group":"  deploy  "}`, want: "deploy"},
		{name: "deploy", group: `{"group":""}`, want: ""},
	} {
		response, body := postControl(t, cs, "/v1/processes/"+request.name+"/group", request.group)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("set group for %s: status=%d body=%s", request.name, response.StatusCode, body)
		}
		process := m.processByName(request.name)
		if process.Group() != request.want {
			t.Fatalf("live group for %s = %q, want %q", request.name, process.Group(), request.want)
		}
		if request.name == "api" {
			if m.cfg.Processes[request.name].Group != request.want {
				t.Fatalf("live config group for %s = %q, want %q", request.name, m.cfg.Processes[request.name].Group, request.want)
			}
		} else if m.cfg.Tasks[request.name].Group != request.want {
			t.Fatalf("live task group for %s = %q, want %q", request.name, m.cfg.Tasks[request.name].Group, request.want)
		}
		cfg, err := loadConfig(configPath)
		if err != nil {
			t.Fatalf("load updated config: %v", err)
		}
		got := cfg.Processes[request.name].Group
		if request.name == "deploy" {
			got = cfg.Tasks[request.name].Group
		}
		if got != request.want {
			t.Fatalf("persisted group for %s = %q, want %q", request.name, got, request.want)
		}
	}

	response, body := postControl(t, cs, "/v1/processes/api/group", `{"group":`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed group body: status=%d body=%s", response.StatusCode, body)
	}
}

func TestControlGroupMutationRejectsOrphans(t *testing.T) {
	m, cs, _ := startControlTest(t, `
version: 1
processes:
  api:
    command: true
    group: hub
`)
	process := m.processByName("api")
	process.orphaned = true
	response, body := postControl(t, cs, "/v1/processes/api/group", `{"group":"release"}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("orphan group mutation: status=%d body=%s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), "orphan") {
		t.Fatalf("orphan group mutation response did not explain refusal: %s", body)
	}
	if process.Group() != "hub" {
		t.Fatalf("orphan group changed to %q", process.Group())
	}
}

func TestControlGroupActions(t *testing.T) {
	m, cs, _ := startControlTest(t, `
version: 1
processes:
  api:
    command: true
    group: hub
  worker:
    command: true
    group: hub
  loose:
    command: true
tasks:
  deploy:
    command: true
    group: hub
  plain:
    command: true
`)
	for _, name := range []string{"api", "worker", "deploy", "loose", "plain"} {
		process := m.processByName(name)
		process.mu.Lock()
		process.status = StatusRunning
		process.mu.Unlock()
	}

	response, body := postControl(t, cs, "/v1/groups/mark", `{"group":" hub "}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("mark hub: status=%d body=%s", response.StatusCode, body)
	}
	var marked struct {
		OK       bool            `json:"ok"`
		Affected []string        `json:"affected"`
		Names    json.RawMessage `json:"names"`
	}
	if err := json.Unmarshal(body, &marked); err != nil {
		t.Fatalf("decode mark response: %v", err)
	}
	if !marked.OK || !reflect.DeepEqual(marked.Affected, []string{"api", "worker", "deploy"}) || len(marked.Names) != 0 {
		t.Fatalf("unexpected hub mark response: %+v", marked)
	}
	response, body = postControl(t, cs, "/v1/groups/mark", `{"group":""}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("mark Other: status=%d body=%s", response.StatusCode, body)
	}
	var otherMarked struct {
		OK       bool            `json:"ok"`
		Affected []string        `json:"affected"`
		Names    json.RawMessage `json:"names"`
	}
	if err := json.Unmarshal(body, &otherMarked); err != nil {
		t.Fatalf("decode Other mark response: %v", err)
	}
	if !otherMarked.OK || !reflect.DeepEqual(otherMarked.Affected, []string{"loose", "plain"}) || len(otherMarked.Names) != 0 {
		t.Fatalf("unexpected Other mark response: %+v", otherMarked)
	}

	response, body = postControl(t, cs, "/v1/groups/mark", `{"group":"missing"}`)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown group: status=%d body=%s", response.StatusCode, body)
	}
	response, body = postControl(t, cs, "/v1/groups/unknown", `{"group":"hub"}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid group action: status=%d body=%s", response.StatusCode, body)
	}
	response, body = postControl(t, cs, "/v1/groups/mark", `{"group":`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed group action: status=%d body=%s", response.StatusCode, body)
	}
}

func TestControlGroupActionFailureResponse(t *testing.T) {
	m, cs, _ := startControlTest(t, `
version: 1
processes:
  api:
    command: true
    group: hub
`)
	process := m.processByName("api")
	process.mu.Lock()
	process.Config.GracefulTimeout = "invalid"
	process.status = StatusRunning
	process.mu.Unlock()
	response, body := postControl(t, cs, "/v1/groups/stop", `{"group":"hub"}`)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("stop failure: status=%d body=%s", response.StatusCode, body)
	}
	var failed struct {
		OK       bool            `json:"ok"`
		Affected []string        `json:"affected"`
		Names    json.RawMessage `json:"names"`
		Error    string          `json:"error"`
	}
	if err := json.Unmarshal(body, &failed); err != nil {
		t.Fatalf("decode failed group action: %v", err)
	}
	if failed.OK || failed.Error == "" || !reflect.DeepEqual(failed.Affected, []string{"api"}) || len(failed.Names) != 0 {
		t.Fatalf("unexpected failed group action: %+v", failed)
	}
}

func TestControlOrderMixedAndServiceOnly(t *testing.T) {
	m, cs, configPath := startControlTest(t, `
version: 1
processes:
  api:
    command: true
  web:
    command: true
tasks:
  deploy:
    command: true
  backup:
    command: true
`)
	response, body := postControl(t, cs, "/v1/order", `{"names":["backup","deploy","web","api"]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("mixed order: status=%d body=%s", response.StatusCode, body)
	}
	assertControlOrder(t, configPath, []string{"web", "api"}, []string{"backup", "deploy"})
	m.applyPendingOrder()
	services, tasks := configuredProcessOrders(m.procs())
	if !reflect.DeepEqual(services, []string{"web", "api"}) || !reflect.DeepEqual(tasks, []string{"backup", "deploy"}) {
		t.Fatalf("mixed pending order applied as services=%v tasks=%v", services, tasks)
	}

	response, body = postControl(t, cs, "/v1/order", `{"names":["api","web"]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("service-only order: status=%d body=%s", response.StatusCode, body)
	}
	assertControlOrder(t, configPath, []string{"api", "web"}, []string{"backup", "deploy"})
}

func TestControlOrderRejectsUnknownOrphanAndInvalidPermutations(t *testing.T) {
	m, cs, configPath := startControlTest(t, `
version: 1
processes:
  api:
    command: true
  web:
    command: true
tasks:
  deploy:
    command: true
  backup:
    command: true
`)
	m.processByName("web").orphaned = true
	for _, names := range []string{
		`["api","missing"]`,
		`["api","web","deploy","backup"]`,
		`["api","api"]`,
		`["api","deploy"]`,
	} {
		before, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		response, body := postControl(t, cs, "/v1/order", `{"names":`+names+`}`)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid order %s: status=%d body=%s", names, response.StatusCode, body)
		}
		after, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read config after invalid order: %v", err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("invalid order %s changed config", names)
		}
		m.pendingMu.Lock()
		queued := m.pendingOrder != nil
		m.pendingMu.Unlock()
		if queued {
			t.Fatalf("invalid order %s was queued", names)
		}
	}
}

func TestControlOrderValidatesBothMappingsBeforeWriting(t *testing.T) {
	m, cs, configPath := startControlTest(t, `
version: 1
processes:
  api:
    command: true
  web:
    command: true
tasks:
  deploy:
    command: true
  backup:
    command: true
`)
	if err := os.WriteFile(configPath, []byte("version: 1\nprocesses:\n  api:\n    command: true\n  web:\n    command: true\ntasks:\n  another:\n    command: true\n"), 0o600); err != nil {
		t.Fatalf("rewrite config tasks: %v", err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	response, body := postControl(t, cs, "/v1/order", `{"names":["web","api","deploy","backup"]}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched task mapping: status=%d body=%s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), "tasks") {
		t.Fatalf("mixed order did not validate the task mapping: %s", body)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config after mixed order: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("invalid task permutation partially changed config")
	}
	m.pendingMu.Lock()
	queued := m.pendingOrder != nil
	m.pendingMu.Unlock()
	if queued {
		t.Fatal("mixed order with an invalid mapping was queued")
	}
}

func startControlTest(t *testing.T, contents string) (*model, *controlServer, string) {
	t.Helper()
	configPath := writeConfig(t, t.TempDir(), contents)
	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	m := newModel(cfg)
	cs, err := startControlServer(m, configPath)
	if err != nil {
		t.Fatalf("startControlServer: %v", err)
	}
	t.Cleanup(cs.Close)
	return m, cs, configPath
}

func postControl(t *testing.T, cs *controlServer, path, body string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post("http://"+cs.listener.Addr().String()+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read %s response: %v", path, err)
	}
	return resp, data
}

func assertControlOrder(t *testing.T, configPath string, wantProcesses, wantTasks []string) {
	t.Helper()
	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("load ordered config: %v", err)
	}
	if !reflect.DeepEqual(cfg.processOrder, wantProcesses) || !reflect.DeepEqual(cfg.taskOrder, wantTasks) {
		t.Fatalf("config order processes=%v tasks=%v, want processes=%v tasks=%v", cfg.processOrder, cfg.taskOrder, wantProcesses, wantTasks)
	}
}
