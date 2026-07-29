package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// InstanceState is written while the supervisor (session or serve) is running
// so the CLI and AI agents can discover and talk to it.
type InstanceState struct {
	PID       int    `json:"pid"`
	Config    string `json:"config"`
	Addr      string `json:"addr"`
	StartedAt string `json:"started_at"`
	// Mode is "session" (TUI+supervisor) or "serve" (headless supervisor).
	Mode string `json:"mode,omitempty"`
}

// ProcessInfo is the JSON shape returned by the control API and CLI.
type ProcessInfo struct {
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Port     int      `json:"port,omitempty"`
	Color    string   `json:"color,omitempty"`
	Errors   int      `json:"errors,omitempty"`
	Tasks    []string `json:"tasks,omitempty"`
	OneShot  bool     `json:"one_shot,omitempty"`
	Orphaned bool     `json:"orphaned,omitempty"`
}

type controlServer struct {
	m         *model
	config    string
	statePath string
	server    *http.Server
	listener  net.Listener
}

func runtimeDir() (string, error) {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "stacker"), nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		return filepath.Join(home, ".cache", "stacker", "run"), nil
	}
	return filepath.Join(cache, "stacker", "run"), nil
}

func instanceID(configPath string) (string, string, error) {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return "", "", err
	}
	abs = filepath.Clean(abs)
	sum := sha256.Sum256([]byte(abs))
	return abs, hex.EncodeToString(sum[:8]), nil
}

func statePathFor(configPath string) (absConfig, path string, err error) {
	absConfig, id, err := instanceID(configPath)
	if err != nil {
		return "", "", err
	}
	dir, err := runtimeDir()
	if err != nil {
		return "", "", err
	}
	return absConfig, filepath.Join(dir, id+".json"), nil
}

func readInstanceState(configPath string) (*InstanceState, string, error) {
	absConfig, path, err := statePathFor(configPath)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, absConfig, err
	}
	var st InstanceState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, absConfig, err
	}
	return &st, absConfig, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return processSignalAlive(proc)
}

func findRunningInstance(configPath string) (*InstanceState, error) {
	st, _, err := readInstanceState(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !processAlive(st.PID) {
		_, path, _ := statePathFor(configPath)
		_ = os.Remove(path)
		return nil, nil
	}
	// Confirm control plane responds.
	if err := controlPing(st.Addr, 300*time.Millisecond); err != nil {
		return nil, nil
	}
	return st, nil
}

// listRunningInstances scans the runtime state dir for live control planes.
func listRunningInstances() ([]InstanceState, error) {
	dir, err := runtimeDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []InstanceState
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var st InstanceState
		if err := json.Unmarshal(data, &st); err != nil {
			continue
		}
		if !processAlive(st.PID) {
			_ = os.Remove(path)
			continue
		}
		if err := controlPing(st.Addr, 300*time.Millisecond); err != nil {
			continue
		}
		out = append(out, st)
	}
	// Stable order for multi-instance hints.
	sort.Slice(out, func(i, j int) bool { return out[i].Config < out[j].Config })
	return out, nil
}

// resolveRunningInstance finds the instance for configPath. If that config has
// no supervisor but exactly one Stacker is running on this machine, it returns
// that one so `stacker a` / `stacker list` work after `serve -d` with a custom
// --config. Multiple running instances still require an explicit --config.
func resolveRunningInstance(configPath string) (st *InstanceState, usedConfig string, fallback bool, err error) {
	st, err = findRunningInstance(configPath)
	if err != nil {
		return nil, configPath, false, err
	}
	if st != nil {
		return st, configPath, false, nil
	}
	all, err := listRunningInstances()
	if err != nil {
		return nil, configPath, false, err
	}
	if len(all) == 0 {
		return nil, configPath, false, nil
	}
	if len(all) == 1 {
		only := all[0]
		return &only, only.Config, true, nil
	}
	return nil, configPath, false, errMultipleInstances(configPath, all)
}

func controlPing(addr string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get("http://" + addr + "/v1/ping")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping status %d", resp.StatusCode)
	}
	return nil
}

