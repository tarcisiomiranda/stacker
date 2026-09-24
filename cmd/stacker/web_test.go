package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"regexp"
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

func TestWebGroupedSectionsOnIndexAndLogsSidebar(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  worker:
    command: true
    group: jobs
  api:
    command: true
    group: hub
  misc:
    command: true
  named-other:
    command: true
    group: Other
tasks:
  release-only:
    command: true
    group: release
  deploy:
    command: true
    group: hub
  cleanup:
    command: true
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

	for _, pagePath := range []string{"/", "/logs/api"} {
		resp, err := http.Get("http://" + ws.Addr() + pagePath)
		if err != nil {
			t.Fatalf("get %s: %v", pagePath, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read %s: %v", pagePath, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get %s status = %d", pagePath, resp.StatusCode)
		}
		page := string(body)
		if !regexp.MustCompile(`try\s*\{\s*localStorage\.setItem\(foldStorageKey,\s*JSON\.stringify\(\[\.\.\.foldedGroups\]\)\);\s*\}\s*catch\s*\{\s*\}`).MatchString(page) {
			t.Fatalf("%s fold persistence write is not guarded against storage failures", pagePath)
		}
		foldWrite := strings.Index(page, "localStorage.setItem(foldStorageKey")
		for _, effect := range []string{
			`.classList.toggle("hidden", folded)`,
			"foldedGroups.add(section.dataset.groupSection)",
			"foldedGroups.delete(section.dataset.groupSection)",
		} {
			effectIndex := strings.Index(page, effect)
			if foldWrite < 0 || effectIndex < 0 || effectIndex > foldWrite {
				t.Fatalf("%s fold change %q is not applied before persistence", pagePath, effect)
			}
		}
		positions := []int{
			strings.Index(page, `data-group-section="jobs"`),
			strings.Index(page, `data-group-section="hub"`),
			strings.Index(page, `data-group-section="release"`),
			strings.Index(page, `data-group-section="Other"`),
		}
		for index, position := range positions {
			if position < 0 {
				t.Fatalf("%s missing grouped section %d:\n%s", pagePath, index, page)
			}
			if index > 0 && positions[index-1] >= position {
				t.Fatalf("%s grouped section order = %v", pagePath, positions)
			}
		}
		sectionContent := func(name string) string {
			start := strings.Index(page, `data-group-section="`+name+`"`)
			if start < 0 {
				t.Fatalf("%s missing group %q", pagePath, name)
			}
			end := strings.Index(page[start:], "</section>")
			if end < 0 {
				t.Fatalf("%s group %q is not closed", pagePath, name)
			}
			return page[start : start+end]
		}
		if strings.Contains(page, `id="task-head"`) || strings.Contains(page, `<p class="mt-6 mb-1 text-xs uppercase tracking-wide text-neutral-500">Tasks · one-shot</p>`) {
			t.Fatalf("%s grouped layout retained the one-shot task section", pagePath)
		}
		if !strings.Contains(sectionContent("hub"), `data-group-member="deploy"`) || !strings.Contains(sectionContent("release"), `data-group-member="release-only"`) || !strings.Contains(sectionContent("Other"), `data-group-member="cleanup"`) {
			t.Fatalf("%s grouped tasks are not inside their matching sections:\n%s", pagePath, page)
		}
		header := sectionContent("hub")
		if !strings.Contains(header, `data-fold-icon>−</span>`) || strings.Count(header, `bg-neutral-700`) < 2 {
			t.Fatalf("%s group header does not frame its title with horizontal dividers:\n%s", pagePath, header)
		}
		if !strings.Contains(page, `toggle.querySelector("[data-fold-icon]").textContent = folded ? "+" : "−";`) {
			t.Fatalf("%s fold control does not switch between the approved markers", pagePath)
		}
		if !strings.Contains(sectionContent("Other"), `data-group-value=""`) || !strings.Contains(sectionContent("Other"), `data-group-member="misc"`) || !strings.Contains(sectionContent("Other"), `data-group-member="named-other"`) {
			t.Fatalf("%s effective Other section must have an empty group value and retain all members:\n%s", pagePath, page)
		}
		if !strings.Contains(sectionContent("hub"), `>▶</span>`) {
			t.Fatalf("%s grouped tasks are missing the one-shot marker:\n%s", pagePath, page)
		}
		for _, action := range []string{"start", "stop", "restart"} {
			if !strings.Contains(page, `data-group-action="`+action+`"`) {
				t.Fatalf("%s grouped layout missing %s all action:\n%s", pagePath, action, page)
			}
		}
		if !strings.Contains(page, `data-group-summary`) {
			t.Fatalf("%s grouped layout missing service summary:\n%s", pagePath, page)
		}
		if !strings.Contains(page, `data-group-summary class="text-xs text-neutral-500">0/1</span>`) || !strings.Contains(page, `data-group-fold`) {
			t.Fatalf("%s grouped header is missing the service-only count or fold control:\n%s", pagePath, page)
		}
		if strings.Contains(sectionContent("release"), `data-group-summary`) {
			t.Fatalf("%s task-only section displays a service summary", pagePath)
		}
		if !strings.Contains(sectionContent("jobs"), `data-group-summary`) {
			t.Fatalf("%s service section is missing its summary", pagePath)
		}
		if pagePath == "/logs/api" {
			for _, want := range []string{
				"if (total > 0)",
				`rule.className = "h-px bg-neutral-700 flex-1";`,
				`fold.append(leftRule, icon, title, rightRule);`,
				`icon.textContent = "−";`,
				"function canonicalDisplayEntries(processes)",
				"return JSON.stringify(canonicalDisplayEntries(processes));",
				"return JSON.stringify(canonicalDisplayEntries(sidebarEntriesFromDOM()));",
				"Date.now() < expectedSidebarStructure.expiresAt",
				"restoreDraggedRow(row, dragOrigin)",
				`groupValue: name === "Other" ? "" : explicitGroup`,
				`section.groupValue = ""`,
			} {
				if !strings.Contains(page, want) {
					t.Fatalf("%s sidebar script missing %q", pagePath, want)
				}
			}
		}
	}
}

