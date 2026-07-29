package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Web log viewer: an on-demand HTTP server. Default bind is 0.0.0.0:52911 so a
// Stacker on a remote machine is reachable from other hosts. Off by default;
// the TUI toggles it with the `w` key (no restart needed). Override with
// ui.web_host / ui.web_port in stacker.yml.
//
// Routes:
//
//	GET /                  index with one link per process
//	GET /logs/{name}       HTML page with logs, copy button, auto-refresh
//	GET /logs/{name}/raw   text/plain logs (curl / easy copy)

//go:embed templates/*.html
var templateFS embed.FS

const (
	defaultWebHost = "0.0.0.0"
	defaultWebPort = 52911
)

var (
	indexTemplate = template.Must(template.ParseFS(templateFS, "templates/index.html"))
	logsTemplate  = template.Must(template.ParseFS(templateFS, "templates/logs.html"))
)

type webServer struct {
	m        *model
	config   string
	server   *http.Server
	listener net.Listener
}

func startWebServer(m *model, config string) (*webServer, error) {
	ln, err := listenWeb(m.cfg.UI)
	if err != nil {
		return nil, err
	}
	ws := &webServer{m: m, config: config, listener: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleIndex)
	mux.HandleFunc("/logs/", ws.handleLogsPage)
	mux.HandleFunc("/api/", ws.handleAction)
	ws.server = &http.Server{Handler: mux}
	go func() { _ = ws.server.Serve(ln) }()
	return ws, nil
}

// listenWeb binds the preferred host:port, falling back to an ephemeral port
// on the same host when the preferred port is already taken.
func listenWeb(ui UIConfig) (net.Listener, error) {
	host := strings.TrimSpace(ui.WebHost)
	if host == "" {
		host = defaultWebHost
	}
	port := ui.WebPort
	if port == 0 {
		port = defaultWebPort
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, nil
	}
	// Preferred port busy: keep the host, pick any free port.
	fallback := net.JoinHostPort(host, "0")
	ln, err2 := net.Listen("tcp", fallback)
	if err2 != nil {
		return nil, fmt.Errorf("web listen %s: %w (fallback %s: %v)", addr, err, fallback, err2)
	}
	return ln, nil
}

func (ws *webServer) Addr() string { return ws.listener.Addr().String() }

// webPublicBaseURL returns an http://host:port suitable for pasting into a
// browser. When listening on 0.0.0.0 / ::, prefer the machine hostname so a
// remote client has a usable address instead of 0.0.0.0.
func webPublicBaseURL(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "http://" + listenAddr
	}
	switch host {
	case "0.0.0.0", "::", "[::]", "":
		if h, herr := os.Hostname(); herr == nil && h != "" {
			host = h
		} else {
			host = "127.0.0.1"
		}
	}
	return "http://" + net.JoinHostPort(host, port)
}

func (ws *webServer) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = ws.server.Shutdown(ctx)
}

func (ws *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTemplate.Execute(w, map[string]any{"Config": ws.config, "Processes": ws.processRows(""), "Tasks": ws.taskRows("")})
}

// taskEntry is one one-shot task rendered as a button on the logs page.
type taskEntry struct {
	Name    string
	Command string
}

func taskEntries(p *Process) []taskEntry {
	names := sortedTaskNames(p.Config.Tasks)
	entries := make([]taskEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, taskEntry{Name: name, Command: p.Config.Tasks[name]})
	}
	return entries
}

type processRow struct {
	Name        string
	NameEscaped string
	Status      string
	Color       string
	Errors      int
	Current     bool
	IsTask      bool
}

// processRows returns the service processes; taskRows returns the standalone
// one-shot tasks. Split so the web sidebar can make only processes draggable.
func (ws *webServer) processRows(current string) []processRow {
	return ws.rows(current, false)
}

func (ws *webServer) taskRows(current string) []processRow {
	return ws.rows(current, true)
}

func (ws *webServer) rows(current string, tasks bool) []processRow {
	procs := ws.m.procs()
	rows := make([]processRow, 0, len(procs))
	for _, p := range procs {
		if p.oneShot != tasks {
			continue
		}
		rows = append(rows, processRow{
			Name:        p.Name,
			NameEscaped: url.PathEscape(p.Name),
			Status:      oneShotStatusLabel(p, p.Status()),
			Color:       p.Color(),
			Errors:      p.Errors(),
			Current:     p.Name == current,
			IsTask:      p.oneShot,
		})
	}
	return rows
}