func startControlServer(m *model, configPath string) (*controlServer, error) {
	absConfig, path, err := statePathFor(configPath)
	if err != nil {
		return nil, err
	}
	if st, err := findRunningInstance(configPath); err != nil {
		return nil, err
	} else if st != nil {
		return nil, fmt.Errorf("stacker already running for %s (pid %d, addr %s)", absConfig, st.PID, st.Addr)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := ln.Addr().String()

	cs := &controlServer{
		m:         m,
		config:    absConfig,
		statePath: path,
		listener:  ln,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ping", cs.handlePing)
	mux.HandleFunc("/v1/processes", cs.handleProcesses)
	mux.HandleFunc("/v1/processes/", cs.handleProcessAction)
	mux.HandleFunc("/v1/free-port", cs.handleFreePort)
	mux.HandleFunc("/v1/tasks/", cs.handleTaskAction)
	mux.HandleFunc("/v1/mark-all", cs.handleMarkAll)
	mux.HandleFunc("/v1/order", cs.handleOrder)
	mux.HandleFunc("/v1/highlight-errors", cs.handleHighlightErrors)
	mux.HandleFunc("/v1/down", cs.handleDown)

	cs.server = &http.Server{Handler: mux}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		_ = ln.Close()
		return nil, err
	}
	mode := m.mode
	if mode == "" {
		mode = "session"
	}
	st := InstanceState{
		PID:       os.Getpid(),
		Config:    absConfig,
		Addr:      addr,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Mode:      mode,
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}

	go func() { _ = cs.server.Serve(ln) }()
	return cs, nil
}

func (cs *controlServer) Close() {
	if cs == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cs.server.Shutdown(ctx)
	_ = os.Remove(cs.statePath)
}

func (cs *controlServer) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mode := cs.m.mode
	if mode == "" {
		mode = "session"
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"config":  cs.config,
		"pid":     os.Getpid(),
		"version": resolveVersion(),
		"mode":    mode,
	})
}

func (cs *controlServer) handleProcesses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "processes": cs.m.processInfos()})
}

func (cs *controlServer) handleProcessAction(w http.ResponseWriter, r *http.Request) {
	// /v1/processes/{name}/{action}
	path := strings.TrimPrefix(r.URL.Path, "/v1/processes/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "expected /v1/processes/{name}/{start|stop|restart|status|logs|mark|color|free-port}", http.StatusBadRequest)
		return
	}
	name, err := url.PathUnescape(parts[0])
	if err != nil || name == "" {
		http.Error(w, "invalid process name", http.StatusBadRequest)
		return
	}
	action := parts[1]
	if action == "logs" || action == "status" {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	} else if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	p := cs.m.processByName(name)
	if p == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "error": fmt.Sprintf("unknown process %q", name)})
		return
	}

	switch action {
	case "status":
		writeJSON(w, map[string]any{"ok": true, "process": processInfo(p)})
	case "logs":
		from := 0
		if v := r.URL.Query().Get("from"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				from = n
			}
		}
		start, lines, next := p.TailLogs(from)
		writeJSON(w, map[string]any{
			"ok": true, "from": start, "next": next, "lines": lines,
			"processes": cs.m.processInfos(),
		})
	case "start":
		if err := p.Start(cs.m.notify); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "process": processInfo(p)})
	case "stop":
		if err := p.Stop(cs.m.notify); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "process": processInfo(p)})
	case "restart":
		if err := p.Stop(cs.m.notify); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "stop: " + err.Error()})
			return
		}
		if err := p.Start(cs.m.notify); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "start: " + err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "process": processInfo(p)})
	case "mark":
		p.Mark()
		cs.m.notify()
		writeJSON(w, map[string]any{"ok": true, "process": processInfo(p)})
	case "free-port":
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
			cs.m.notify()
		}()
		writeJSON(w, map[string]any{"ok": true, "process": processInfo(p)})
	case "color":
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
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": fmt.Sprintf("invalid color %q", color)})
			return
		}
		if err := updateConfigColor(cs.config, p.Name, color); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		p.SetColor(color)
		cs.m.notify()
		writeJSON(w, map[string]any{"ok": true, "color": color})
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

