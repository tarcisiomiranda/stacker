package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// launchMode is what a TUI invocation of stacker resolves to.
type launchMode int

const (
	// launchSession starts a supervisor for the target config in this process.
	launchSession launchMode = iota
	// launchAttach connects a TUI to an already running supervisor.
	launchAttach
	// launchPicker lets the user choose among live instances.
	launchPicker
	// launchNoInstance means there is nothing to attach to and starting one is
	// not allowed (the `attach` subcommand).
	launchNoInstance
)

// resolveLaunch decides what a TUI invocation resolves to, given the facts
// about what is running. It never touches the filesystem or network so the
// whole decision table is testable.
//
// target is the supervisor for configPath, or nil when that config has none.
// others are live supervisors for different configs. canStart is false for the
// `attach` subcommand, which must never bring a supervisor up.
//
// The ordering is the point: an explicitly named config outranks any other
// running instance. Falling back to an unrelated instance in that case is what
// made `stacker --config stacker.yml` open a different project.
func resolveLaunch(configPath string, explicit, canStart bool, target *InstanceState, others []InstanceState) (launchMode, string) {
	// A supervisor for exactly this config always wins, session or serve.
	if target != nil {
		return launchAttach, target.Config
	}
	if explicit {
		// The user named a config; honour it or say nothing is there.
		if canStart {
			return launchSession, configPath
		}
		return launchNoInstance, configPath
	}
	// No config was named, so offer whatever is running instead of guessing.
	if len(others) > 0 {
		return launchPicker, configPath
	}
	if canStart {
		return launchSession, configPath
	}
	return launchNoInstance, configPath
}

// runTUI resolves a bare `stacker` (canStart) or `stacker attach` (!canStart)
// against what is actually running.
func runTUI(configPath string, explicit, canStart bool) int {
	all, err := listRunningInstances()
	if err != nil {
		// Trouble discovering instances must not stop someone from starting
		// their own stack; the worst case is a missing warning.
		all = nil
	}
	target, others := splitInstances(all, configPath)

	switch mode, used := resolveLaunch(configPath, explicit, canStart, target, others); mode {
	case launchAttach:
		return runAttach(used)
	case launchPicker:
		return runPicker(configPath, collectInstanceSummaries(others))
	case launchNoInstance:
		fmt.Fprintln(os.Stderr, errNoRunningInstance(configPath))
		return 1
	default:
		return runSession(used)
	}
}

// portClashes returns the ports cfg declares that a live instance also declares,
// sorted. free-port terminates whatever holds a port, so a shared port means
// starting a process here can kill a service over there.
func portClashes(cfg Config, rows []instanceSummary) []int {
	if len(rows) == 0 {
		return nil
	}
	mine := map[int]struct{}{}
	for _, pc := range cfg.Processes {
		if pc.Port > 0 {
			mine[pc.Port] = struct{}{}
		}
	}
	if len(mine) == 0 {
		return nil
	}
	found := map[int]struct{}{}
	for _, row := range rows {
		for _, port := range row.Ports {
			if _, ok := mine[port]; ok {
				found[port] = struct{}{}
			}
		}
	}
	out := make([]int, 0, len(found))
	for port := range found {
		out = append(out, port)
	}
	sort.Ints(out)
	return out
}

// formatOtherInstancesNotice is the one-line heads-up shown when a supervisor
// starts while others are already up.
func formatOtherInstancesNotice(rows []instanceSummary, clashes []int) string {
	if len(rows) == 0 {
		return ""
	}
	labels := make([]string, 0, len(rows))
	for _, row := range rows {
		labels = append(labels, row.Label)
	}
	msg := fmt.Sprintf("%d other instance(s) running: %s", len(rows), strings.Join(labels, ", "))
	if len(clashes) > 0 {
		msg += fmt.Sprintf(" · ⚠ shares port %s — starting here terminates it there", joinInts(clashes))
	}
	return msg
}

// otherInstancesNotice builds the startup notice for configPath, or "" when this
// is the only supervisor.
func otherInstancesNotice(configPath string, cfg Config) string {
	all, err := listRunningInstances()
	if err != nil || len(all) == 0 {
		return ""
	}
	_, others := splitInstances(all, configPath)
	if len(others) == 0 {
		return ""
	}
	rows := collectInstanceSummaries(others)
	return formatOtherInstancesNotice(rows, portClashes(cfg, rows))
}

// splitInstances separates the supervisor for configPath from the rest.
func splitInstances(all []InstanceState, configPath string) (target *InstanceState, others []InstanceState) {
	abs, _, err := instanceID(configPath)
	if err != nil {
		abs = configPath
	}
	for i := range all {
		if all[i].Config == abs {
			target = &all[i]
			continue
		}
		others = append(others, all[i])
	}
	return target, others
}
