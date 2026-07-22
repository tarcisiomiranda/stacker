package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

func runCLI(configPath string, args []string) int {
	jsonOut := false
	var filtered []string
	for _, a := range args {
		if a == "--json" || a == "-json" {
			jsonOut = true
			continue
		}
		if a == "-h" || a == "--help" || a == "help" {
			printCLIHelp()
			return 0
		}
		filtered = append(filtered, a)
	}
	if len(filtered) == 0 {
		printCLIHelp()
		return 0
	}

	cmd := filtered[0]
	rest := filtered[1:]

	switch cmd {
	case "version":
		if jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"version": resolveVersion()})
		} else {
			fmt.Printf("stacker %s\n", resolveVersion())
		}
		return 0
	case "ping":
		return cliPing(configPath, jsonOut)
	case "list", "ls":
		return cliList(configPath, jsonOut)
	case "status":
		name := ""
		if len(rest) > 0 {
			name = rest[0]
		}
		return cliStatus(configPath, name, jsonOut)
	case "start":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: stacker start <name>")
			return 2
		}
		return cliAction(configPath, rest[0], "start", jsonOut)
	case "stop":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: stacker stop <name>")
			return 2
		}
		return cliAction(configPath, rest[0], "stop", jsonOut)
	case "restart":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: stacker restart <name>")
			return 2
		}
		return cliAction(configPath, rest[0], "restart", jsonOut)
	case "free-port", "freeport":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: stacker free-port <port>")
			return 2
		}
		return cliFreePort(configPath, rest[0], jsonOut)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printCLIHelp()
		return 2
	}
}

func printCLIHelp() {
	fmt.Print(`Stacker CLI — control a running Stacker TUI instance

Usage:
  stacker [-config path]                  Start the TUI (control plane)
  stacker [-config path] <command> [args] Talk to a running instance

Commands:
  ping                 Check if Stacker is running for this config
  list                 List configured processes and their status
  status [name]        Status of all processes, or one by name
  start <name>         Start a process (frees configured port first)
  stop <name>          Stop a process
  restart <name>       Stop then start a process
  free-port <port>     Kill whatever is listening on TCP port (no TUI required)
  version              Print the Stacker version (-v, --version)

Flags:
  -config path         Path to stacker.yml (default: stacker.yml)
  --json               Machine-readable JSON output (for AI agents)

Examples:
  stacker -config ./stacker.yml
  stacker list --json
  stacker restart backend
  stacker free-port 8000
`)
}

func cliPing(configPath string, jsonOut bool) int {
	st, err := findRunningInstance(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if st == nil {
		abs, _, _ := instanceID(configPath)
		if jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "running": false, "config": abs})
		} else {
			fmt.Printf("not running (%s)\n", abs)
		}
		return 1
	}
	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"ok": true, "running": true, "pid": st.PID, "addr": st.Addr, "config": st.Config,
		})
	} else {
		fmt.Printf("running pid=%d addr=%s\nconfig=%s\n", st.PID, st.Addr, st.Config)
	}
	return 0
}

func cliList(configPath string, jsonOut bool) int {
	client, st, err := newControlClient(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var resp struct {
		OK        bool          `json:"ok"`
		Error     string        `json:"error"`
		Processes []ProcessInfo `json:"processes"`
	}
	if err := client.get("/v1/processes", &resp); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if !resp.OK && resp.Error != "" {
		fmt.Fprintln(os.Stderr, "error:", resp.Error)
		return 1
	}
	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"ok": true, "config": st.Config, "processes": resp.Processes,
		})
		return 0
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tPORT")
	for _, p := range resp.Processes {
		port := "-"
		if p.Port > 0 {
			port = strconv.Itoa(p.Port)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", p.Name, p.Status, port)
	}
	_ = w.Flush()
	return 0
}

func cliStatus(configPath, name string, jsonOut bool) int {
	if name == "" {
		return cliList(configPath, jsonOut)
	}
	client, _, err := newControlClient(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var resp struct {
		OK      bool        `json:"ok"`
		Error   string      `json:"error"`
		Process ProcessInfo `json:"process"`
	}
	path := "/v1/processes/" + name + "/status"
	// status accepts GET
	if err := client.get(path, &resp); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintln(os.Stderr, "error:", resp.Error)
		return 1
	}
	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(resp)
		return 0
	}
	port := "-"
	if resp.Process.Port > 0 {
		port = strconv.Itoa(resp.Process.Port)
	}
	fmt.Printf("%s\t%s\tport=%s\n", resp.Process.Name, resp.Process.Status, port)
	return 0
}

func cliAction(configPath, name, action string, jsonOut bool) int {
	client, _, err := newControlClient(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var resp struct {
		OK      bool        `json:"ok"`
		Error   string      `json:"error"`
		Process ProcessInfo `json:"process"`
	}
	path := "/v1/processes/" + name + "/" + action
	if err := client.post(path, nil, &resp); err != nil {
		// Prefer body error when present
		if resp.Error != "" {
			fmt.Fprintln(os.Stderr, "error:", resp.Error)
			return 1
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintln(os.Stderr, "error:", resp.Error)
		return 1
	}
	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(resp)
		return 0
	}
	fmt.Printf("%s %s → %s\n", action, resp.Process.Name, resp.Process.Status)
	return 0
}

func cliFreePort(configPath, portStr string, jsonOut bool) int {
	port, err := parsePortArg(portStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}

	// Prefer asking the running instance (same machine, shared view); fall back local.
	if client, _, err := newControlClient(configPath); err == nil {
		var resp struct {
			OK     bool   `json:"ok"`
			Error  string `json:"error"`
			Port   int    `json:"port"`
			Killed []int  `json:"killed"`
		}
		if err := client.post("/v1/free-port", map[string]int{"port": port}, &resp); err == nil && resp.OK {
			return printFreePort(resp.Port, resp.Killed, jsonOut)
		}
	}

	killed, err := freePort(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return printFreePort(port, killed, jsonOut)
}

func printFreePort(port int, killed []int, jsonOut bool) int {
	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"ok": true, "port": port, "killed": killed,
		})
		return 0
	}
	if len(killed) == 0 {
		fmt.Printf("port %d: nothing listening\n", port)
		return 0
	}
	parts := make([]string, len(killed))
	for i, pid := range killed {
		parts[i] = strconv.Itoa(pid)
	}
	fmt.Printf("port %d: killed pid(s) %s\n", port, strings.Join(parts, ", "))
	return 0
}
