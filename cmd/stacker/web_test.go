package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWebColorEndpointUpdatesProcessAndConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  # api service
  api:
    command: echo ok
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	m := newModel(cfg)
	ws, err := startWebServer(m, path)
	if err != nil {
		t.Fatalf("start web server: %v", err)
	}
	defer ws.Close()

	post := func(body string) *http.Response {
		t.Helper()
		resp, err := http.Post("http://"+ws.Addr()+"/api/api/color", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post color: %v", err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	if resp := post(`{"color": "#38bdf8"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := m.processByName("api").Color(); got != "#38bdf8" {
		t.Fatalf("expected process color updated, got %q", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), `"#38bdf8"`) || !strings.Contains(string(data), "# api service") {
		t.Fatalf("config not rewritten with color and comment preserved:\n%s", data)
	}

	if resp := post(`{"color": "not a color!"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid color, got %d", resp.StatusCode)
	}

	if resp := post(`{"color": ""}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for color removal, got %d", resp.StatusCode)
	}
	if got := m.processByName("api").Color(); got != "" {
		t.Fatalf("expected color removed, got %q", got)
	}
}

func TestWebOrderEndpointReordersAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  api:
    command: echo ok
  web:
    command: echo ok
  demo:
    command: echo ok
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	m := newModel(cfg)
	ws, err := startWebServer(m, path)
	if err != nil {
		t.Fatalf("start web server: %v", err)
	}
	defer ws.Close()

	resp, err := http.Post("http://"+ws.Addr()+"/api/order", "application/json",
		strings.NewReader(`{"names": ["demo", "api", "web"]}`))
	if err != nil {
		t.Fatalf("post order: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// The TUI goroutine applies the pending order on refresh; simulate it.
	m.applyPendingOrder()
	want := []string{"demo", "api", "web"}
	for i, info := range m.processInfos() {
		if info.Name != want[i] {
			t.Fatalf("expected in-memory order %v, got %q at %d", want, info.Name, i)
		}
	}
	cfg2, err := loadConfig(path)
	if err != nil {
		t.Fatalf("reordered config must load: %v", err)
	}
	for i := range want {
		if cfg2.processOrder[i] != want[i] {
			t.Fatalf("expected YAML order %v, got %v", want, cfg2.processOrder)
		}
	}

	bad, err := http.Post("http://"+ws.Addr()+"/api/order", "application/json",
		strings.NewReader(`{"names": ["api"]}`))
	if err != nil {
		t.Fatalf("post bad order: %v", err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad permutation, got %d", bad.StatusCode)
	}
}

func TestWebHighlightErrorsToggle(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  api:
    command: echo ok
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	m := newModel(cfg)
	ws, err := startWebServer(m, path)
	if err != nil {
		t.Fatalf("start web server: %v", err)
	}
	defer ws.Close()

	resp, err := http.Post("http://"+ws.Addr()+"/api/highlight-errors", "application/json",
		strings.NewReader(`{"enabled": true}`))
	if err != nil {
		t.Fatalf("post toggle: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	p := m.processByName("api")
	p.capture(strings.NewReader("panic: boom\n"), "stderr", func() {})
	if got := p.Errors(); got != 1 {
		t.Fatalf("expected detection enabled after toggle, got %d errors", got)
	}
	cfg2, err := loadConfig(path)
	if err != nil {
		t.Fatalf("config must load after toggle: %v", err)
	}
	if !cfg2.UI.HighlightErrors {
		t.Fatal("expected highlight_errors persisted as true")
	}

	resp2, err := http.Post("http://"+ws.Addr()+"/api/highlight-errors", "application/json",
		strings.NewReader(`{"enabled": false}`))
	if err != nil {
		t.Fatalf("post toggle off: %v", err)
	}
	defer resp2.Body.Close()
	if got := p.Errors(); got != 0 {
		t.Fatalf("expected badge cleared when disabled, got %d", got)
	}
}

func TestWebFreePortEndpoint(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	freeport := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api":    {Command: "echo ok", Port: freeport},
		"noport": {Command: "echo ok"},
	}})
	ws, err := startWebServer(m, "stacker.yml")
	if err != nil {
		t.Fatalf("start web server: %v", err)
	}
	defer ws.Close()

	resp, err := http.Post("http://"+ws.Addr()+"/api/api/free-port", "application/json", nil)
	if err != nil {
		t.Fatalf("post free-port: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	p := m.processByName("api")
	waitFor(t, 5*time.Second, func() bool {
		return strings.Contains(strings.Join(p.Logs(), "\n"), "nothing listening")
	})

	bad, err := http.Post("http://"+ws.Addr()+"/api/noport/free-port", "application/json", nil)
	if err != nil {
		t.Fatalf("post free-port noport: %v", err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for process without port, got %d", bad.StatusCode)
	}
}

func TestWebLogsPageRendersColorPickerAndWrapToggle(t *testing.T) {
	m := newModel(Config{UI: UIConfig{WordWrap: true}, Processes: map[string]ProcessConfig{
		"api": {Command: "echo ok", Color: "#38bdf8", Port: 8123},
	}})
	ws, err := startWebServer(m, "stacker.yml")
	if err != nil {
		t.Fatalf("start web server: %v", err)
	}
	defer ws.Close()

	resp, err := http.Get("http://" + ws.Addr() + "/logs/api")
	if err != nil {
		t.Fatalf("get logs page: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	page := string(body)
	// The template must fully execute: the script block is at the end.
	for _, want := range []string{`id="more-pop"`, `id="wrap" checked`, `id="herr"`, `id="plist"`, `draggable="true"`, `data-act="free-port"`, "Free port 8123", "whitespace-pre-wrap", "</html>"} {
		if !strings.Contains(page, want) {
			t.Fatalf("logs page missing %q:\n%s", want, page)
		}
	}
}