func (cs *controlServer) handleMarkAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "marked": cs.m.markAllRunning()})
}

func (cs *controlServer) handleOrder(w http.ResponseWriter, r *http.Request) {
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
	if err := updateConfigOrder(cs.config, body.Names); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	cs.m.requestOrder(body.Names)
	writeJSON(w, map[string]any{"ok": true, "order": body.Names})
}

func (cs *controlServer) handleHighlightErrors(w http.ResponseWriter, r *http.Request) {
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
	if err := updateConfigUIFlag(cs.config, "highlight_errors", body.Enabled); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	cs.m.hlErr.Store(body.Enabled)
	for _, p := range cs.m.procs() {
		p.SetDetectErrors(body.Enabled)
	}
	cs.m.notify()
	writeJSON(w, map[string]any{"ok": true, "enabled": body.Enabled})
}

// handleDown stops every process and signals the supervisor to exit.
func (cs *controlServer) handleDown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "stopping": true})
	go func() {
		cs.m.stopAll()
		cs.m.requestShutdown()
	}()
}

// handleTaskAction runs a one-shot task: POST /v1/tasks/{proc}/{task}.
func (cs *controlServer) handleTaskAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
	parts := strings.SplitN(strings.Trim(path, "/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "expected /v1/tasks/{process}/{task}", http.StatusBadRequest)
		return
	}
	p := cs.m.processByName(parts[0])
	if p == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "error": fmt.Sprintf("unknown process %q", parts[0])})
		return
	}
	if err := p.RunTask(parts[1], cs.m.notify); err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "process": parts[0], "task": parts[1]})
}

