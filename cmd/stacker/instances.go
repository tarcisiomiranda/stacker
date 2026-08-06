package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// instanceSummary is one row in the picker, the `instances` command, and the
// no-TTY listing. Ports/Up/Total come from querying the instance's control
// plane; Err records why a live instance could not be described.
type instanceSummary struct {
	State InstanceState
	Label string
	Ports []int
	Up    int
	Total int
	// Collide are ports this instance declares that another live instance also
	// declares — the setup where starting one silently kills the other's service.
	Collide []int
	Err     string
}

// labelInstances names each instance for display. The basename of the config's
// directory is what users actually call their projects; when two of them match
// (several repos with a frontend/ directory), both grow a second segment so the
// label stays unique — an ambiguous label defeats the whole purpose.
func labelInstances(states []InstanceState) []string {
	labels := make([]string, len(states))
	counts := map[string]int{}
	for i, st := range states {
		labels[i] = configLabel(st.Config, 1)
		counts[labels[i]]++
	}
	for i, st := range states {
		if counts[labels[i]] > 1 {
			labels[i] = configLabel(st.Config, 2)
		}
	}
	return labels
}

// configLabel takes the last n directory segments of a config path. It never
// returns empty: a config sitting at the filesystem root falls back to the file
// name itself.
func configLabel(configPath string, segments int) string {
	dir := filepath.Dir(filepath.Clean(configPath))
	parts := []string{}
	for i := 0; i < segments; i++ {
		base := filepath.Base(dir)
		if base == "." || base == string(filepath.Separator) || base == "" {
			break
		}
		parts = append([]string{base}, parts...)
		dir = filepath.Dir(dir)
	}
	if len(parts) == 0 {
		return filepath.Base(filepath.Clean(configPath))
	}
	return strings.Join(parts, string(filepath.Separator))
}

// collectInstanceSummaries describes every live instance by querying its control
// plane. An instance that fails to answer is still listed, with Err set: hiding
// it would be worse than showing it degraded, since it still holds ports.
func collectInstanceSummaries(all []InstanceState) []instanceSummary {
	labels := labelInstances(all)
	rows := make([]instanceSummary, 0, len(all))
	for i, st := range all {
		row := instanceSummary{State: st, Label: labels[i]}
		client := &controlClient{
			addr:   st.Addr,
			client: &http.Client{Timeout: 2 * time.Second},
		}
		var resp struct {
			OK        bool          `json:"ok"`
			Processes []ProcessInfo `json:"processes"`
		}
		if err := client.get("/v1/processes", &resp); err != nil {
			row.Err = err.Error()
			rows = append(rows, row)
			continue
		}
		for _, pi := range resp.Processes {
			// One-shot tasks are not services; counting them would make the
			// "n/total up" ratio meaningless.
			if pi.OneShot {
				continue
			}
			row.Total++
			if pi.Status == string(StatusRunning) {
				row.Up++
			}
			if pi.Port > 0 {
				row.Ports = append(row.Ports, pi.Port)
			}
		}
		rows = append(rows, row)
	}
	markCollisions(rows)
	return rows
}

// markCollisions flags ports declared by more than one instance — the setup
// where starting a process in one project makes free-port terminate another
// project's service. A port repeated inside a single config is that config's own
// business and is not flagged.
func markCollisions(rows []instanceSummary) {
	owners := map[int]map[int]struct{}{}
	for i, row := range rows {
		for _, port := range row.Ports {
			if port <= 0 {
				continue
			}
			if owners[port] == nil {
				owners[port] = map[int]struct{}{}
			}
			owners[port][i] = struct{}{}
		}
	}
	for i := range rows {
		var collide []int
		seen := map[int]struct{}{}
		for _, port := range rows[i].Ports {
			if _, dup := seen[port]; dup {
				continue
			}
			seen[port] = struct{}{}
			if len(owners[port]) > 1 {
				collide = append(collide, port)
			}
		}
		sort.Ints(collide)
		rows[i].Collide = collide
	}
}

// formatInstanceList renders the plain-text listing used by `stacker instances`
// and by the picker when there is no TTY to draw on.
func formatInstanceList(rows []instanceSummary, localConfig string) string {
	var b strings.Builder
	if len(rows) == 0 {
		b.WriteString("No Stacker instance is running.\n")
	} else {
		fmt.Fprintf(&b, "%d instance(s) running:\n\n", len(rows))
	}
	for _, row := range rows {
		mode := orDefault(row.State.Mode, "session")
		fmt.Fprintf(&b, "  %s  (pid %d, %s)\n", row.Label, row.State.PID, mode)
		fmt.Fprintf(&b, "    config: %s\n", row.State.Config)
		if row.Err != "" {
			fmt.Fprintf(&b, "    unreachable: %s\n", row.Err)
		} else {
			fmt.Fprintf(&b, "    processes: %d/%d up\n", row.Up, row.Total)
			if len(row.Ports) > 0 {
				fmt.Fprintf(&b, "    ports: %s\n", joinInts(row.Ports))
			}
		}
		if len(row.Collide) > 0 {
			fmt.Fprintf(&b, "    warning: port(s) %s also declared by another instance;\n", joinInts(row.Collide))
			b.WriteString("             starting one there terminates the listener here\n")
		}
		fmt.Fprintf(&b, "    attach: stacker --config %s a\n", shellQuote(row.State.Config))
	}
	if strings.TrimSpace(localConfig) != "" {
		b.WriteString("\nTo start the config in this directory:\n")
		fmt.Fprintf(&b, "  stacker --config %s\n", shellQuote(localConfig))
	}
	return b.String()
}

// instancesJSON is the machine-readable shape of the instance list. Scripts get
// the port collisions too: that is the one fact they cannot work out alone.
// Empty port lists serialize as [] rather than null so consumers can iterate.
func instancesJSON(rows []instanceSummary) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		ports := row.Ports
		if ports == nil {
			ports = []int{}
		}
		collide := row.Collide
		if collide == nil {
			collide = []int{}
		}
		entry := map[string]any{
			"label":           row.Label,
			"config":          row.State.Config,
			"pid":             row.State.PID,
			"mode":            orDefault(row.State.Mode, "session"),
			"addr":            row.State.Addr,
			"up":              row.Up,
			"total":           row.Total,
			"ports":           ports,
			"port_collisions": collide,
		}
		if row.Err != "" {
			entry["error"] = row.Err
		}
		out = append(out, entry)
	}
	return out
}

// cliInstances lists every live supervisor on this machine. `list` keeps meaning
// processes, so instances get their own command instead of overloading it.
func cliInstances(configPath string, jsonOut bool) int {
	all, err := listRunningInstances()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	rows := collectInstanceSummaries(all)

	if jsonOut {
		payload := map[string]any{"ok": true, "instances": instancesJSON(rows)}
		if err := json.NewEncoder(os.Stdout).Encode(payload); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		return 0
	}

	local := ""
	if configExists(configPath) {
		if abs, _, err := instanceID(configPath); err == nil {
			local = abs
		} else {
			local = configPath
		}
	}
	fmt.Print(formatInstanceList(rows, local))
	if len(rows) == 0 {
		return 1
	}
	return 0
}

// joinInts renders a port list for humans.
func joinInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, " ")
}
