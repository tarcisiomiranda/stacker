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
)

// pickerEntry is one selectable row: either a live instance or the offer to
// start the config in the working directory.
type pickerEntry struct {
	summary *instanceSummary
	// start marks the "start the config here" row, which has no instance yet.
	start      bool
	startLabel string
	config     string
}

type pickerModel struct {
	rows        []instanceSummary
	localConfig string
	selected    int

	action pickerAction
	chosen string

	statusText    string
	width, height int
}

func newPickerModel(rows []instanceSummary, localConfig string) *pickerModel {
	return &pickerModel{rows: rows, localConfig: strings.TrimSpace(localConfig)}
}

// entries is the selectable list: every live instance, then the start row when
// the working directory actually has a config to start.
func (m *pickerModel) entries() []pickerEntry {
	out := make([]pickerEntry, 0, len(m.rows)+1)
	for i := range m.rows {
		out = append(out, pickerEntry{summary: &m.rows[i], config: m.rows[i].State.Config})
	}
	if m.localConfig != "" {
		out = append(out, pickerEntry{
			start:      true,
			startLabel: configLabel(m.localConfig, 1),
			config:     m.localConfig,
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
		m.statusText = "stopped " + msg.label
		m.rows = msg.rows
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
			if entry.start {
				m.action = pickerStart
			} else {
				m.action = pickerAttach
			}
			m.chosen = entry.config
			return m, tea.Quit
		case "n":
			// Shortcut for "start the config here" without scrolling to the last row.
			for i, entry := range entries {
				if entry.start {
					m.selected = i
					m.action = pickerStart
					m.chosen = entry.config
					return m, tea.Quit
				}
			}
			m.statusText = "no local stacker.yml to start"
			return m, nil
		case "d":
			if m.selected < 0 || m.selected >= len(entries) {
				return m, nil
			}
			entry := entries[m.selected]
			if entry.start {
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
	b.WriteString(titleStyle.Render("Stacker — running instances"))
	b.WriteString("\n\n")

	entries := m.entries()
	if len(entries) == 0 {
		b.WriteString(pickerDimStyle.Render("nothing running, and no stacker.yml here"))
		b.WriteString("\n")
	}

	for i, entry := range entries {
		cursor := "  "
		if i == m.selected {
			cursor = "❯ "
		}
		if entry.start {
			line := cursor + "+ start the config here"
			if i == m.selected {
				line = selectedProcessStyle.Render(line)
			}
			b.WriteString(line)
			b.WriteString("\n")
			b.WriteString("    " + pickerDimStyle.Render(entry.config))
			b.WriteString("\n\n")
			continue
		}

		row := entry.summary
		head := fmt.Sprintf("%s%s", cursor, row.Label)
		if i == m.selected {
			head = selectedProcessStyle.Render(head)
		}
		b.WriteString(head)
		b.WriteString("  ")
		b.WriteString(pickerDimStyle.Render(orDefault(row.State.Mode, "session")))
		b.WriteString("\n")
		b.WriteString("    " + pickerDimStyle.Render(row.State.Config))
		b.WriteString("\n")

		if row.Err != "" {
			b.WriteString("    " + failedStyle.Render("unreachable: "+row.Err))
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

	footer := []string{
		keycap("↑↓") + " move",
		keycap("enter") + " open",
		keycap("n") + " start here",
		keycap("d") + " stop",
		keycap("r") + " refresh",
		keycap("q") + " quit",
	}
	if m.localConfig == "" {
		// No local config row: omit the start shortcut so the footer does not
		// advertise an action that always fails.
		footer = []string{
			keycap("↑↓") + " move",
			keycap("enter") + " open",
			keycap("d") + " stop",
			keycap("r") + " refresh",
			keycap("q") + " quit",
		}
	}
	b.WriteString(strings.Join(footer, mutedStyle.Render(" · ")))

	out := b.String()
	if m.width > 0 {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(out)
	}
	return out
}

// runPicker lets the user choose among live instances. Without a TTY it prints
// the same information and fails, so agents and scripts never block on a prompt
// they cannot answer.
func runPicker(configPath string, rows []instanceSummary) int {
	local := ""
	if configExists(configPath) {
		if abs, _, err := instanceID(configPath); err == nil {
			local = abs
		} else {
			local = configPath
		}
	}

	if !stdoutIsTerminal() {
		fmt.Fprint(os.Stderr, formatInstanceList(rows, local))
		fmt.Fprintln(os.Stderr, "\nPass --config to choose one without a terminal.")
		return 1
	}

	m := newPickerModel(rows, local)
	program := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "picker error:", err)
		return 1
	}
	switch m.action {
	case pickerAttach:
		return runAttach(m.chosen)
	case pickerStart:
		return runSession(m.chosen)
	default:
		return 0
	}
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