func (ws *webServer) handleLogsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/logs/"), "/"), "/")
	if len(parts) < 1 || parts[0] == "" || len(parts) > 2 {
		http.Error(w, "expected /logs/{name} or /logs/{name}/raw", http.StatusBadRequest)
		return
	}
	name, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, "invalid process name", http.StatusBadRequest)
		return
	}
	p := ws.m.processByName(name)
	if p == nil {
		http.Error(w, fmt.Sprintf("unknown process %q", name), http.StatusNotFound)
		return
	}
	// TailLogs(0) gives lines and the next offset in one consistent snapshot.
	_, lines, next := p.TailLogs(0)
	logs := strings.Join(lines, "\n")

	if len(parts) == 2 {
		if parts[1] != "raw" {
			http.Error(w, "expected /logs/{name}/raw", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, logs)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = logsTemplate.Execute(w, map[string]any{
		"Name":            p.Name,
		"NameEscaped":     url.PathEscape(p.Name),
		"Status":          oneShotStatusLabel(p, p.Status()),
		"Color":           p.Color(),
		"Errors":          p.Errors(),
		"Port":            p.Config.Port,
		"IsTask":          p.oneShot,
		"ProcTasks":       taskEntries(p),
		"Standalone":      ws.taskRows(p.Name),
		"Logs":            logs,
		"LogNext":         next,
		"WordWrap":        ws.m.cfg.UI.WordWrap,
		"HighlightErrors": ws.m.hlErr.Load(),
		"Processes":       ws.processRows(p.Name),
	})
}

// handleAction serves the web page's API:
// GET  /api/{name}/tail?from=N[&nolines=1]  incremental logs + all statuses
// POST /api/{name}/{start|stop|restart|mark}
func (ws *webServer) handleAction(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	// Global action: POST /api/mark-all marks every running process.
	if trimmed == "mark-all" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "marked": ws.m.markAllRunning()})
		return
	}

	// POST /api/order {"names": [...]} reorders the process list everywhere
	// (web sidebar, TUI, YAML file). Must be a permutation of all names.
	if trimmed == "order" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Names []string `json:"names"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json body"})
			return
		}
		if err := updateConfigOrder(ws.config, body.Names); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		ws.m.requestOrder(body.Names)
		writeJSON(w, map[string]any{"ok": true, "order": body.Names})
		return
	}

	// POST /api/highlight-errors {"enabled": bool} toggles error detection at
	// runtime and persists ui.highlight_errors in the YAML config.
	if trimmed == "highlight-errors" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json body"})
			return
		}
		if err := updateConfigUIFlag(ws.config, "highlight_errors", body.Enabled); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		ws.m.hlErr.Store(body.Enabled)
		for _, p := range ws.m.procs() {
			p.SetDetectErrors(body.Enabled)
		}
		ws.m.notify()
		writeJSON(w, map[string]any{"ok": true, "enabled": body.Enabled})
		return
	}

	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" {
		http.Error(w, "expected /api/{name}/{tail|start|stop|restart|mark}", http.StatusBadRequest)
		return
	}
	name, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, "invalid process name", http.StatusBadRequest)
		return
	}
	p := ws.m.processByName(name)
	if p == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "error": fmt.Sprintf("unknown process %q", name)})
		return
	}

	if parts[1] == "tail" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// nolines: statuses only (used while the page is frozen) — no copying.
		if r.URL.Query().Get("nolines") != "" {
			writeJSON(w, map[string]any{
				"ok": true, "next": p.LogNext(), "processes": ws.m.processInfos(),
			})
			return
		}
		from, _ := strconv.Atoi(r.URL.Query().Get("from"))
		start, lines, next := p.TailLogs(from)
		writeJSON(w, map[string]any{
			"ok": true, "from": start, "next": next, "lines": lines,
			"processes": ws.m.processInfos(),
		})
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// POST /api/{name}/color {"color": "#38bdf8"} — empty color removes it.
	// Updates the running process (TUI redraws) and rewrites the YAML config.
	if parts[1] == "color" {
		if p.oneShot {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "color is not editable for standalone tasks"})
			return
		}
		var body struct {
			Color string `json:"color"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json body"})
			return
		}
		color := strings.TrimSpace(body.Color)
		if color != "" && !validColor(color) {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": fmt.Sprintf("invalid color %q; use hex (#0af, #00aaff) or a CSS color name", color)})
			return
		}
		if err := updateConfigColor(ws.config, p.Name, color); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		p.SetColor(color)
		ws.m.notify()
		writeJSON(w, map[string]any{"ok": true, "color": color})
		return
	}

	switch parts[1] {
	case "start":
		go func() { _ = p.Start(ws.m.notify) }()
	case "stop":
		go func() { _ = p.Stop(ws.m.notify) }()
	case "restart":
		p.Restart(ws.m.notify)
	case "task":
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json body"})
			return
		}
		if err := p.RunTask(body.Name, ws.m.notify); err != nil {
			writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "task": body.Name})
		return
	case "mark":
		p.Mark()
		ws.m.notify()
	case "free-port":
		// Same behavior as the TUI `f` key: async, results go to the logs.
		if p.Config.Port <= 0 {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": fmt.Sprintf("process %q has no port configured", p.Name)})
			return
		}
		go func() {
			killed, err := freePort(p.Config.Port)
			switch {
			case err != nil:
				p.appendLog("[stacker] free-port failed: " + err.Error())
			case len(killed) == 0:
				p.appendLog(fmt.Sprintf("[stacker] port %d: nothing listening", p.Config.Port))
			default:
				p.appendLog(fmt.Sprintf("[stacker] freed port %d (killed pids=%v)", p.Config.Port, killed))
			}
			ws.m.notify()
		}()
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "process": processInfo(p)})
}

// webLogsURL builds the browser URL for a process's log page.
func webLogsURL(listenAddr, name string) string {
	return webPublicBaseURL(listenAddr) + "/logs/" + url.PathEscape(name)
}

// canOpenBrowser reports whether a desktop browser launch is plausible.
// Headless/SSH sessions (no DISPLAY / WAYLAND_DISPLAY on Linux) skip xdg-open
// so the TUI status line is not polluted with a expected failure.
func canOpenBrowser() bool {
	switch runtime.GOOS {
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return false
		}
	case "darwin", "windows":
		return true
	}
	return true
}

// openBrowser opens url with the platform's default browser.
func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		if _, err := exec.LookPath("xdg-open"); err != nil {
			return fmt.Errorf("xdg-open not found")
		}
		cmd = exec.Command("xdg-open", target)
	}
	// Discard browser helper output; failures still return via Start/Wait.
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}
