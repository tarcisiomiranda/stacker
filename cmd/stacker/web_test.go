package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
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

func TestWebLogsPageRendersColorPickerAndWrapToggle(t *testing.T) {
	m := newModel(Config{UI: UIConfig{WordWrap: true}, Processes: map[string]ProcessConfig{
		"api": {Command: "echo ok", Color: "#38bdf8"},
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
	for _, want := range []string{`id="color-pop"`, `id="wrap" checked`, "whitespace-pre-wrap", "</html>"} {
		if !strings.Contains(page, want) {
			t.Fatalf("logs page missing %q:\n%s", want, page)
		}
	}
}
