package main

import (
	"embed"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Completion scripts ship inside the binary so they can never drift from the
// commands they complete: `stacker completion zsh` always describes this build.
//
//go:embed completions/stacker.bash completions/stacker.zsh completions/stacker.fish
var completionScripts embed.FS

// completionShells maps a shell name to its embedded script and the file name
// it is conventionally installed as.
var completionShells = map[string]struct {
	source  string
	install string
}{
	"bash": {"completions/stacker.bash", "stacker"},
	"zsh":  {"completions/stacker.zsh", "_stacker"},
	"fish": {"completions/stacker.fish", "stacker.fish"},
}

func completionShellNames() []string {
	names := make([]string, 0, len(completionShells))
	for name := range completionShells {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func cliCompletion(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: stacker completion <%s>\n\n",
			strings.Join(completionShellNames(), "|"))
		fmt.Fprintln(os.Stderr, "Install it where your shell looks:")
		for _, shell := range completionShellNames() {
			fmt.Fprintf(os.Stderr, "  %-5s %s\n", shell, completionInstallHint(shell))
		}
		fmt.Fprintln(os.Stderr, "\nOr let the local installer do it: mise run build:install")
		return 2
	}
	shell := strings.ToLower(strings.TrimSpace(args[0]))
	entry, ok := completionShells[shell]
	if !ok {
		fmt.Fprintf(os.Stderr, "error: unsupported shell %q; use one of: %s\n",
			args[0], strings.Join(completionShellNames(), ", "))
		return 2
	}
	data, err := completionScripts.ReadFile(entry.source)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	os.Stdout.Write(data)
	return 0
}

// cliCompleteHelp prints the per-shell install hint for `completion` with no
// arguments in the main help, kept next to the scripts it describes.
func completionInstallHint(shell string) string {
	switch shell {
	case "zsh":
		return "stacker completion zsh > \"${fpath[1]}/_stacker\""
	case "fish":
		return "stacker completion fish > ~/.config/fish/completions/stacker.fish"
	default:
		return "stacker completion bash > ~/.local/share/bash-completion/completions/stacker"
	}
}

// commandCompletions is the subcommand list offered on TAB, with the short
// descriptions zsh and fish display next to each candidate.
var commandCompletions = [][2]string{
	{"attach", "Attach a TUI to a running instance"},
	{"completion", "Print the shell completion script"},
	{"down", "Stop every process and the supervisor"},
	{"free-port", "Kill whatever listens on a TCP port"},
	{"instances", "List every running Stacker on this machine"},
	{"list", "List processes and their status"},
	{"logs", "Print a process log (or --supervisor)"},
	{"ping", "Check whether this config has a supervisor"},
	{"restart", "Stop then start a process"},
	{"run", "Run a one-shot task of a process"},
	{"serve", "Start a headless supervisor"},
	{"start", "Start a process"},
	{"status", "Status of one process or all"},
	{"stop", "Stop a process"},
	{"tasks", "List one-shot tasks"},
	{"version", "Print the Stacker version"},
}

// flagCompletions are the flags worth offering per command. The global ones are
// repeated where they apply rather than merged, so TAB never suggests a flag
// the command ignores.
var flagCompletions = map[string][][2]string{
	"": {
		{"--config", "Path to stacker.yml"},
		{"--json", "Machine-readable output"},
		{"--help", "Show help"},
		{"--version", "Print the version"},
	},
	"logs": {
		{"--tail", "Show the last N lines (default 200)"},
		{"--all", "Show everything still retained"},
		{"--since", "Start at an absolute log index"},
		{"--follow", "Keep printing new lines"},
		{"--supervisor", "The daemon's own log file"},
		{"--json", "Lines plus the next index"},
		{"--config", "Path to stacker.yml"},
	},
	"start": {
		{"--config", "Path to stacker.yml"},
		{"--json", "Machine-readable output"},
		{"--help", "Show help"},
		{"--version", "Print the version"},
		{"--group", "Select a process group"},
		{"-g", "Select a process group"},
	},
	"stop": {
		{"--config", "Path to stacker.yml"},
		{"--json", "Machine-readable output"},
		{"--help", "Show help"},
		{"--version", "Print the version"},
		{"--group", "Select a process group"},
		{"-g", "Select a process group"},
	},
	"restart": {
		{"--config", "Path to stacker.yml"},
		{"--json", "Machine-readable output"},
		{"--help", "Show help"},
		{"--version", "Print the version"},
		{"--group", "Select a process group"},
		{"-g", "Select a process group"},
	},
	"serve": {
		{"--background", "Daemonize the supervisor"},
		{"--web", "Start the web log viewer too"},
		{"--config", "Path to stacker.yml"},
	},
	"list":      {{"--json", "Machine-readable output"}, {"--config", "Path to stacker.yml"}},
	"status":    {{"--json", "Machine-readable output"}, {"--config", "Path to stacker.yml"}},
	"instances": {{"--json", "Machine-readable output"}, {"--config", "Path to stacker.yml"}},
	"tasks":     {{"--json", "Machine-readable output"}, {"--config", "Path to stacker.yml"}},
	"ping":      {{"--json", "Machine-readable output"}, {"--config", "Path to stacker.yml"}},
	"down":      {{"--json", "Machine-readable output"}, {"--config", "Path to stacker.yml"}},
}

// processCompletionCommands take a process name as their first argument.
var processCompletionCommands = map[string]bool{
	"logs":    true,
	"start":   true,
	"stop":    true,
	"restart": true,
	"status":  true,
	"tasks":   true,
	"run":     true,
}

// cliComplete answers the completion scripts. It is deliberately quiet: any
// failure prints nothing and exits 0, because a TAB press must never spray
// errors over the prompt or block on a dead supervisor.
func cliComplete(configPath string, args []string) int {
	if len(args) == 0 {
		return 0
	}
	switch args[0] {
	case "commands":
		printCompletions(commandCompletions)
	case "flags":
		command := ""
		if len(args) > 1 {
			command = normalizeCompletionCommand(args[1])
		}
		flags, ok := flagCompletions[command]
		if !ok {
			flags = flagCompletions[""]
		}
		printCompletions(flags)
	case "processes":
		printCompletions(processCandidates(configPath, false))
	case "groups":
		printCompletions(groupCandidates(configPath))
	case "tasks":
		if len(args) < 2 {
			return 0
		}
		printCompletions(taskCandidates(configPath, args[1]))
	case "shells":
		for _, name := range completionShellNames() {
			fmt.Println(name)
		}
	}
	return 0
}

// normalizeCompletionCommand folds the command aliases so `ls` and `list` share
// one flag set.
func normalizeCompletionCommand(command string) string {
	switch command {
	case "ls":
		return "list"
	case "log":
		return "logs"
	case "a":
		return "attach"
	case "ins":
		return "instances"
	case "freeport":
		return "free-port"
	default:
		return command
	}
}

func printCompletions(pairs [][2]string) {
	for _, pair := range pairs {
		if pair[1] == "" {
			fmt.Println(pair[0])
			continue
		}
		fmt.Printf("%s\t%s\n", pair[0], pair[1])
	}
}

// processCandidates lists the live processes with a description that answers
// "which one do I want?" — the status, and the port when it has one.
func processCandidates(configPath string, tasksOnly bool) [][2]string {
	infos, err := completionProcesses(configPath)
	if err != nil {
		return nil
	}
	out := make([][2]string, 0, len(infos))
	for _, p := range infos {
		if tasksOnly && len(p.Tasks) == 0 {
			continue
		}
		out = append(out, [2]string{p.Name, describeForCompletion(p)})
	}
	return out
}

func describeForCompletion(p ProcessInfo) string {
	parts := []string{orDefault(p.Status, "unknown")}
	if p.Status == string(StatusDisabled) {
		// The status alone reads like a choice someone made; say why it cannot run.
		parts[0] = "disabled · cwd unavailable"
	}
	if p.OneShot {
		parts = append(parts, "task")
	}
	if p.Port > 0 {
		parts = append(parts, ":"+strconv.Itoa(p.Port))
	}
	if p.Orphaned {
		parts = append(parts, "removed from YAML")
	}
	if p.Group != "" {
		parts = append(parts, p.Group)
	}
	return strings.Join(parts, " · ")
}

func groupCandidates(configPath string) [][2]string {
	infos, err := completionProcesses(configPath)
	if err != nil {
		return nil
	}
	members := make([]sectionMember, 0, len(infos))
	for index, process := range infos {
		members = append(members, sectionMember{
			Name:     process.Name,
			Group:    process.Group,
			OneShot:  process.OneShot,
			Orphaned: process.Orphaned,
			Index:    index,
		})
	}
	sections := buildSections(members)
	candidates := make([][2]string, 0, len(sections))
	for _, current := range sections {
		candidates = append(candidates, [2]string{current.Name, ""})
	}
	return candidates
}

func taskCandidates(configPath, process string) [][2]string {
	infos, err := completionProcesses(configPath)
	if err != nil {
		return nil
	}
	for _, p := range infos {
		if p.Name != process {
			continue
		}
		out := make([][2]string, 0, len(p.Tasks))
		for _, task := range p.Tasks {
			out = append(out, [2]string{task, "task of " + p.Name})
		}
		return out
	}
	return nil
}

func completionProcesses(configPath string) ([]ProcessInfo, error) {
	client, _, err := newControlClientFor(configPath, true)
	if err != nil {
		return nil, err
	}
	var resp struct {
		OK        bool          `json:"ok"`
		Processes []ProcessInfo `json:"processes"`
	}
	if err := client.get("/v1/processes", &resp); err != nil {
		return nil, err
	}
	return resp.Processes, nil
}