func TestWebNoGroupKeepsStandaloneTaskLayout(t *testing.T) {
	m := newModel(Config{
		Processes: map[string]ProcessConfig{"api": {Command: "true"}},
		Tasks:     map[string]TaskConfig{"deploy": {Command: "true"}},
		taskOrder: []string{"deploy"},
	})
	ws, err := startWebServer(m, "stacker.yml")
	if err != nil {
		t.Fatalf("start web server: %v", err)
	}
	defer ws.Close()

	for _, pagePath := range []string{"/", "/logs/api"} {
		resp, err := http.Get("http://" + ws.Addr() + pagePath)
		if err != nil {
			t.Fatalf("get %s: %v", pagePath, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read %s: %v", pagePath, err)
		}
		page := string(body)
		if !strings.Contains(page, "Tasks · one-shot") || strings.Contains(page, `data-group-section=`) {
			t.Fatalf("%s did not preserve the ungrouped layout:\n%s", pagePath, page)
		}
	}
}

func TestWebTailIncludesRawGroup(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{"api": {Command: "true", Group: "hub"}}})
	ws, err := startWebServer(m, "stacker.yml")
	if err != nil {
		t.Fatalf("start web server: %v", err)
	}
	defer ws.Close()

	resp, err := http.Get("http://" + ws.Addr() + "/api/api/tail?from=0")
	if err != nil {
		t.Fatalf("get process tail: %v", err)
	}
	defer resp.Body.Close()
	var response struct {
		Processes []ProcessInfo `json:"processes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("decode process tail: %v", err)
	}
	if len(response.Processes) != 1 || response.Processes[0].Group != "hub" {
		t.Fatalf("tail processes = %+v, want api in raw group hub", response.Processes)
	}
}

func TestWebGroupActionsReserveGroupsRoute(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  groups:
    command: sleep 60
    group: Other
  api:
    command: sleep 60
    group: hub
tasks:
  deploy:
    command: true
    group: hub
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
	t.Cleanup(m.stopAll)

	for _, action := range []string{"start", "restart", "stop"} {
		status, body, contentType := postWeb(t, ws, "/api/groups/"+action, `{"group":"hub"}`)
		if status != http.StatusOK {
			t.Fatalf("group %s: status=%d body=%s", action, status, body)
		}
		if !strings.HasPrefix(contentType, "application/json") {
			t.Fatalf("group %s content type = %q", action, contentType)
		}
		var response struct {
			OK       bool     `json:"ok"`
			Group    string   `json:"group"`
			Action   string   `json:"action"`
			Affected []string `json:"affected"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatalf("decode group %s response: %v", action, err)
		}
		if !response.OK || response.Group != "hub" || response.Action != action || !reflect.DeepEqual(response.Affected, []string{"api"}) {
			t.Fatalf("unexpected group %s response: %+v", action, response)
		}
	}
	waitFor(t, 3*time.Second, func() bool { return m.processByName("api").Status() == StatusStopped })
	status, body, _ := postWeb(t, ws, "/api/groups/start", `{"group":""}`)
	var otherResponse struct {
		Group    string   `json:"group"`
		Affected []string `json:"affected"`
	}
	if err := json.Unmarshal(body, &otherResponse); err != nil {
		t.Fatalf("decode empty group response: %v", err)
	}
	if status != http.StatusOK || otherResponse.Group != "Other" || !reflect.DeepEqual(otherResponse.Affected, []string{"groups"}) {
		t.Fatalf("empty group response: status=%d response=%+v body=%s", status, otherResponse, body)
	}
	status, body, _ = postWeb(t, ws, "/api/groups/stop", `{"group":" Other "}`)
	var stoppedOther struct {
		Affected []string `json:"affected"`
	}
	if err := json.Unmarshal(body, &stoppedOther); err != nil {
		t.Fatalf("decode stop Other response: %v", err)
	}
	if status != http.StatusOK || !reflect.DeepEqual(stoppedOther.Affected, []string{"groups"}) {
		t.Fatalf("stop Other response: status=%d body=%s", status, body)
	}

	status, body, contentType := postWeb(t, ws, "/api/groups/start", `{"group":"missing"}`)
	if status != http.StatusNotFound || !strings.HasPrefix(contentType, "application/json") || !strings.Contains(string(body), "unknown group") {
		t.Fatalf("unknown group response: status=%d content-type=%q body=%s", status, contentType, body)
	}
	status, body, contentType = postWeb(t, ws, "/api/groups/mark", `{"group":"hub"}`)
	if status != http.StatusBadRequest || !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("group mark response: status=%d content-type=%q body=%s", status, contentType, body)
	}
}

