package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServeAndDown(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  demo:
    command: "trap 'exit 0' TERM; while :; do sleep 1; done"
    graceful_timeout: 2s
`)

	// Ensure no leftover state from other tests with same path.
	_, statePath, err := statePathFor(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(statePath)

	done := make(chan int, 1)
	go func() {
		done <- runServe(path, false, false)
	}()

	// Wait for control plane.
	var st *InstanceState
	waitFor(t, 3*time.Second, func() bool {
		st, err = findRunningInstance(path)
		return err == nil && st != nil
	})
	if st.Mode != "serve" {
		t.Fatalf("mode = %q, want serve", st.Mode)
	}

	client := &controlClient{
		addr:   st.Addr,
		client: &http.Client{Timeout: 5 * time.Second},
	}
	var list struct {
		OK        bool          `json:"ok"`
		Processes []ProcessInfo `json:"processes"`
	}
	if err := client.get("/v1/processes", &list); err != nil {
		t.Fatal(err)
	}
	if !list.OK || len(list.Processes) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// down
	code := runDown(path, true)
	if code != 0 {
		t.Fatalf("down exit %d", code)
	}

	select {
	case c := <-done:
		if c != 0 {
			t.Fatalf("serve exit %d", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not exit after down")
	}

	// State file should be gone or process dead.
	waitFor(t, 2*time.Second, func() bool {
		st2, _ := findRunningInstance(path)
		return st2 == nil
	})
}

func TestControlLogsEndpoint(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  demo:
    command: "echo hello-from-logs"
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg)
	m.mode = "session"
	cs, err := startControlServer(m, path)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	if err := m.processes[0].Start(func() {}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return m.processes[0].LogNext() > 0
	})

	resp, err := http.Get("http://" + cs.listener.Addr().String() + "/v1/processes/demo/logs?from=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		OK    bool     `json:"ok"`
		Lines []string `json:"lines"`
		Next  int      `json:"next"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Next == 0 {
		t.Fatalf("logs body = %+v", body)
	}
	_ = filepath.Base(path)
}
