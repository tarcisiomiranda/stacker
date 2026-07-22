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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// InstanceState is written while the TUI (control plane) is running so the CLI
// and AI agents can discover and talk to it.
type InstanceState struct {
	PID       int    `json:"pid"`
	Config    string `json:"config"`
	Addr      string `json:"addr"`
	StartedAt string `json:"started_at"`
}

// ProcessInfo is the JSON shape returned by the control API and CLI.
type ProcessInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Port   int    `json:"port,omitempty"`
	Color  string `json:"color,omitempty"`
	Errors int    `json:"errors,omitempty"`
}

type controlServer struct {
	m        *model
	config   string
	statePath string
	server   *http.Server
	listener net.Listener
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

	cs.server = &http.Server{Handler: mux}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		_ = ln.Close()
		return nil, err
	}
	st := InstanceState{
		PID:       os.Getpid(),
		Config:    absConfig,
		Addr:      addr,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
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
	writeJSON(w, map[string]any{"ok": true, "config": cs.config, "pid": os.Getpid(), "version": resolveVersion()})
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
		http.Error(w, "expected /v1/processes/{name}/{start|stop|restart|status}", http.StatusBadRequest)
		return
	}
	name, action := parts[0], parts[1]
	if r.Method != http.MethodPost && action != "status" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if action == "status" && r.Method != http.MethodGet && r.Method != http.MethodPost {
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
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
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
	for _, p := range m.processes {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func (m *model) processInfos() []ProcessInfo {
	out := make([]ProcessInfo, 0, len(m.processes))
	for _, p := range m.processes {
		out = append(out, processInfo(p))
	}
	return out
}

func processInfo(p *Process) ProcessInfo {
	return ProcessInfo{
		Name:   p.Name,
		Status: string(p.Status()),
		Port:   p.Config.Port,
		Color:  p.Color(),
		Errors: p.Errors(),
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
	st, err := findRunningInstance(configPath)
	if err != nil {
		return nil, nil, err
	}
	if st == nil {
		abs, _, _ := instanceID(configPath)
		return nil, nil, fmt.Errorf("no running Stacker for %s\nstart the TUI first: stacker -config %s", abs, configPath)
	}
	return &controlClient{
		addr: st.Addr,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}, st, nil
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