func (cs *controlServer) handleFreePort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Port int `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json body"})
		return
	}
	killed, err := freePort(body.Port)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "port": body.Port, "killed": killed})
}

func (m *model) processByName(name string) *Process {
	for _, p := range m.procs() {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func (m *model) processInfos() []ProcessInfo {
	procs := m.procs()
	out := make([]ProcessInfo, 0, len(procs))
	for _, p := range procs {
		out = append(out, processInfo(p))
	}
	return out
}

func processInfo(p *Process) ProcessInfo {
	st := p.Status()
	label := string(st)
	if p.oneShot && st == StatusStopped {
		label = "idle"
	}
	return ProcessInfo{
		Name:     p.Name,
		Status:   label,
		Port:     p.Config.Port,
		Color:    p.Color(),
		Errors:   p.Errors(),
		Tasks:    sortedTaskNames(p.Config.Tasks),
		OneShot:  p.oneShot,
		Orphaned: p.orphaned,
	}
}

// requestShutdown asks the supervisor (session TUI or serve loop) to exit.
func (m *model) requestShutdown() {
	select {
	case <-m.shutdown:
	default:
		close(m.shutdown)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// --- CLI client helpers ---

type controlClient struct {
	addr   string
	client *http.Client
}

func newControlClient(configPath string) (*controlClient, *InstanceState, error) {
	st, used, fallback, err := resolveRunningInstance(configPath)
	if err != nil {
		return nil, nil, err
	}
	if st == nil {
		return nil, nil, errNoRunningInstance(configPath)
	}
	if fallback {
		// Human-readable note on stderr so --json stdout stays clean.
		fmt.Fprintf(os.Stderr, "note: no instance for %s; using only running config %s\n", configPath, st.Config)
	}
	_ = used
	return &controlClient{
		addr: st.Addr,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}, st, nil
}

// errNoRunningInstance explains that no supervisor is up for this config and
// shows the same --config path in every example so it is obvious which YAML
// to pass (attach/list/down must use the identical path as serve/session).
func errNoRunningInstance(configPath string) error {
	abs, _, _ := instanceID(configPath)
	cfg := configPath
	if strings.TrimSpace(cfg) == "" {
		cfg = "stacker.yml"
	}
	// Quote when the path has spaces so copy-paste works.
	flag := "--config " + shellQuote(cfg)
	var b strings.Builder
	fmt.Fprintf(&b, "no running Stacker for %s\n", cfg)
	if abs != "" && abs != cfg {
		fmt.Fprintf(&b, "(resolved path: %s)\n", abs)
	}

	// If other instances are up, list them first — this is usually what the
	// user hit (forgot --config after serve -d with a custom YAML).
	if all, err := listRunningInstances(); err == nil && len(all) > 0 {
		b.WriteString("\n")
		fmt.Fprintf(&b, "Other running instance(s) on this machine:\n")
		for _, st := range all {
			mode := orDefault(st.Mode, "session")
			fmt.Fprintf(&b, "  %s  (pid %d, mode %s)\n", st.Config, st.PID, mode)
			fmt.Fprintf(&b, "    stacker --config %s a\n", shellQuote(st.Config))
			fmt.Fprintf(&b, "    stacker --config %s list\n", shellQuote(st.Config))
		}
		b.WriteString("\nOr, if only one is running, plain `stacker a` / `stacker list` should pick it up.\n")
		return errors.New(b.String())
	}

	b.WriteString("\n")
	b.WriteString("Start a supervisor with that same config, then use CLI/attach:\n")
	fmt.Fprintf(&b, "  stacker %s serve -d    # headless in background\n", flag)
	fmt.Fprintf(&b, "  stacker %s a           # TUI attach (short for attach)\n", flag)
	fmt.Fprintf(&b, "  stacker %s             # session mode (TUI owns processes)\n", flag)
	b.WriteString("\n")
	b.WriteString("Examples (always pass the same --config):\n")
	fmt.Fprintf(&b, "  stacker %s list --json\n", flag)
	fmt.Fprintf(&b, "  stacker %s restart backend\n", flag)
	fmt.Fprintf(&b, "  stacker %s down\n", flag)
	fmt.Fprintf(&b, "  stacker free-port 8000\n")
	fmt.Fprintf(&b, "  stacker %s run backend migrate\n", flag)
	return errors.New(b.String())
}

// errMultipleInstances is returned when the requested config is not running
// but more than one other Stacker is up — the user must pick with --config.
func errMultipleInstances(requested string, all []InstanceState) error {
	var b strings.Builder
	fmt.Fprintf(&b, "no running Stacker for %s, and %d other instances are up:\n\n", requested, len(all))
	for _, st := range all {
		mode := st.Mode
		if mode == "" {
			mode = "session"
		}
		fmt.Fprintf(&b, "  %s  (pid %d, mode %s)\n", st.Config, st.PID, mode)
		fmt.Fprintf(&b, "    stacker --config %s a\n", shellQuote(st.Config))
		fmt.Fprintf(&b, "    stacker --config %s list\n", shellQuote(st.Config))
	}
	b.WriteString("\nPass --config to choose one.\n")
	return errors.New(b.String())
}

// shellQuote returns s if it is a simple path, otherwise single-quotes it.
func shellQuote(s string) string {
	if s == "" {
		return "stacker.yml"
	}
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '"' || r == '\'' || r == '$' || r == '`' {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

func (c *controlClient) get(path string, out any) error {
	resp, err := c.client.Get("http://" + c.addr + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeControlResponse(resp, out)
}

func (c *controlClient) post(path string, body any, out any) error {
	var (
		req *http.Request
		err error
	)
	if body != nil {
		data, mErr := json.Marshal(body)
		if mErr != nil {
			return mErr
		}
		req, err = http.NewRequest(http.MethodPost, "http://"+c.addr+path, strings.NewReader(string(data)))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(http.MethodPost, "http://"+c.addr+path, nil)
		if err != nil {
			return err
		}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeControlResponse(resp, out)
}

func decodeControlResponse(resp *http.Response, out any) error {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return err
		}
	}
	if resp.StatusCode >= 400 {
		var er struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &er)
		if er.Error != "" {
			return errors.New(er.Error)
		}
		return fmt.Errorf("control API status %d", resp.StatusCode)
	}
	return nil
}

func parsePortArg(s string) (int, error) {
	port, err := strconv.Atoi(s)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return port, nil
}