func TestWebGroupsProcessTailRoute(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{"groups": {Command: "sleep 60"}}})
	ws, err := startWebServer(m, "stacker.yml")
	if err != nil {
		t.Fatalf("start web server: %v", err)
	}
	t.Cleanup(func() {
		m.stopAll()
		ws.Close()
	})

	resp, err := http.Get("http://" + ws.Addr() + "/api/groups/tail")
	if err != nil {
		t.Fatalf("get configured groups process tail: %v", err)
	}
	defer resp.Body.Close()
	var response struct {
		OK        bool          `json:"ok"`
		Processes []ProcessInfo `json:"processes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("decode configured groups process tail: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !response.OK || len(response.Processes) != 1 || response.Processes[0].Name != "groups" {
		t.Fatalf("configured groups process tail: status=%d response=%+v", resp.StatusCode, response)
	}
}

func TestWebGroupsProcessBodylessStartRoute(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{"groups": {Command: "sleep 60"}}})
	ws, err := startWebServer(m, "stacker.yml")
	if err != nil {
		t.Fatalf("start web server: %v", err)
	}
	t.Cleanup(func() {
		m.stopAll()
		ws.Close()
	})

	resp, err := http.Post("http://"+ws.Addr()+"/api/groups/start", "application/json", nil)
	if err != nil {
		t.Fatalf("post bodyless configured groups process start: %v", err)
	}
	defer resp.Body.Close()
	var response struct {
		OK      bool        `json:"ok"`
		Process ProcessInfo `json:"process"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("decode configured groups process start: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !response.OK || response.Process.Name != "groups" {
		t.Fatalf("configured groups process start: status=%d response=%+v", resp.StatusCode, response)
	}
	waitFor(t, 3*time.Second, func() bool { return m.processByName("groups").Status() == StatusRunning })
}

