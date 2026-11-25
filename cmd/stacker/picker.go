package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// pickerAction is what the user chose to do when the picker closed.
type pickerAction int

const (
	pickerCancel pickerAction = iota
	pickerAttach
	pickerStart
	pickerWorkspace
	// pickerLogHints prints the log commands for the selected instance and
	// exits, for when the answer wanted is a pipe, not a full-screen TUI.
	pickerLogHints
)

type pickerEntry struct {
	summary   *instanceSummary
	local     bool
	workspace bool
	config    string
}

func (entry pickerEntry) running() bool {
	return entry.summary != nil && entry.summary.State.PID > 0
}

type pickerModel struct {
	rows          []instanceSummary
	localConfig   string
	discoveryPath string
	projects      []workspaceProject
	selected      int
	listOffset    int

	action pickerAction
	chosen string
	// chosenRow is the instance behind chosen, kept for actions that need more
	// than its config path (log hints).
	chosenRow instanceSummary

	statusText    string
	width, height int
}

func newPickerModel(rows []instanceSummary, localConfig string) *pickerModel {
	return &pickerModel{rows: rows, localConfig: strings.TrimSpace(localConfig)}
}

func (m *pickerModel) entries() []pickerEntry {
	out := make([]pickerEntry, 0, len(m.rows)+2)
	if len(m.projects) > 1 {
		out = append(out, pickerEntry{workspace: true})
	}
	for i := range m.rows {
		out = append(out, pickerEntry{summary: &m.rows[i], config: m.rows[i].State.Config})
	}
	if m.localConfig != "" {
		out = append(out, pickerEntry{
			local:  true,
			config: m.localConfig,
		})
	}
	return out
}

func (m *pickerModel) Init() tea.Cmd { return nil }

func (m *pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case pickerDownMsg:
		if msg.err != nil {
			m.statusText = "stop failed: " + msg.err.Error()
			return m, nil
		}
		m.statusText = "refreshed"
		if msg.label != "" {
			m.statusText = "stopped " + msg.label
		}
		entries := m.entries()
		var previous pickerEntry
		if m.selected < len(entries) {
			previous = entries[m.selected]
		}
		if m.discoveryPath != "" {
			m.projects = discoverProjects(m.discoveryPath)
		}
		m.rows = pickerProjectRows(m.projects, msg.rows)
		for i, entry := range m.entries() {
			if entry.config == previous.config && entry.workspace == previous.workspace {
				m.selected = i
				return m, nil
			}
		}
		if m.selected >= len(m.entries()) {
			m.selected = max(0, len(m.entries())-1)
		}
		return m, nil
	case tea.KeyMsg:
		entries := m.entries()
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.action = pickerCancel
			return m, tea.Quit
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.selected+1 < len(entries) {
				m.selected++
			}
		case "enter":
			if m.selected < 0 || m.selected >= len(entries) {
				return m, nil
			}
			entry := entries[m.selected]
			switch {
			case entry.workspace:
				m.action = pickerWorkspace
			case entry.running():
				m.action = pickerAttach
			default:
				m.action = pickerStart
			}
			m.chosen = entry.config
			return m, tea.Quit
		case "n":
			// Shortcut for "start the config here" without scrolling to the last row.
			for i, entry := range entries {
				if entry.local {
					m.selected = i
					m.action = pickerStart
					m.chosen = entry.config
					return m, tea.Quit
				}
			}
			m.statusText = "no local stacker.yml to start"
			return m, nil
		case "l":
			if m.selected < 0 || m.selected >= len(entries) {
				return m, nil
			}
			entry := entries[m.selected]
			if !entry.running() {
				m.statusText = "Select a running project for log commands"
				return m, nil
			}
			m.action = pickerLogHints
			m.chosen = entry.config
			m.chosenRow = *entry.summary
			return m, tea.Quit
		case "d":
			if m.selected < 0 || m.selected >= len(entries) {
				return m, nil
			}
			entry := entries[m.selected]
			if !entry.running() {
				return m, nil
			}
			m.statusText = "stopping " + entry.summary.Label + "…"
			return m, stopInstanceCmd(*entry.summary)
		case "r":
			return m, refreshInstancesCmd()
		}
	}
	return m, nil
}

