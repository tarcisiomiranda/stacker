package main

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
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
	Current     bool
}

func (ws *webServer) processRows(current string) []processRow {
	rows := make([]processRow, 0, len(ws.m.processes))
	for _, p := range ws.m.processes {
		rows = append(rows, processRow{
			Name:        p.Name,
			NameEscaped: url.PathEscape(p.Name),
			Status:      string(p.Status()),
			Color:       p.Config.Color,
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
	logs := strings.Join(p.Logs(), "\n")

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
		"Logs":        logs,
		"Processes":   ws.processRows(p.Name),
	})
}

// handleAction runs process actions from the web page:
// POST /api/{name}/restart and POST /api/{name}/mark.
func (ws *webServer) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		http.Error(w, "expected /api/{name}/{restart|mark}", http.StatusBadRequest)
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
	switch parts[1] {
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