func TestWebGroupsProcessBodyRoutes(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  groups:
    command: sleep 60
    color: "#38bdf8"
    group: hub
    tasks:
      hello: echo groups-task-output
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
	t.Cleanup(func() {
		m.stopAll()
		ws.Close()
	})

	resp, err := http.Get("http://" + ws.Addr() + "/api/groups/tail")
	if err != nil {
		t.Fatalf("get configured groups process tail: %v", err)
	}
	var tail struct {
		OK        bool          `json:"ok"`
		Processes []ProcessInfo `json:"processes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tail); err != nil {
		resp.Body.Close()
		t.Fatalf("decode configured groups process tail: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !tail.OK || len(tail.Processes) != 1 || tail.Processes[0].Name != "groups" {
		t.Errorf("configured groups process tail: status=%d response=%+v", resp.StatusCode, tail)
	}

	status, body, contentType := postWeb(t, ws, "/api/groups/color", `{"color":"#0af"}`)
	var colorResponse struct {
		OK    bool   `json:"ok"`
		Color string `json:"color"`
	}
	if err := json.Unmarshal(body, &colorResponse); err != nil {
		t.Errorf("decode configured groups process color: %v", err)
	}
	if status != http.StatusOK || !strings.HasPrefix(contentType, "application/json") || !colorResponse.OK || colorResponse.Color != "#0af" {
		t.Errorf("configured groups process color: status=%d content-type=%q response=%+v", status, contentType, colorResponse)
	}

	status, body, contentType = postWeb(t, ws, "/api/groups/group", `{"group":"runtime"}`)
	var groupResponse struct {
		OK      bool        `json:"ok"`
		Process ProcessInfo `json:"process"`
	}
	if err := json.Unmarshal(body, &groupResponse); err != nil {
		t.Errorf("decode configured groups process group: %v", err)
	}
	if status != http.StatusOK || !strings.HasPrefix(contentType, "application/json") || !groupResponse.OK || groupResponse.Process.Group != "runtime" {
		t.Errorf("configured groups process group: status=%d content-type=%q response=%+v", status, contentType, groupResponse)
	}
	if got := m.processByName("groups").Group(); got != "runtime" {
		t.Errorf("configured groups process group = %q, want runtime", got)
	}

	status, body, contentType = postWeb(t, ws, "/api/groups/task", `{"name":"hello"}`)
	var taskResponse struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(body, &taskResponse); err != nil {
		t.Errorf("decode configured groups process task: %v", err)
	}
	if status != http.StatusOK || !strings.HasPrefix(contentType, "application/json") || !taskResponse.OK {
		t.Errorf("configured groups process task: status=%d content-type=%q response=%+v", status, contentType, taskResponse)
	} else {
		waitFor(t, 3*time.Second, func() bool {
			return strings.Contains(strings.Join(m.processByName("groups").Logs(), "\n"), "[task hello] groups-task-output")
		})
	}
}

func TestWebGroupAssignmentPersistsServicesAndTasks(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
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

	for _, request := range []struct {
		name  string
		group string
		want  string
	}{
		{name: "api", group: `{"group":" release "}`, want: "release"},
		{name: "deploy", group: `{"group":"hub"}`, want: "hub"},
		{name: "api", group: `{"group":""}`},
		{name: "deploy", group: `{"group":""}`},
	} {
		status, body, contentType := postWeb(t, ws, "/api/"+request.name+"/group", request.group)
		if status != http.StatusOK || !strings.HasPrefix(contentType, "application/json") {
			t.Fatalf("set group for %s: status=%d content-type=%q body=%s", request.name, status, contentType, body)
		}
		if got := m.processByName(request.name).Group(); got != request.want {
			t.Fatalf("live group for %s = %q, want %q", request.name, got, request.want)
		}
		liveConfigGroup := m.cfg.Processes[request.name].Group
		if request.name == "deploy" {
			liveConfigGroup = m.cfg.Tasks[request.name].Group
		}
		if liveConfigGroup != request.want {
			t.Fatalf("live config group for %s = %q, want %q", request.name, liveConfigGroup, request.want)
		}
		updated, err := loadConfig(path)
		if err != nil {
			t.Fatalf("load updated config: %v", err)
		}
		got := updated.Processes[request.name].Group
		if request.name == "deploy" {
			got = updated.Tasks[request.name].Group
		}
		if got != request.want {
			t.Fatalf("persisted group for %s = %q, want %q", request.name, got, request.want)
		}
	}
}

func TestWebGroupAssignmentRejectsOrphans(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
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

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config before orphan changes: %v", err)
	}
	for _, request := range []struct {
		name  string
		group string
	}{
		{name: "api", group: `{"group":"release"}`},
		{name: "deploy", group: `{"group":"hub"}`},
	} {
		process := m.processByName(request.name)
		process.mu.Lock()
		process.orphaned = true
		process.mu.Unlock()
		status, body, contentType := postWeb(t, ws, "/api/"+request.name+"/group", request.group)
		if status != http.StatusBadRequest || !strings.HasPrefix(contentType, "application/json") || !strings.Contains(string(body), "orphaned") {
			t.Fatalf("orphan group mutation for %s: status=%d content-type=%q body=%s", request.name, status, contentType, body)
		}
		if process.Group() != map[string]string{"api": "hub", "deploy": "release"}[request.name] {
			t.Fatalf("orphan group mutation changed %s to %q", request.name, process.Group())
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read config after orphan change: %v", err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("orphan group mutation changed config for %s", request.name)
		}
	}
}

func TestWebOrderEndpointMixedMappings(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
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

	status, body, _ := postWeb(t, ws, "/api/order", `{"names":["backup","web","deploy","api"]}`)
	if status != http.StatusOK {
		t.Fatalf("mixed order: status=%d body=%s", status, body)
	}
	updated, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load reordered config: %v", err)
	}
	if !reflect.DeepEqual(updated.processOrder, []string{"web", "api"}) || !reflect.DeepEqual(updated.taskOrder, []string{"backup", "deploy"}) {
		t.Fatalf("mixed config order processes=%v tasks=%v", updated.processOrder, updated.taskOrder)
	}
	m.applyPendingOrder()
	var got []string
	for _, process := range m.processInfos() {
		got = append(got, process.Name)
	}
	if !reflect.DeepEqual(got, []string{"web", "api", "backup", "deploy"}) {
		t.Fatalf("mixed live order = %v", got)
	}

	m.processByName("web").orphaned = true
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config before orphan order: %v", err)
	}
	status, body, contentType := postWeb(t, ws, "/api/order", `{"names":["api","web","deploy","backup"]}`)
	if status != http.StatusBadRequest || !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("orphan mixed order: status=%d content-type=%q body=%s", status, contentType, body)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after orphan order: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("orphan mixed order changed config")
	}
	m.processByName("web").orphaned = false

	if err := os.WriteFile(path, []byte("version: 1\nprocesses:\n  api:\n    command: true\n  web:\n    command: true\ntasks:\n  fresh:\n    command: true\n"), 0o600); err != nil {
		t.Fatalf("rewrite task mapping: %v", err)
	}
	before, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config before invalid mapping order: %v", err)
	}
	status, body, contentType = postWeb(t, ws, "/api/order", `{"names":["api","web","deploy","backup"]}`)
	if status != http.StatusBadRequest || !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("stale task mapping order: status=%d content-type=%q body=%s", status, contentType, body)
	}
	after, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after invalid mapping order: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("invalid task mapping order partially changed config")
	}
}

func postWeb(t *testing.T, ws *webServer, path, body string) (int, []byte, string) {
	t.Helper()
	resp, err := http.Post("http://"+ws.Addr()+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read %s response: %v", path, err)
	}
	return resp.StatusCode, data, resp.Header.Get("Content-Type")
}