// pickerDownMsg carries the result of stopping an instance plus the refreshed
// list, so the picker never shows an entry that is already gone.
type pickerDownMsg struct {
	label string
	rows  []instanceSummary
	err   error
}

func stopInstanceCmd(row instanceSummary) tea.Cmd {
	return func() tea.Msg {
		client := &controlClient{
			addr:   row.State.Addr,
			client: &http.Client{Timeout: 20 * time.Second},
		}
		var resp struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := client.post("/v1/down", nil, &resp); err != nil {
			return pickerDownMsg{label: row.Label, rows: liveSummaries(), err: err}
		}
		// Give the supervisor a moment to clear its state file so the refreshed
		// list does not show a corpse.
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if !processAlive(row.State.PID) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		return pickerDownMsg{label: row.Label, rows: liveSummaries()}
	}
}

func refreshInstancesCmd() tea.Cmd {
	return func() tea.Msg {
		return pickerDownMsg{label: "", rows: liveSummaries()}
	}
}

// liveSummaries is the current instance list, best effort.
func liveSummaries() []instanceSummary {
	all, err := listRunningInstances()
	if err != nil {
		return nil
	}
	return collectInstanceSummaries(all)
}

var (
	pickerCollideStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	pickerDimStyle     = lipgloss.NewStyle().Faint(true)
)

