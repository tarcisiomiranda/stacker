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
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Web log viewer: an on-demand HTTP server on 127.0.0.1 with a random high
// port. Off by default; the TUI toggles it with the `w` key, no restart needed.
// Routes:
//
//	GET /                  index with one link per process
//	GET /logs/{name}       HTML page with logs, copy button, auto-refresh
//	GET /logs/{name}/raw   text/plain logs (curl / easy copy)

//go:embed templates/*.html
var templateFS embed.FS

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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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

func (ws *webServer) Addr() string { return ws.listener.Addr().String() }

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
	_ = indexTemplate.Execute(w, map[string]any{"Config": ws.config, "Processes": ws.processRows("")})
}

type processRow struct {
	Name        string
	NameEscaped string
	Status      string
	Color       string
	Errors      int
	Current     bool
}

func (ws *webServer) processRows(current string) []processRow {
	rows := make([]processRow, 0, len(ws.m.processes))
	for _, p := range ws.m.processes {
		rows = append(rows, processRow{
			Name:        p.Name,
			NameEscaped: url.PathEscape(p.Name),
			Status:      string(p.Status()),
			Color:       p.Color(),
			Errors:      p.Errors(),
			Current:     p.Name == current,
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
		"Name":        p.Name,
		"NameEscaped": url.PathEscape(p.Name),
		"Status":      string(p.Status()),
		"Color":       p.Color(),
		"Errors":      p.Errors(),
		"Logs":        logs,
		"LogNext":     next,
		"WordWrap":    ws.m.cfg.UI.WordWrap,
		"Processes":   ws.processRows(p.Name),
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
	case "mark":
		p.Mark()
		ws.m.notify()
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "process": processInfo(p)})
}

// webLogsURL builds the browser URL for a process's log page.
func webLogsURL(addr, name string) string {
	return "http://" + addr + "/logs/" + url.PathEscape(name)
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
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}
