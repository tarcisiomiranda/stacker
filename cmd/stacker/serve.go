package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// runServe starts a headless supervisor: control plane + processes, no TUI.
// background re-execs into a daemon child; withWeb starts the log viewer.
func runServe(configPath string, background, withWeb bool) int {
	if background && os.Getenv("STACKER_DAEMON_CHILD") != "1" {
		return daemonizeServe(configPath, withWeb)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		return 1
	}

	m := newModel(cfg)
	m.mode = "serve"
	control, err := startControlServer(m, configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "control plane error:", err)
		return 1
	}
	defer control.Close()
	m.configPath = control.config
	m.startConfigWatcher()
	defer m.stopConfigWatcher()

	// Autostart processes (same as TUI Init).
	for _, p := range m.processes {
		if p.Config.Autostart {
			go func(proc *Process) { _ = proc.Start(m.notify) }(p)
		}
	}

	// Drain refresh notifications: apply config reloads and prune orphans.
	go func() {
		for {
			select {
			case <-m.shutdown:
				return
			case <-m.refreshCh:
				m.applyPendingOrder()
				m.applyPendingConfig()
				m.pruneOrphans()
			}
		}
	}()

	if withWeb {
		ws, err := startWebServer(m, m.configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "web server error:", err)
		} else {
			m.web = ws
			target := webPublicBaseURL(ws.Addr()) + "/"
			if len(m.processes) > 0 {
				target = webLogsURL(ws.Addr(), m.processes[0].Name)
			}
			fmt.Fprintf(os.Stderr, "web logs: %s (listen %s)\n", target, ws.Addr())
		}
	}

	if os.Getenv("STACKER_DAEMON_CHILD") == "1" {
		fmt.Fprintf(os.Stderr, "stacker serve running (pid %d, mode serve)\n", os.Getpid())
	} else {
		fmt.Fprintf(os.Stderr, "stacker serve (pid %d) — control plane on %s\n", os.Getpid(), control.listener.Addr().String())
		fmt.Fprintln(os.Stderr, "press Ctrl+C or run: stacker down")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case <-sigCh:
	case <-m.shutdown:
	}

	if m.web != nil {
		m.web.Close()
	}
	m.stopAll()
	return 0
}

func daemonLogPath(configPath string) string {
	_, id, err := instanceID(configPath)
	if err != nil {
		return filepath.Join(os.TempDir(), "stacker-serve.log")
	}
	dir, err := runtimeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "stacker-"+id+".log")
	}
	return filepath.Join(dir, id+".log")
}

func printServeStarted(configPath string, pid int, logPath string) {
	cfgFlag := "--config " + shellQuote(configPath)
	fmt.Printf("stacker serve started in background (pid %d)\n", pid)
	fmt.Printf("config: %s\n", configPath)
	fmt.Printf("log: %s\n", logPath)
	fmt.Printf("attach: stacker %s a\n", cfgFlag)
	fmt.Printf("        stacker a   # ok when this is the only running instance\n")
	fmt.Printf("stop:   stacker %s down\n", cfgFlag)
}

func printServeAlreadyRunning(configPath string, st *InstanceState) {
	cfgFlag := "--config " + shellQuote(configPath)
	fmt.Printf("stacker already running for %s (pid %d, mode %s)\n", st.Config, st.PID, orDefault(st.Mode, "session"))
	fmt.Printf("attach: stacker %s a\n", cfgFlag)
	fmt.Printf("        stacker a   # ok when this is the only running instance\n")
	fmt.Printf("list:   stacker %s list\n", cfgFlag)
	fmt.Printf("stop:   stacker %s down\n", cfgFlag)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// tailFile returns the last n lines of path (best-effort).
func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	// Drop trailing empty from final newline.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

// runDown stops a running supervisor (session or serve) via the control plane.
func runDown(configPath string, jsonOut bool) int {
	client, st, err := newControlClient(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var resp struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Stopping bool   `json:"stopping"`
	}
	if err := client.post("/v1/down", nil, &resp); err != nil {
		if resp.Error != "" {
			fmt.Fprintln(os.Stderr, "error:", resp.Error)
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return 1
	}
	// Wait briefly for the process to exit and clear the state file.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(st.PID) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if jsonOut {
		fmt.Println(`{"ok":true,"stopped":true}`)
		return 0
	}
	fmt.Println("stacker stopped")
	return 0
}