func (m *pickerModel) View() string {
	var b strings.Builder
	title := "Stacker — running instances"
	if m.discoveryPath != "" {
		title = "Stacker — choose a project"
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n\n")

	entries := m.entries()
	if len(entries) == 0 {
		b.WriteString(pickerDimStyle.Render("nothing running, and no stacker.yml here"))
		b.WriteString("\n")
	}

	visible := len(entries)
	if m.height > 0 {
		visible = min(visible, max(1, (m.height-7)/5))
	}
	m.listOffset = sidebarSelectionOffset(m.listOffset, m.selected, len(entries), visible)
	for i := m.listOffset; i < m.listOffset+visible; i++ {
		entry := entries[i]
		cursor := "  "
		if i == m.selected {
			cursor = "❯ "
		}
		if entry.local || entry.workspace {
			title, detail := "+ start the config here", entry.config
			if entry.workspace {
				title, detail = "All projects", "Choose services from the projects in this folder"
			}
			line := cursor + title
			if i == m.selected {
				line = selectedProcessStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
			b.WriteString("    " + pickerDimStyle.Render(sanitizeLogLine(detail)))
			b.WriteString("\n\n")
			continue
		}

		row := entry.summary
		head := cursor + sanitizeLogLine(row.Label)
		if i == m.selected {
			head = selectedProcessStyle.Render(head)
		}
		b.WriteString(head)
		b.WriteString("  ")
		mode := "stopped"
		if entry.running() {
			mode = orDefault(row.State.Mode, "session")
		}
		b.WriteString(pickerDimStyle.Render(mode))
		b.WriteString("\n")
		b.WriteString("    " + pickerDimStyle.Render(sanitizeLogLine(row.State.Config)))
		b.WriteString("\n")

		if row.Err != "" {
			b.WriteString("    " + failedStyle.Render(sanitizeLogLine(row.Err)))
			b.WriteString("\n\n")
			continue
		}

		detail := fmt.Sprintf("%d/%d up", row.Up, row.Total)
		if len(row.Ports) > 0 {
			detail += " · " + joinInts(row.Ports)
		}
		b.WriteString("    " + pickerDimStyle.Render(detail))
		b.WriteString("\n")
		if len(row.Collide) > 0 {
			b.WriteString("    " + pickerCollideStyle.Render("⚠ port "+joinInts(row.Collide)+" also declared elsewhere"))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if m.statusText != "" {
		b.WriteString(mutedStyle.Render(m.statusText))
		b.WriteString("\n\n")
	}

	if len(entries) > visible {
		fmt.Fprintf(&b, "%d/%d\n", m.selected+1, len(entries))
	}

	footer := []string{
		keycap("↑↓") + " move",
		keycap("enter") + " open",
		keycap("l") + " logs",
	}
	if m.localConfig != "" {
		footer = append(footer, keycap("n")+" start here")
	}
	footer = append(footer, keycap("d")+" stop", keycap("r")+" refresh", keycap("q")+" quit")
	b.WriteString(strings.Join(footer, mutedStyle.Render(" · ")))

	out := b.String()
	if m.width > 0 {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(out)
	}
	return out
}

func runPicker(configPath, local string, projects []workspaceProject, rows []instanceSummary) int {
	choices := pickerProjectRows(projects, rows)
	if !stdoutIsTerminal() {
		if len(projects) > 0 {
			fmt.Fprint(os.Stderr, formatProjectChoices(choices[:len(projects)]))
		}
		if len(choices) > len(projects) || len(projects) == 0 {
			fmt.Fprint(os.Stderr, formatInstanceList(choices[len(projects):], local))
		}
		fmt.Fprintln(os.Stderr, "\nPass --config to choose one without a terminal.")
		return 1
	}

	m := newPickerModel(choices, local)
	if len(projects) > 0 {
		m.discoveryPath, m.projects = configPath, projects
	}
	program := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "picker error:", err)
		return 1
	}
	switch m.action {
	case pickerAttach:
		return runAttach(m.chosen)
	case pickerStart:
		return runTUI(m.chosen, true, true)
	case pickerWorkspace:
		return runWorkspace(configPath, m.projects)
	case pickerLogHints:
		fmt.Print(formatLogHints(m.chosenRow))
		return 0
	default:
		return 0
	}
}

func pickerProjectRows(projects []workspaceProject, running []instanceSummary) []instanceSummary {
	if len(projects) == 0 {
		return running
	}
	byConfig := make(map[string]instanceSummary, len(running))
	for _, row := range running {
		byConfig[row.State.Config] = row
	}
	rows := make([]instanceSummary, 0, len(projects)+len(running))
	for _, project := range projects {
		row, found := byConfig[project.config]
		if found {
			delete(byConfig, project.config)
		} else {
			row.State.Config = project.config
			procs, err := project.configuredProcesses()
			if err != nil {
				row.Err = err.Error()
			} else {
				row.setProcesses(procs)
			}
		}
		row.Label = project.name
		rows = append(rows, row)
	}
	for _, row := range running {
		if _, found := byConfig[row.State.Config]; found {
			rows = append(rows, row)
		}
	}
	markCollisions(rows)
	return rows
}

func formatProjectChoices(rows []instanceSummary) string {
	var b strings.Builder
	b.WriteString("Stacker projects in this directory:\n")
	for _, row := range rows {
		status := "stopped"
		if row.State.PID > 0 {
			status = "running, " + orDefault(row.State.Mode, "session")
		}
		fmt.Fprintf(&b, "  %s (%s)\n    stacker --config %s\n", sanitizeLogLine(row.Label), status, shellQuote(row.State.Config))
		if row.State.PID > 0 {
			fmt.Fprintf(&b, "    stacker --config %s logs %s\n", shellQuote(row.State.Config), shellQuote(orDefault(row.Sample, "<process>")))
		}
		if row.Err != "" {
			fmt.Fprintf(&b, "    %s\n", sanitizeLogLine(row.Err))
		}
		if len(row.Collide) > 0 {
			fmt.Fprintf(&b, "    shared ports: %s\n", joinInts(row.Collide))
		}
	}
	return b.String()
}

func configExists(path string) bool {
	info, err := os.Stat(path)
	if err == nil {
		return !info.IsDir()
	}
	// Permission denied still means "a config is there" — treating it as missing
	// would wrongly fall back read-only commands to another instance.
	if os.IsPermission(err) {
		return true
	}
	return false
}

func stdoutIsTerminal() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}
