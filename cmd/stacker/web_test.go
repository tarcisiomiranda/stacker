package main

import (
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A wildcard bind must never advertise os.Hostname(): machine names routinely
// do not resolve (macOS *.local without mDNS, corporate DHCP suffixes), which
// produced a URL nobody could open.
func TestWebPublicBaseURLUsesLoopbackOnLocalDesktop(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("DISPLAY", ":0") // keep canOpenBrowser() true on headless Linux CI
	if got := webPublicBaseURL("0.0.0.0:52911"); got != "http://127.0.0.1:52911" {
		t.Fatalf("webPublicBaseURL = %q, want http://127.0.0.1:52911", got)
	}
}

// Over SSH the browser runs on the client, so loopback is useless. SSH_CONNECTION
// carries "client_ip client_port server_ip server_port": the server address is
// the one the client actually reached, so it is routable by construction.
func TestWebPublicBaseURLUsesSSHServerAddress(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.1.2.3 54321 192.168.1.20 22")
	if got := webPublicBaseURL("0.0.0.0:52911"); got != "http://192.168.1.20:52911" {
		t.Fatalf("webPublicBaseURL = %q, want http://192.168.1.20:52911", got)
	}
}

func TestWebPublicBaseURLBracketsIPv6SSHAddress(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "fe80::1 54321 2001:db8::20 22")
	if got := webPublicBaseURL("[::]:52911"); got != "http://[2001:db8::20]:52911" {
		t.Fatalf("webPublicBaseURL = %q, want http://[2001:db8::20]:52911", got)
	}
}

// An explicit ui.web_host is a deliberate choice and must survive untouched.
func TestWebPublicBaseURLKeepsExplicitBindHost(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.1.2.3 54321 192.168.1.20 22")
	if got := webPublicBaseURL("127.0.0.1:52911"); got != "http://127.0.0.1:52911" {
		t.Fatalf("webPublicBaseURL = %q, want the explicit bind host", got)
	}
	if got := webPublicBaseURL("10.0.0.9:52911"); got != "http://10.0.0.9:52911" {
		t.Fatalf("webPublicBaseURL = %q, want the explicit bind host", got)
	}
}

func TestSSHServerAddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"10.1.2.3 54321", ""},
		{"10.1.2.3 54321 192.168.1.20 22", "192.168.1.20"},
		{"  10.1.2.3   54321   192.168.1.20   22  ", "192.168.1.20"},
		{"fe80::1 54321 2001:db8::20 22", "2001:db8::20"},
		{"10.1.2.3 54321 not-an-ip 22", ""},
	}
	for _, tc := range cases {
		if got := sshServerAddress(tc.in); got != tc.want {
			t.Fatalf("sshServerAddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Two log tabs from different projects used to be indistinguishable: both were
// titled "backend — Stacker logs" with nothing naming the config being served.
func TestWebLogsPageIdentifiesTheProject(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  backend:
    command: true
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	m := newModel(cfg)
	ws, err := startWebServer(m, path)
	if err != nil {
		t.Fatalf("startWebServer: %v", err)
	}
	defer ws.Close()

	resp, err := http.Get("http://" + ws.Addr() + "/logs/backend")
	if err != nil {
		t.Fatalf("get logs page: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	page := string(body)

	label := configLabel(path, 1)
	if !strings.Contains(page, label) {
		t.Fatalf("logs page does not name the project %q:\n%s", label, page)
	}
	if !strings.Contains(page, path) {
		t.Fatalf("logs page does not show the config path %q", path)
	}

	// The tab title is what disambiguates two open tabs, so the label has to be
	// in the <title> and not only in the body.
	start := strings.Index(page, "<title>")
	end := strings.Index(page, "</title>")
	if start < 0 || end < start {
		t.Fatalf("no <title> in the logs page")
	}
	title := page[start:end]
	if !strings.Contains(title, label) {
		t.Fatalf("<title> %q does not include the project label %q", title, label)
	}
	if !strings.Contains(title, "backend") {
		t.Fatalf("<title> %q lost the process name", title)
	}
}

func TestListenWebDefaultPort(t *testing.T) {
	ln, err := listenWeb(UIConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if port == "" || port == "0" {
		t.Fatalf("empty port: %q", port)
	}
}

func TestCanOpenBrowserHeadlessLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only env probe")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if canOpenBrowser() {
		t.Fatal("expected canOpenBrowser false without display")
	}
	t.Setenv("DISPLAY", ":0")
	if !canOpenBrowser() {
		t.Fatal("expected canOpenBrowser true with DISPLAY")
	}
}

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

func TestWebTaskEndpoint(t *testing.T) {
	dir := t.TempDir()
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "sleep 60", Cwd: dir, Tasks: map[string]string{"hello": "echo done-web"}},
	}})
	ws, err := startWebServer(m, "stacker.yml")
	if err != nil {
		t.Fatalf("start web server: %v", err)
	}
	defer ws.Close()

	resp, err := http.Post("http://"+ws.Addr()+"/api/api/task", "application/json",
		strings.NewReader(`{"name": "hello"}`))
	if err != nil {
		t.Fatalf("post task: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	p := m.processByName("api")
	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(strings.Join(p.Logs(), "\n"), "[task hello] done-web")
	})

	bad, err := http.Post("http://"+ws.Addr()+"/api/api/task", "application/json",
		strings.NewReader(`{"name": "missing"}`))
	if err != nil {
		t.Fatalf("post bad task: %v", err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown task, got %d", bad.StatusCode)
	}
}

func TestWebLogsPageRendersColorPickerAndWrapToggle(t *testing.T) {
	m := newModel(Config{UI: UIConfig{WordWrap: true}, Processes: map[string]ProcessConfig{
		"api": {Command: "echo ok", Color: "#38bdf8", Port: 8123, Tasks: map[string]string{"migrate": "mise run migrate"}},
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
	for _, want := range []string{`id="more-pop"`, `id="wrap" checked`, `id="herr"`, `id="plist"`, `draggable="true"`, `data-act="free-port"`, "Free port 8123", `data-task="migrate"`, "whitespace-pre-wrap", "</html>"} {
		if !strings.Contains(page, want) {
			t.Fatalf("logs page missing %q:\n%s", want, page)
		}
	}
}

func TestWebStandaloneTaskSidebarAndPage(t *testing.T) {
	m := newModel(Config{
		Processes: map[string]ProcessConfig{"api": {Command: "echo ok"}},
		Tasks:     map[string]TaskConfig{"deploy": {Command: "echo deploy"}},
		taskOrder: []string{"deploy"},
	})
	ws, err := startWebServer(m, "stacker.yml")
	if err != nil {
		t.Fatalf("start web server: %v", err)
	}
	defer ws.Close()

	// A one-shot's own log page marks it as a task (▶ Run, no color section).
	resp, err := http.Get("http://" + ws.Addr() + "/logs/deploy")
	if err != nil {
		t.Fatalf("get task page: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	for _, want := range []string{`data-trow="deploy"`, "▶ Run", "Tasks · one-shot"} {
		if !strings.Contains(page, want) {
			t.Fatalf("task page missing %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, `id="color-swatches"`) {
		t.Fatal("standalone task page must not offer the color selector")
	}

	// The color endpoint is refused for a one-shot.
	c, err := http.Post("http://"+ws.Addr()+"/api/deploy/color", "application/json",
		strings.NewReader(`{"color": "#38bdf8"}`))
	if err != nil {
		t.Fatalf("post color: %v", err)
	}
	defer c.Body.Close()
	if c.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for task color, got %d", c.StatusCode)
	}

	// Running the one-shot via its start action executes the command.
	run, err := http.Post("http://"+ws.Addr()+"/api/deploy/start", "application/json", nil)
	if err != nil {
		t.Fatalf("post start: %v", err)
	}
	run.Body.Close()
	p := m.processByName("deploy")
	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(strings.Join(p.Logs(), "\n"), "deploy") && p.Status() == StatusStopped
	})
}
