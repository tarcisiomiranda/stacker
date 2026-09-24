package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestProcessOrphanedAccessor(t *testing.T) {
	process := NewProcess("test", ProcessConfig{}, 10)
	process.mu.Lock()
	process.orphaned = true
	process.mu.Unlock()
	if !process.Orphaned() {
		t.Fatal("orphaned accessor did not return true")
	}
	process.mu.Lock()
	process.orphaned = false
	process.mu.Unlock()
	if process.Orphaned() {
		t.Fatal("orphaned accessor did not return false")
	}

	const iterations = 100_000
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		ready <- struct{}{}
		<-start
		for index := 0; index < iterations; index++ {
			process.mu.Lock()
			process.orphaned = index%2 == 0
			process.mu.Unlock()
		}
	}()
	go func() {
		defer workers.Done()
		ready <- struct{}{}
		<-start
		for index := 0; index < iterations; index++ {
			process.Orphaned()
		}
	}()
	<-ready
	<-ready
	close(start)
	workers.Wait()
}

func TestProcessKeepsOnlyConfiguredNumberOfLogs(t *testing.T) {
	p := NewProcess("test", ProcessConfig{}, 2)
	p.appendLog("one")
	p.appendLog("two")
	p.appendLog("three")

	logs := p.Logs()
	if len(logs) != 2 || logs[0] != "two" || logs[1] != "three" {
		t.Fatalf("unexpected retained logs: %#v", logs)
	}
}

func TestNewModelSelectsFirstAutostartProcess(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"backend": {Command: "true"},
		"demo":    {Command: "true", Autostart: true},
	}})

	if current := m.current(); current == nil || current.Name != "demo" {
		t.Fatalf("expected demo to be selected, got %#v", current)
	}
}

func TestNewModelSelectsGroupedAutostartProcessByRow(t *testing.T) {
	m := groupedModel()
	if m.selected != 1 {
		t.Fatalf("expected selected row 1 for api after its section header, got %d", m.selected)
	}
	if current := m.current(); current == nil || current.Name != "api" {
		t.Fatalf("expected api to be selected, got %#v", current)
	}
}

func TestNewModelInitializesCollapsedSections(t *testing.T) {
	if groupedModel().collapsed == nil {
		t.Fatal("expected collapsed state to be initialized")
	}
}

func TestNewModelMembersNormalizeGroupsAndPreserveIndexes(t *testing.T) {
	m := newModel(Config{
		Processes: map[string]ProcessConfig{
			"api":    {Command: "true", Group: " core "},
			"worker": {Command: "true", Group: "jobs"},
		},
		processOrder: []string{"api", "worker"},
		Tasks: map[string]TaskConfig{
			"deploy": {Command: "true", Group: " core "},
		},
		taskOrder: []string{"deploy"},
	})
	members := m.members()
	if len(members) != 3 {
		t.Fatalf("expected three members, got %#v", members)
	}
	if members[0].Name != "api" || members[0].Group != "core" || members[0].Index != 0 {
		t.Fatalf("unexpected first service member: %#v", members[0])
	}
	if members[1].Name != "worker" || members[1].Group != "jobs" || members[1].Index != 1 {
		t.Fatalf("unexpected second service member: %#v", members[1])
	}
	if members[2].Name != "deploy" || members[2].Group != "core" || !members[2].OneShot || members[2].Index != 2 {
		t.Fatalf("unexpected one-shot member: %#v", members[2])
	}
}

func TestNewModelUngroupedProcessListKeepsOneRowPerProcess(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api":    {Command: "true"},
		"worker": {Command: "true"},
	}})
	lines := strings.Split(m.processList(), "\n")
	if len(lines) != len(m.processes)+1 {
		t.Fatalf("expected one list row per ungrouped process plus title, got %d lines for %d processes", len(lines), len(m.processes))
	}
}

func TestProcessListNavigationSelectsSectionHeadersAndMembers(t *testing.T) {
	m := groupedModel()
	m.selected = 1

	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.selected != 0 || m.current() != nil {
		t.Fatalf("expected up to select the core header row, selected=%d current=%#v", m.selected, m.current())
	}

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if current := m.current(); current == nil || current.Name != "api" {
		t.Fatalf("expected down to select api, got %#v", current)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if current := m.current(); current == nil || current.Name != "worker" {
		t.Fatalf("expected down to select worker, got %#v", current)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if current := m.current(); current == nil || current.Name != "migrate" {
		t.Fatalf("expected down to select migrate, got %#v", current)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.current() != nil {
		t.Fatalf("expected down to select a section header, got %#v", m.current())
	}
}

func TestProcessListNavigationSkipsMembersInCollapsedSections(t *testing.T) {
	m := groupedModel()
	m.collapsed["core"] = true
	m.selected = 0

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.selected != 1 || m.current() != nil {
		t.Fatalf("expected down to move from collapsed core to data header row, selected=%d current=%#v", m.selected, m.current())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if current := m.current(); current == nil || current.Name != "db" {
		t.Fatalf("expected down to move from data header to db, got %#v", current)
	}
}

func TestProcessListRendersSectionHeaderRows(t *testing.T) {
	m := groupedModel()
	lines := strings.Split(m.processList(), "\n")
	if len(lines) != 7 {
		t.Fatalf("expected title plus six section rows, got %d lines: %q", len(lines), lines)
	}
	if got := ansi.Strip(strings.Join(lines, "\n")); !strings.Contains(got, "core") || !strings.Contains(got, "data") {
		t.Fatalf("expected group headers in process list, got %q", got)
	}
}

func TestSectionHeaderRendersDividerFoldMarkerServiceCountAndTaskErrorBadge(t *testing.T) {
	m := groupedModel()
	m.width = 100
	m.processes[0].status = StatusRunning
	m.processes[1].status = StatusStopped
	m.processes[3].status = StatusFailed
	m.processes[3].errCount = 1

	contentWidth := max(1, m.leftWidth()-5)
	line := ansi.Strip(strings.Split(m.processList(), "\n")[1])
	if !strings.Contains(line, "− core") || !strings.Contains(line, "core ─") || strings.Contains(line, "▾") || !strings.HasSuffix(line, " 1/2!") {
		t.Fatalf("expected expanded core header with service count and task error badge, got %q", line)
	}
	if width := ansi.StringWidth(line); width != contentWidth {
		t.Fatalf("expanded section header width = %d, want %d: %q", width, contentWidth, line)
	}

	m.collapsed["core"] = true
	line = ansi.Strip(strings.Split(m.processList(), "\n")[1])
	if !strings.Contains(line, "+ core") || !strings.Contains(line, "core ─") || strings.Contains(line, "▸") || !strings.HasSuffix(line, " 1/2!") {
		t.Fatalf("expected collapsed core header with service count and task error badge, got %q", line)
	}
	if width := ansi.StringWidth(line); width != contentWidth {
		t.Fatalf("collapsed section header width = %d, want %d: %q", width, contentWidth, line)
	}
}

func TestSectionHeaderTruncatesWithinAvailableWidth(t *testing.T) {
	const width = 16
	line := formatSectionHeader("a-very-long-group-name", sectionSummary{
		Running: 1,
		Total:   2,
		State:   "error",
	}, false, false, width)
	plain := ansi.Strip(line)
	if got := ansi.StringWidth(plain); got != width {
		t.Fatalf("section header width = %d, want %d: %q", got, width, plain)
	}
	if !strings.Contains(plain, "−") || !strings.Contains(plain, " ─ ") || !strings.HasSuffix(plain, " 1/2!") {
		t.Fatalf("narrow section header lost visible divider spacing or its count: %q", plain)
	}
}

func TestSectionHeaderOmitsCountForTaskOnlySection(t *testing.T) {
	m := newModel(Config{Tasks: map[string]TaskConfig{
		"publish": {Command: "true", Group: "release"},
	}})
	m.width = 100

	line := ansi.Strip(strings.Split(m.processList(), "\n")[1])
	if !strings.Contains(line, "− release") || strings.Contains(line, "▾") || strings.Contains(line, "/") {
		t.Fatalf("expected task-only header without a count, got %q", line)
	}
}

func TestSectionHeaderSelectionUsesSelectedRowTreatment(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })

	m := groupedModel()
	m.width = 100
	m.selected = 0

	line := strings.Split(m.processList(), "\n")[1]
	plain := ansi.Strip(line)
	if plain == line {
		t.Fatal("expected selected header styling to render reverse-video control codes")
	}
	if line != selectedProcessStyle.Render(plain) {
		t.Fatalf("selected header did not use selected-row reverse treatment\ngot  %q\nwant %q", line, selectedProcessStyle.Render(plain))
	}
}

func TestProcessListShowsTaskMarkerInGroupedRow(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {
			Command: "true",
			Group:   "application",
			Tasks:   map[string]string{"lint": "true"},
		},
	}})
	m.width = 100

	for _, line := range strings.Split(m.processList(), "\n") {
		plain := ansi.Strip(line)
		if strings.Contains(plain, "api") {
			if !strings.Contains(plain, "⋯") {
				t.Fatalf("grouped service with tasks lacks task marker: %q", plain)
			}
			return
		}
	}
	t.Fatal("grouped api row not found")
}

func TestSectionHeaderUsesTaskFailureAndErrorState(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })

	m := groupedModel()
	m.width = 100
	task := m.processes[3]
	task.status = StatusFailed

	line := strings.Split(m.processList(), "\n")[1]
	plain := ansi.Strip(line)
	if strings.Contains(plain, "!") || line != failedStyle.Render(plain) {
		t.Fatalf("expected failed task to render a red header without an error badge, got %q", line)
	}

	task.errCount = 1
	line = strings.Split(m.processList(), "\n")[1]
	plain = ansi.Strip(line)
	if !strings.Contains(plain, "!") || line != errorBadgeStyle.Render(plain) {
		t.Fatalf("expected task error lines to render an orange header with an error badge, got %q", line)
	}
}

func TestSectionHeaderFoldKeysCollapseMemberAndExpandHeader(t *testing.T) {
	m := groupedModel()
	m.selected = 1

	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if !m.collapsed["core"] || m.selected != 0 || m.selectedSection() == nil {
		t.Fatalf("expected left to collapse core and select its header, collapsed=%v selected=%d section=%#v", m.collapsed, m.selected, m.selectedSection())
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if m.collapsed["core"] || m.selected != 0 {
		t.Fatalf("expected l to expand selected core header, collapsed=%v selected=%d", m.collapsed, m.selected)
	}

	m.selected = 1
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if !m.collapsed["core"] || m.selected != 0 {
		t.Fatalf("expected h to collapse core from its member and select its header, collapsed=%v selected=%d", m.collapsed, m.selected)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.collapsed["core"] || m.selected != 0 {
		t.Fatalf("expected right to expand selected core header, collapsed=%v selected=%d", m.collapsed, m.selected)
	}
}

func TestMouseProcessListTogglesHeaderAndSelectsMembers(t *testing.T) {
	m := groupedModel()
	m.width = 100

	m.handleMouse(tea.MouseMsg{X: 1, Y: 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !m.collapsed["core"] || m.selected != 0 || m.selectedSection() == nil {
		t.Fatalf("expected header click to collapse and keep core selected, collapsed=%v selected=%d", m.collapsed, m.selected)
	}
	m.handleMouse(tea.MouseMsg{X: 1, Y: 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.collapsed["core"] || m.selected != 0 {
		t.Fatalf("expected second header click to expand and keep core selected, collapsed=%v selected=%d", m.collapsed, m.selected)
	}
	m.handleMouse(tea.MouseMsg{X: 1, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if current := m.current(); current == nil || current.Name != "api" {
		t.Fatalf("expected click on api row to select api, got %#v", current)
	}
}

func TestMouseProcessListClampsClickToVisibleRows(t *testing.T) {
	m := groupedModel()
	m.width = 100

	m.handleMouse(tea.MouseMsg{X: 1, Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !m.collapsed["core"] || m.selected != 0 {
		t.Fatalf("expected click above the rows to clamp to and toggle the first header, collapsed=%v selected=%d", m.collapsed, m.selected)
	}

	m = groupedModel()
	m.width = 100
	m.handleMouse(tea.MouseMsg{X: 1, Y: 100, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if current := m.current(); current == nil || current.Name != "db" {
		t.Fatalf("expected click below the rows to clamp to the last member, got %#v at row %d", current, m.selected)
	}
}

func TestHelpDocumentsSectionFoldingActionsGroupPickerAndReordering(t *testing.T) {
	got := ansi.Strip(groupedModel().helpView())
	for _, text := range []string{"Sections", "←/h", "→/l", "g", "↑/k", "↓/j", "enter", "1–9", "0", "on a header", "whole section", "within its section"} {
		if !strings.Contains(got, text) {
			t.Fatalf("help is missing %q: %q", text, got)
		}
	}
}

func TestProcessListHeaderShowsSectionSummaryInLogPanel(t *testing.T) {
	m := groupedModel()
	m.width = 100
	m.height = 20
	m.selected = 0

	got := m.logView()
	if !strings.Contains(got, "Section: core") || !strings.Contains(got, "2 services") || !strings.Contains(got, "1 task") {
		t.Fatalf("expected selected section summary, got %q", got)
	}
}

func TestMoveSelectedGroupedProcessUsesMemberIndex(t *testing.T) {
	m := groupedModel()
	m.selected = 1

	if cmd := m.moveSelectedCmd(1); cmd == nil {
		t.Fatal("expected selected api to be reorderable")
	}
	if current := m.current(); current == nil || current.Name != "api" {
		t.Fatalf("expected moved api to remain selected, got %#v", current)
	}
	if m.selected != 2 {
		t.Fatalf("expected api selected at its new visible row 2, got %d", m.selected)
	}
}

func groupedModel() *model {
	return newModel(Config{
		Processes: map[string]ProcessConfig{
			"api":    {Command: "true", Group: "core", Autostart: true},
			"worker": {Command: "true", Group: "core"},
			"db":     {Command: "true", Group: "data"},
		},
		processOrder: []string{"api", "worker", "db"},
		Tasks: map[string]TaskConfig{
			"migrate": {Command: "true", Group: "core"},
		},
		taskOrder: []string{"migrate"},
	})
}

func TestProcessListRowsFitPanel(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"backend-with-a-long-name": {Command: "true", Group: "infrastructure-group-with-a-very-long-name"},
		"demo":                     {Command: "true", Autostart: true, Group: "infrastructure-group-with-a-very-long-name"},
	}})
	m.width = 80

	maxWidth := m.leftWidth() - 5
	for _, line := range strings.Split(m.processList(), "\n") {
		if width := lipgloss.Width(line); width > maxWidth {
			t.Fatalf("line width %d exceeds panel content width %d: %q", width, maxWidth, line)
		}
	}
}

// Selected rows with a color dot must reverse the whole plain name, not nest
// the colored ● inside Reverse (that closes reverse early on the ball).
func TestProcessListSelectionCoversNameWithColorDot(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"myapp": {Command: "true", Color: "#38bdf8", Autostart: true},
	}})
	m.width = 80

	var selected string
	for _, line := range strings.Split(m.processList(), "\n") {
		if strings.Contains(ansi.Strip(line), "myapp") {
			selected = line
			break
		}
	}
	if selected == "" {
		t.Fatal("selected process line not found")
	}
	plain := ansi.Strip(selected)
	if !strings.Contains(plain, "●") || !strings.Contains(plain, "myapp") {
		t.Fatalf("expected plain row with dot+name, got %q", plain)
	}
	// Reverse must wrap plain text only — no nested SGR before the name.
	want := selectedProcessStyle.Render(plain)
	if selected != want {
		t.Fatalf("selection did not cover full plain row\ngot  %q\nwant %q", selected, want)
	}
}

func TestFormatProcessListLineSelectedIgnoresNestedColors(t *testing.T) {
	got := formatProcessListLine(processListLine{
		name:         "api",
		status:       "running",
		statusKind:   "running",
		color:        "red",
		selected:     true,
		contentWidth: 40,
	})
	plain := ansi.Strip(got)
	if got != selectedProcessStyle.Render(plain) {
		t.Fatalf("selected line must be reverse(plain), got %q plain %q", got, plain)
	}
	if !strings.HasPrefix(plain, "● api") {
		t.Fatalf("plain row should start with colored-ball placeholder + name, got %q", plain)
	}

	// Unselected keeps a colored ● / status when the terminal profile allows
	// it; without a TTY lipgloss may strip colors, so only assert structure.
	unsel := formatProcessListLine(processListLine{
		name:         "api",
		status:       "running",
		statusKind:   "running",
		color:        "red",
		selected:     false,
		contentWidth: 40,
	})
	if !strings.Contains(ansi.Strip(unsel), "api") {
		t.Fatalf("unselected row missing name: %q", unsel)
	}
	if strings.HasPrefix(ansi.Strip(unsel), "● api") == false {
		t.Fatalf("unselected plain should start with ● api, got %q", ansi.Strip(unsel))
	}
}

func TestCaptureSanitizesTerminalControlSequences(t *testing.T) {
	p := NewProcess("test", ProcessConfig{}, 10)
	p.capture(strings.NewReader("\x1b[31mINFO\x1b[0m\trequest\rrewrite\a\n"), "stderr", func() {})

	logs := p.Logs()
	if len(logs) != 1 {
		t.Fatalf("expected one log line, got %#v", logs)
	}
	if strings.ContainsAny(logs[0], "\x1b\t\r\a") {
		t.Fatalf("log still contains terminal control characters: %q", logs[0])
	}
	if logs[0] != "[stderr] INFO    request rewrite " {
		t.Fatalf("unexpected sanitized log: %q", logs[0])
	}
}

func TestLogViewLinesStayWithinPanelWidth(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"demo": {Command: "true"},
	}})
	m.width = 100
	m.height = 20
	for range 100 {
		m.current().appendLog(sanitizeLogLine("\x1b[34mINFO\x1b[0m\t" + strings.Repeat("界", 100)))
	}
	m.scrollToBottom()

	maxWidth := m.width - m.leftWidth() - 6
	lines := strings.Split(m.logView(), "\n")
	if len(lines) > m.logHeight() {
		t.Fatalf("log view height %d exceeds available height %d", len(lines), m.logHeight())
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > maxWidth {
			t.Fatalf("line width %d exceeds panel width %d: %q", width, maxWidth, line)
		}
	}
}

func TestLogViewWordWrapShowsFullLines(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"demo": {Command: "true"},
	}})
	m.width = 100
	m.height = 30
	m.wrap = true
	long := strings.Repeat("abc ", 60)
	m.current().appendLog(long)
	m.scrollToBottom()

	maxWidth := m.logWidth()
	view := m.logView()
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > maxWidth {
			t.Fatalf("wrapped line width %d exceeds panel width %d: %q", width, maxWidth, line)
		}
	}
	if strings.Contains(view, "…") {
		t.Fatal("wrap mode must not truncate lines")
	}
	joined := strings.ReplaceAll(strings.Join(strings.Split(view, "\n")[1:], ""), "\n", "")
	if !strings.Contains(strings.ReplaceAll(joined, " ", ""), strings.ReplaceAll(long, " ", "")) {
		t.Fatal("wrapped view lost log content")
	}
}

func TestVisualLinesShareLogicalIndexWhenWrapped(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"demo": {Command: "true"},
	}})
	m.wrap = true
	vis := m.visualLines([]string{"short", strings.Repeat("x", 25)}, 10)
	if len(vis) != 4 {
		t.Fatalf("expected 4 visual lines, got %d: %#v", len(vis), vis)
	}
	if vis[0].idx != 0 || vis[1].idx != 1 || vis[2].idx != 1 || vis[3].idx != 1 {
		t.Fatalf("unexpected logical indexes: %#v", vis)
	}
}

func TestUpdateConfigColorPreservesCommentsAndFields(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1

# UI tuning
ui:
  wheel_lines: 3

processes:
  # backend service
  api:
    command: echo ok
    graceful_timeout: 1s
  web:
    command: echo ok
    color: "#0af"
`)

	if err := updateConfigColor(path, "api", "#38bdf8"); err != nil {
		t.Fatalf("set color: %v", err)
	}
	if err := updateConfigColor(path, "web", ""); err != nil {
		t.Fatalf("remove color: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{"# UI tuning", "# backend service", `"#38bdf8"`, "\n\nprocesses:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in rewritten config:\n%s", want, text)
		}
	}
	if strings.Contains(text, "#0af") {
		t.Fatalf("expected web color removed:\n%s", text)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("rewritten config must still load: %v", err)
	}
	if got := cfg.Processes["api"].Color; got != "#38bdf8" {
		t.Fatalf("expected api color #38bdf8, got %q", got)
	}
	if got := cfg.Processes["web"].Color; got != "" {
		t.Fatalf("expected web color removed, got %q", got)
	}
}

func TestUpdateConfigColorUnknownProcess(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
`)
	if err := updateConfigColor(path, "missing", "#fff"); err == nil {
		t.Fatal("expected error for unknown process")
	}
}

func TestUpdateConfigFieldGroupPreservesComments(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
# process mapping note
processes:
  # api member note
  api:
    command: echo api
    group: old # api group note
  # worker member note
  worker:
    command: echo worker
# task mapping note
tasks:
  # deploy task note
  deploy:
    command: echo deploy
    group: old # deploy group note
  # backup task note
  backup:
    command: echo backup
`)

	value := `release: # [blue], {canary} "east"`
	for _, target := range []struct {
		mapping string
		name    string
	}{
		{mapping: "processes", name: "api"},
		{mapping: "processes", name: "worker"},
		{mapping: "tasks", name: "deploy"},
		{mapping: "tasks", name: "backup"},
	} {
		if err := updateConfigField(path, target.mapping, target.name, "group", value); err != nil {
			t.Fatalf("set %s group on %s: %v", target.name, target.mapping, err)
		}
	}

	if err := updateConfigField(path, "processes", "api", "group", "release: # [green], {stable} 'west'"); err != nil {
		t.Fatalf("replace process group: %v", err)
	}
	if err := updateConfigField(path, "tasks", "deploy", "group", "release: # [green], {stable} 'west'"); err != nil {
		t.Fatalf("replace task group: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rewritten config: %v", err)
	}
	text := string(data)
	for _, comment := range []string{
		"# process mapping note",
		"# api member note",
		"# api group note",
		"# worker member note",
		"# task mapping note",
		"# deploy task note",
		"# deploy group note",
		"# backup task note",
	} {
		if !strings.Contains(text, comment) {
			t.Fatalf("expected comment %q to survive field updates:\n%s", comment, text)
		}
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("rewritten config must still load: %v", err)
	}
	if got := cfg.Processes["api"].Group; got != "release: # [green], {stable} 'west'" {
		t.Fatalf("unexpected replaced process group %q", got)
	}
	if got := cfg.Processes["worker"].Group; got != value {
		t.Fatalf("unexpected inserted process group %q", got)
	}
	if got := cfg.Tasks["deploy"].Group; got != "release: # [green], {stable} 'west'" {
		t.Fatalf("unexpected replaced task group %q", got)
	}
	if got := cfg.Tasks["backup"].Group; got != value {
		t.Fatalf("unexpected inserted task group %q", got)
	}

	for _, target := range []struct {
		mapping string
		name    string
	}{
		{mapping: "processes", name: "api"},
		{mapping: "processes", name: "worker"},
		{mapping: "tasks", name: "deploy"},
		{mapping: "tasks", name: "backup"},
	} {
		if err := updateConfigField(path, target.mapping, target.name, "group", ""); err != nil {
			t.Fatalf("remove %s group from %s: %v", target.name, target.mapping, err)
		}
	}

	cfg, err = loadConfig(path)
	if err != nil {
		t.Fatalf("config after removing groups must still load: %v", err)
	}
	for _, got := range []string{
		cfg.Processes["api"].Group,
		cfg.Processes["worker"].Group,
		cfg.Tasks["deploy"].Group,
		cfg.Tasks["backup"].Group,
	} {
		if got != "" {
			t.Fatalf("expected all groups removed, got %q", got)
		}
	}
}

func TestUpdateConfigFieldFlowEntryFallbackPreservesComments(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
# process mapping note
processes:
  # api member note
  api: {command: echo api, group: old}
# task mapping note
tasks:
  # deploy task note
  deploy: {command: echo deploy, group: old}
`)
	value := `release: # [blue], {canary} "east"`
	for _, target := range []struct {
		mapping string
		name    string
	}{
		{mapping: "processes", name: "api"},
		{mapping: "tasks", name: "deploy"},
	} {
		if err := updateConfigField(path, target.mapping, target.name, "group", value); err != nil {
			t.Fatalf("update %s group on %s: %v", target.name, target.mapping, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rewritten config: %v", err)
	}
	text := string(data)
	for _, comment := range []string{"# process mapping note", "# api member note", "# task mapping note", "# deploy task note"} {
		if !strings.Contains(text, comment) {
			t.Fatalf("expected comment %q to survive fallback encoding:\n%s", comment, text)
		}
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("fallback-encoded config must still load: %v", err)
	}
	if got := cfg.Processes["api"].Group; got != value {
		t.Fatalf("unexpected process group after fallback encoding %q", got)
	}
	if got := cfg.Tasks["deploy"].Group; got != value {
		t.Fatalf("unexpected task group after fallback encoding %q", got)
	}
}

func TestLoadConfigAcceptsWordWrap(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
ui:
  word_wrap: true
processes:
  api:
    command: echo ok
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.UI.WordWrap {
		t.Fatal("expected word_wrap to be true")
	}
	if m := newModel(cfg); !m.wrap {
		t.Fatal("expected model wrap to start enabled")
	}
}

func TestLoadConfigKeepsYAMLOrder(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  zeta:
    command: echo ok
  alpha:
    command: echo ok
  midway:
    command: echo ok
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	want := []string{"zeta", "alpha", "midway"}
	m := newModel(cfg)
	for i, name := range want {
		if m.processes[i].Name != name {
			t.Fatalf("expected order %v, got %q at %d", want, m.processes[i].Name, i)
		}
	}
}

func TestUpdateConfigOrderMovesBlocksWithComments(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1

# UI tuning
ui:
  wheel_lines: 3

processes:
  # backend service
  api:
    command: echo api
    graceful_timeout: 1s

  web:
    command: echo web
    color: "#0af"

  # demo generator
  demo:
    command: echo demo
`)

	if err := updateConfigOrder(path, "processes", []string{"demo", "api", "web"}); err != nil {
		t.Fatalf("update order: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	demoAt := strings.Index(text, "demo:")
	apiAt := strings.Index(text, "api:")
	webAt := strings.Index(text, "web:")
	if !(demoAt < apiAt && apiAt < webAt) {
		t.Fatalf("expected demo < api < web in file:\n%s", text)
	}
	// Comments must travel with their process; blank separators must survive.
	if !strings.Contains(text, "# demo generator\n  demo:") {
		t.Fatalf("demo comment did not move with its block:\n%s", text)
	}
	if !strings.Contains(text, "# backend service\n  api:") {
		t.Fatalf("api comment lost:\n%s", text)
	}
	if !strings.Contains(text, "# UI tuning") || !strings.Contains(text, "\n\nprocesses:") {
		t.Fatalf("unrelated formatting changed:\n%s", text)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("reordered config must still load: %v", err)
	}
	want := []string{"demo", "api", "web"}
	for i, name := range want {
		if cfg.processOrder[i] != name {
			t.Fatalf("expected order %v, got %v", want, cfg.processOrder)
		}
	}
}

func TestUpdateConfigOrderRejectsBadPermutation(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
  web:
    command: echo ok
`)
	for _, names := range [][]string{
		{"api"},
		{"api", "api"},
		{"api", "missing"},
	} {
		if err := updateConfigOrder(path, "processes", names); err == nil {
			t.Fatalf("expected rejection for %v", names)
		}
	}
}

func TestUpdateConfigOrderReordersTasksWithComments(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
tasks:
  # backup task
  backup: {command: echo backup}
  # deploy task
  deploy: {command: echo deploy}
`)

	if err := updateConfigOrder(path, "tasks", []string{"deploy", "backup"}); err != nil {
		t.Fatalf("update task order: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rewritten config: %v", err)
	}
	text := string(data)
	deployAt := strings.Index(text, "deploy:")
	backupAt := strings.Index(text, "backup:")
	if !(deployAt < backupAt) {
		t.Fatalf("expected deploy < backup in file:\n%s", text)
	}
	if !strings.Contains(text, "# deploy task\n  deploy:") || !strings.Contains(text, "# backup task\n  backup:") {
		t.Fatalf("task comments did not move with their blocks:\n%s", text)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("reordered config must still load: %v", err)
	}
	want := []string{"deploy", "backup"}
	for i, name := range want {
		if cfg.taskOrder[i] != name {
			t.Fatalf("expected task order %v, got %v", want, cfg.taskOrder)
		}
	}
}

func TestUpdateConfigOrderRejectsBadTaskPermutation(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
tasks:
  backup:
    command: echo backup
  deploy:
    command: echo deploy
`)
	for _, names := range [][]string{
		{"backup"},
		{"backup", "backup"},
		{"backup", "missing"},
	} {
		if err := updateConfigOrder(path, "tasks", names); err == nil {
			t.Fatalf("expected task mapping rejection for %v", names)
		}
	}
}

func TestUpdateConfigUIFlag(t *testing.T) {
	t.Run("replaces existing keeping comment", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), `
version: 1
ui:
  highlight_errors: false  # opt-in badge
processes:
  api:
    command: echo ok
`)
		if err := updateConfigUIFlag(path, "highlight_errors", true); err != nil {
			t.Fatalf("update flag: %v", err)
		}
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), "highlight_errors: true  # opt-in badge") {
			t.Fatalf("expected replaced line with comment:\n%s", data)
		}
	})

	t.Run("adds key to existing ui section", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), `
version: 1
ui:
  wheel_lines: 3

processes:
  api:
    command: echo ok
`)
		if err := updateConfigUIFlag(path, "highlight_errors", true); err != nil {
			t.Fatalf("update flag: %v", err)
		}
		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatalf("config must still load: %v", err)
		}
		if !cfg.UI.HighlightErrors {
			t.Fatal("expected highlight_errors true")
		}
		if cfg.UI.WheelLines != 3 {
			t.Fatal("wheel_lines lost")
		}
	})

	t.Run("creates ui section when missing", func(t *testing.T) {
		path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
`)
		if err := updateConfigUIFlag(path, "highlight_errors", true); err != nil {
			t.Fatalf("update flag: %v", err)
		}
		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatalf("config must still load: %v", err)
		}
		if !cfg.UI.HighlightErrors {
			t.Fatal("expected highlight_errors true")
		}
	})
}

func TestRunTaskStreamsOutputWithoutTouchingStatus(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess("api", ProcessConfig{
		Command: "sleep 60",
		Cwd:     dir,
		Tasks:   map[string]string{"hello": "echo migrate-ok"},
	}, 100)
	p.status = StatusRunning // pretend the service is up

	if err := p.RunTask("hello", func() {}); err != nil {
		t.Fatalf("run task: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(strings.Join(p.Logs(), "\n"), "[task hello] done")
	})
	logs := strings.Join(p.Logs(), "\n")
	if !strings.Contains(logs, "[task hello] $ echo migrate-ok") {
		t.Fatalf("expected task command echoed, got %q", logs)
	}
	if !strings.Contains(logs, "[task hello] migrate-ok") {
		t.Fatalf("expected task output captured, got %q", logs)
	}
	if p.Status() != StatusRunning {
		t.Fatalf("task must not change status, got %q", p.Status())
	}
}

func TestRunTaskRunsInProcessCwd(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess("api", ProcessConfig{
		Cwd:   dir,
		Tasks: map[string]string{"pwd": "pwd"},
	}, 100)
	if err := p.RunTask("pwd", func() {}); err != nil {
		t.Fatalf("run task: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(strings.Join(p.Logs(), "\n"), "[task pwd] done")
	})
	if !strings.Contains(strings.Join(p.Logs(), "\n"), dir) {
		t.Fatalf("expected task to run in %q; logs: %q", dir, p.Logs())
	}
}

func TestRunTaskUnknown(t *testing.T) {
	p := NewProcess("api", ProcessConfig{Tasks: map[string]string{"a": "true"}}, 10)
	if err := p.RunTask("missing", func() {}); err == nil {
		t.Fatal("expected error for unknown task")
	}
}

func TestRunTaskFailureBumpsBadgeWhenDetecting(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess("api", ProcessConfig{
		Cwd:   dir,
		Tasks: map[string]string{"boom": "exit 3"},
	}, 100)
	p.detectErrors = true
	if err := p.RunTask("boom", func() {}); err != nil {
		t.Fatalf("run task: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(strings.Join(p.Logs(), "\n"), "[task boom] exited")
	})
	if p.Errors() == 0 {
		t.Fatal("expected failed task to bump the error badge")
	}
}

func TestLoadConfigAcceptsTasks(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
    tasks:
      migrate: mise run migrate
      seed: python manage.py seed
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	tasks := cfg.Processes["api"].Tasks
	if tasks["migrate"] != "mise run migrate" || tasks["seed"] != "python manage.py seed" {
		t.Fatalf("unexpected tasks: %#v", tasks)
	}
}

func TestLoadConfigRejectsEmptyTaskCommand(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
    tasks:
      migrate: ""
`)
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected empty task command to be rejected")
	}
}

func TestStandaloneTasksLoadAndAppendAfterProcesses(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  backend:
    command: echo ok
tasks:
  deploy:
    command: echo deploy
    color: "#f97316"
  clear-cache:
    command: echo clear
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Tasks) != 2 || cfg.Tasks["deploy"].Command != "echo deploy" {
		t.Fatalf("unexpected tasks: %#v", cfg.Tasks)
	}
	// cwd resolves relative to the config dir.
	if cfg.Tasks["deploy"].Cwd != dir {
		t.Fatalf("expected task cwd %q, got %q", dir, cfg.Tasks["deploy"].Cwd)
	}

	m := newModel(cfg)
	if m.numProcesses != 1 {
		t.Fatalf("expected 1 process, got numProcesses=%d", m.numProcesses)
	}
	if len(m.processes) != 3 {
		t.Fatalf("expected 3 entries (1 process + 2 tasks), got %d", len(m.processes))
	}
	if m.processes[0].Name != "backend" || m.processes[0].oneShot {
		t.Fatalf("expected backend first as a service, got %+v", m.processes[0])
	}
	// Tasks follow in YAML order.
	if m.processes[1].Name != "deploy" || !m.processes[1].oneShot {
		t.Fatalf("expected deploy one-shot second, got %+v", m.processes[1])
	}
	if m.processes[2].Name != "clear-cache" || !m.processes[2].oneShot {
		t.Fatalf("expected clear-cache one-shot third, got %+v", m.processes[2])
	}
}

func TestStandaloneTaskRunsAndReturnsIdle(t *testing.T) {
	dir := t.TempDir()
	m := newModel(Config{
		Tasks:     map[string]TaskConfig{"deploy": {Command: "echo deployed", Cwd: dir}},
		taskOrder: []string{"deploy"},
	})
	p := m.processByName("deploy")
	if p == nil || !p.oneShot {
		t.Fatal("expected a one-shot process named deploy")
	}
	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start one-shot: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return p.Status() == StatusStopped })
	if !strings.Contains(strings.Join(p.Logs(), "\n"), "deployed") {
		t.Fatalf("expected task output, got %q", p.Logs())
	}
	// A stopped one-shot displays as "idle".
	if got := oneShotStatusLabel(p, p.Status()); got != "idle" {
		t.Fatalf("expected idle label, got %q", got)
	}
}

func TestLoadConfigRejectsTaskProcessNameClash(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  worker:
    command: echo ok
tasks:
  worker:
    command: echo clash
`)
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected clash between task and process name to be rejected")
	}
}

func TestLoadConfigRejectsEmptyStandaloneTask(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
tasks:
  deploy:
    command: ""
`)
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected empty standalone task command to be rejected")
	}
}

func TestReorderPinsOneShots(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
version: 1
processes:
  api:
    command: echo ok
    group: core
  web:
    command: echo ok
    group: core
  worker:
    command: echo ok
    group: jobs
tasks:
  deploy:
    command: echo deploy
    group: release
  backup:
    command: echo backup
    group: release
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	m := newModel(cfg)
	m.configPath = path

	m.selectByName("deploy")
	if cmd := m.moveSelectedCmd(1); cmd == nil {
		t.Fatal("expected moving a task within its section to be allowed")
	} else if msg, ok := cmd().(orderSavedMsg); !ok || msg.err != nil {
		t.Fatalf("task order save failed: %#v", msg)
	}
	cfg2, err := loadConfig(path)
	if err != nil {
		t.Fatalf("reordered config must load: %v", err)
	}
	if got := cfg2.taskOrder; len(got) != 2 || got[0] != "backup" || got[1] != "deploy" {
		t.Fatalf("expected tasks reordered to [backup deploy], got %v", got)
	}

	m.selectByName("worker")
	cmd := m.moveSelectedCmd(1)
	if cmd != nil {
		t.Fatal("expected a service at its section edge not to move into the next section")
	}

	m.selectByName("api")
	cmd = m.moveSelectedCmd(-1)
	if cmd != nil {
		t.Fatal("expected a service at its section edge not to move above its section")
	}
	cmd = m.moveSelectedCmd(1)
	if cmd == nil {
		t.Fatal("expected moving a service within its section to be allowed")
	}
	if msg, ok := cmd().(orderSavedMsg); !ok || msg.err != nil {
		t.Fatalf("order save failed: %#v", msg)
	}
	cfg2, err = loadConfig(path)
	if err != nil {
		t.Fatalf("reordered config must load: %v", err)
	}
	if len(cfg2.processOrder) != 3 || cfg2.processOrder[0] != "web" || cfg2.processOrder[1] != "api" || cfg2.processOrder[2] != "worker" {
		t.Fatalf("expected processes reordered to [web api worker], got %v", cfg2.processOrder)
	}
	if current := m.current(); current == nil || current.Name != "api" {
		t.Fatalf("expected selection to stay on api after reorder, got %#v", current)
	}
	if m.processes[0].Name != "web" || m.processes[1].Name != "api" || m.processes[2].Name != "worker" || m.processes[3].Name != "backup" || m.processes[4].Name != "deploy" {
		t.Fatalf("expected storage order to match configured services and tasks, got %v", processNames(m))
	}
}

func TestReorderMovesWholeSectionAndKeepsOtherLast(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
    group: core
  worker:
    command: echo ok
    group: jobs
  misc:
    command: echo ok
    group: Other
tasks:
  release:
    command: echo ok
    group: release
  migrate:
    command: echo ok
    group: core
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg)
	m.configPath = path
	if !m.selectHeader("core") {
		t.Fatal("expected core header")
	}
	cmd := m.moveSelectedCmd(1)
	if cmd == nil {
		t.Fatal("expected service-bearing section move")
	}
	if msg := cmd().(orderSavedMsg); msg.err != nil {
		t.Fatalf("section order save failed: %v", msg.err)
	}
	wantSections := []string{"jobs", "core", "release", "Other"}
	gotSections := make([]string, 0, len(m.sections()))
	for _, current := range m.sections() {
		gotSections = append(gotSections, current.Name)
	}
	if !reflect.DeepEqual(gotSections, wantSections) {
		t.Fatalf("expected section order %v, got %v", wantSections, gotSections)
	}
	cfg, err = loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.processOrder, []string{"worker", "api", "misc"}) {
		t.Fatalf("expected full process order [worker api misc], got %v", cfg.processOrder)
	}
	if !reflect.DeepEqual(cfg.taskOrder, []string{"migrate", "release"}) {
		t.Fatalf("expected section-scoped task order [migrate release], got %v", cfg.taskOrder)
	}
	if !m.selectHeader("Other") {
		t.Fatal("expected Other header")
	}
	if cmd := m.moveSelectedCmd(-1); cmd != nil {
		t.Fatal("expected Other section movement to be refused")
	}
	if got := m.sections()[len(m.sections())-1].Name; got != "Other" {
		t.Fatalf("expected Other to remain last, got %q", got)
	}
}

func TestReorderRefusesTaskOnlySectionAboveServices(t *testing.T) {
	m := newModel(Config{
		Processes:    map[string]ProcessConfig{"api": {Command: "echo ok", Group: "core"}},
		processOrder: []string{"api"},
		Tasks: map[string]TaskConfig{
			"deploy":  {Command: "echo ok", Group: "release"},
			"publish": {Command: "echo ok", Group: "docs"},
		},
		taskOrder: []string{"deploy", "publish"},
	})
	if !m.selectHeader("release") {
		t.Fatal("expected release header")
	}
	if cmd := m.moveSelectedCmd(-1); cmd != nil {
		t.Fatal("expected task-only section not to move above a service-bearing section")
	}
	if got, want := m.statusText, "Section can't cross service/task tiers"; got != want {
		t.Fatalf("expected refusal status %q, got %q", want, got)
	}
}

func TestReorderMovesTaskOnlySectionWithinTier(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
    group: core
tasks:
  deploy:
    command: echo ok
    group: release
  publish:
    command: echo ok
    group: docs
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg)
	m.configPath = path
	if !m.selectHeader("release") {
		t.Fatal("expected release header")
	}
	cmd := m.moveSelectedCmd(1)
	if cmd == nil {
		t.Fatal("expected adjacent task-only section move")
	}
	if msg := cmd().(orderSavedMsg); msg.err != nil {
		t.Fatalf("task-only section order save failed: %v", msg.err)
	}
	gotSections := make([]string, 0, len(m.sections()))
	for _, current := range m.sections() {
		gotSections = append(gotSections, current.Name)
	}
	if !reflect.DeepEqual(gotSections, []string{"core", "docs", "release"}) {
		t.Fatalf("expected task-only section order [core docs release], got %v", gotSections)
	}
	cfg, err = loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.taskOrder, []string{"publish", "deploy"}) {
		t.Fatalf("expected task order [publish deploy], got %v", cfg.taskOrder)
	}
}

func TestReorderRefusesOrphanMembersAndPreservesOrphanStorageOrder(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
    group: core
  web:
    command: echo ok
    group: core
tasks:
  deploy:
    command: echo ok
    group: release
  cleanup:
    command: echo ok
    group: release
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(cfg)
	m.configPath = path
	orphanServiceA := NewProcess("orphan-service-a", ProcessConfig{Command: "echo a", Group: "core"}, 100)
	orphanServiceA.orphaned = true
	orphanServiceB := NewProcess("orphan-service-b", ProcessConfig{Command: "echo b", Group: "core"}, 100)
	orphanServiceB.orphaned = true
	orphanTaskA := NewProcess("orphan-task-a", ProcessConfig{Command: "echo a", Group: "release"}, 100)
	orphanTaskA.oneShot = true
	orphanTaskA.orphaned = true
	orphanTaskB := NewProcess("orphan-task-b", ProcessConfig{Command: "echo b", Group: "release"}, 100)
	orphanTaskB.oneShot = true
	orphanTaskB.orphaned = true
	m.processes = []*Process{m.processByName("api"), m.processByName("web"), orphanServiceA, orphanServiceB, m.processByName("deploy"), m.processByName("cleanup"), orphanTaskA, orphanTaskB}
	m.numProcesses = 2
	if !m.selectByName("orphan-service-a") {
		t.Fatal("expected orphan member")
	}
	if cmd := m.moveSelectedCmd(1); cmd != nil {
		t.Fatal("expected orphan member movement to be refused")
	}
	m.selectByName("api")
	cmd := m.moveSelectedCmd(1)
	if cmd == nil {
		t.Fatal("expected configured service movement")
	}
	if msg := cmd().(orderSavedMsg); msg.err != nil {
		t.Fatalf("service reorder failed: %v", msg.err)
	}
	want := []string{"web", "api", "orphan-service-a", "orphan-service-b", "deploy", "cleanup", "orphan-task-a", "orphan-task-b"}
	if got := processNames(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected four storage segments and orphan order %v, got %v", want, got)
	}
}

func TestWebPendingOrderAcceptsMixedNamesAndPreservesOmittedTasks(t *testing.T) {
	m := newModel(Config{
		Processes: map[string]ProcessConfig{
			"api":    {Command: "echo ok", Group: "core"},
			"web":    {Command: "echo ok", Group: "core"},
			"worker": {Command: "echo ok", Group: "jobs"},
		},
		processOrder: []string{"api", "web", "worker"},
		Tasks: map[string]TaskConfig{
			"deploy": {Command: "echo ok", Group: "release"},
			"backup": {Command: "echo ok", Group: "release"},
		},
		taskOrder: []string{"deploy", "backup"},
	})
	m.requestOrder([]string{"worker", "backup", "web", "deploy", "api"})
	m.applyPendingOrder()
	if got := processNames(m); !reflect.DeepEqual(got, []string{"worker", "web", "api", "backup", "deploy"}) {
		t.Fatalf("expected mixed order applied within four storage segments, got %v", got)
	}
	m.requestOrder([]string{"api", "worker", "web"})
	m.applyPendingOrder()
	if got := processNames(m); !reflect.DeepEqual(got, []string{"api", "worker", "web", "backup", "deploy"}) {
		t.Fatalf("service-only order must leave task order unchanged, got %v", got)
	}
}

func TestReorderRefusesCrossKindAndSectionEdges(t *testing.T) {
	newGroupedModel := func() *model {
		return newModel(Config{
			Processes: map[string]ProcessConfig{
				"api":    {Command: "echo ok", Group: "core"},
				"web":    {Command: "echo ok", Group: "core"},
				"worker": {Command: "echo ok", Group: "jobs"},
			},
			processOrder: []string{"api", "web", "worker"},
			Tasks: map[string]TaskConfig{
				"migrate": {Command: "echo ok", Group: "core"},
				"deploy":  {Command: "echo ok", Group: "release"},
			},
			taskOrder: []string{"migrate", "deploy"},
		})
	}
	for _, test := range []struct {
		name       string
		delta      int
		wantStatus string
	}{
		{name: "web", delta: 1, wantStatus: "Member can't move outside its same-kind section members"},
		{name: "worker", delta: -1, wantStatus: "Member can't move outside its same-kind section members"},
		{name: "api", delta: -1, wantStatus: "Member can't move outside its same-kind section members"},
		{name: "migrate", delta: 1, wantStatus: "Member can't move outside its same-kind section members"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newGroupedModel()
			if !m.selectByName(test.name) {
				t.Fatalf("expected %s member", test.name)
			}
			if cmd := m.moveSelectedCmd(test.delta); cmd != nil {
				t.Fatalf("expected %s movement by %d to be refused", test.name, test.delta)
			}
			if got := m.statusText; got != test.wantStatus {
				t.Fatalf("expected refusal status %q, got %q", test.wantStatus, got)
			}
		})
	}
}

func TestErrorDetectionCountsAndMarkClears(t *testing.T) {
	p := NewProcess("test", ProcessConfig{}, 100)
	p.detectErrors = true
	p.capture(strings.NewReader(
		"Traceback (most recent call last):\n"+
			`  File "app.py", line 1, in <module>`+"\n"+
			"ValueError: boom\n"+
			"normal line\n"), "stderr", func() {})

	if got := p.Errors(); got != 2 {
		t.Fatalf("expected 2 error lines, got %d; logs: %q", got, p.Logs())
	}
	p.Mark()
	if got := p.Errors(); got != 0 {
		t.Fatalf("expected mark to clear errors, got %d", got)
	}
}

func TestErrorDetectionDisabledByDefault(t *testing.T) {
	p := NewProcess("test", ProcessConfig{}, 100)
	p.capture(strings.NewReader("panic: boom\n"), "stderr", func() {})
	if got := p.Errors(); got != 0 {
		t.Fatalf("expected no detection when disabled, got %d", got)
	}
}

func TestErrorLinePatterns(t *testing.T) {
	match := []string{
		"Traceback (most recent call last):",
		"ValueError: invalid literal",
		"panic: runtime error: index out of range",
		"fatal error: all goroutines are asleep",
		"TypeError: Cannot read properties of undefined",
		"Error: connect ECONNREFUSED",
		"npm ERR! code ELIFECYCLE",
		"error[E0308]: mismatched types",
		"2026-07-22 10:00:00 ERROR something broke",
		"app.py:10: error: something",
		"java.lang.NullPointerException",
		"UnhandledPromiseRejection: oops",
	}
	noMatch := []string{
		"0 failed, 12 passed",
		"no errors here",
		"GET /api/users 200",
		"compiling without issue",
		"errors: 0",
	}
	for _, line := range match {
		if !errLineRe.MatchString(line) {
			t.Errorf("expected match: %q", line)
		}
	}
	for _, line := range noMatch {
		if errLineRe.MatchString(line) {
			t.Errorf("expected no match: %q", line)
		}
	}
}

func TestStartClearsErrorCount(t *testing.T) {
	p := NewProcess("test", ProcessConfig{Command: "true", Cwd: t.TempDir()}, 10)
	p.detectErrors = true
	p.errCount = 5
	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return p.Status() == StatusStopped })
	if got := p.Errors(); got != 0 {
		t.Fatalf("expected start to clear errors, got %d", got)
	}
}

func TestLoadConfigAcceptsHighlightErrors(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
ui:
  highlight_errors: true
processes:
  api:
    command: echo ok
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.UI.HighlightErrors {
		t.Fatal("expected highlight_errors to be true")
	}
	m := newModel(cfg)
	if !m.processByName("api").detectErrors {
		t.Fatal("expected process detectErrors enabled")
	}
}

func TestStartFailureSetsFailedStatus(t *testing.T) {
	p := NewProcess("test", ProcessConfig{
		Command: "true",
		Cwd:     filepath.Join(t.TempDir(), "missing"),
	}, 10)

	err := p.Start(func() {})
	if err == nil {
		t.Fatal("expected start to fail")
	}
	if p.Status() != StatusFailed {
		t.Fatalf("expected failed status, got %q", p.Status())
	}
	if logs := strings.Join(p.Logs(), "\n"); !strings.Contains(logs, "start failed") {
		t.Fatalf("expected start failure in logs, got %q", logs)
	}
}

func TestStartInheritsUserPath(t *testing.T) {
	dir := t.TempDir()
	probePath := filepath.Join(dir, "stacker-path-probe")
	if err := os.WriteFile(probePath, []byte("#!/bin/sh\necho inherited-path\n"), 0o755); err != nil {
		t.Fatalf("write path probe: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := NewProcess("test", ProcessConfig{
		Command: "stacker-path-probe",
		Cwd:     dir,
	}, 10)
	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		status := p.Status()
		return status == StatusStopped || status == StatusFailed
	})

	if p.Status() != StatusStopped {
		t.Fatalf("expected stopped status, got %q; logs: %q", p.Status(), p.Logs())
	}
	if logs := strings.Join(p.Logs(), "\n"); !strings.Contains(logs, "inherited-path") {
		t.Fatalf("command was not found through inherited PATH; logs: %q", logs)
	}
}

func TestStopWaitsForProcessExit(t *testing.T) {
	p := NewProcess("test", ProcessConfig{
		Command:         "trap 'exit 0' TERM; while :; do sleep 1; done",
		Cwd:             t.TempDir(),
		GracefulTimeout: "2s",
	}, 20)

	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if err := p.Stop(func() {}); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if p.Status() != StatusStopped {
		t.Fatalf("expected stopped status, got %q", p.Status())
	}
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd != nil {
		t.Fatal("process command was not cleared after stop")
	}
}

func TestStopTerminatesChildProcessGroup(t *testing.T) {
	dir := t.TempDir()
	startedFile := filepath.Join(dir, "child.started")
	stoppedFile := filepath.Join(dir, "child.stopped")
	p := NewProcess("test", ProcessConfig{
		Command:         `sh -c 'trap "echo stopped > child.stopped; exit 0" TERM; echo started > child.started; while :; do sleep 1; done' & wait`,
		Cwd:             dir,
		GracefulTimeout: "2s",
	}, 20)

	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, err := os.Stat(startedFile)
		return err == nil
	})

	if err := p.Stop(func() {}); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, err := os.Stat(stoppedFile)
		return err == nil
	})
}

func TestLoadConfigResolvesRelativeWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	workingDir := filepath.Join(dir, "app")
	if err := os.Mkdir(workingDir, 0o755); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	path := writeConfig(t, dir, `
version: 1
ui:
  wheel_lines: 3
  max_log_lines: 100
processes:
  app:
    command: echo ok
    cwd: ./app
    graceful_timeout: 1s
`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Processes["app"].Cwd; got != workingDir {
		t.Fatalf("expected cwd %q, got %q", workingDir, got)
	}
}

func TestLoadConfigGroups(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
    group: " hub "
  worker:
    command: echo ok
    group: "   "
tasks:
  deploy:
    command: echo deploy
    group: " hub "
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Processes["api"].Group; got != "hub" {
		t.Fatalf("expected trimmed process group hub, got %q", got)
	}
	if got := cfg.Processes["worker"].Group; got != "" {
		t.Fatalf("expected whitespace-only process group to be unset, got %q", got)
	}
	if got := cfg.Tasks["deploy"].Group; got != "hub" {
		t.Fatalf("expected trimmed task group hub, got %q", got)
	}

	m := newModel(cfg)
	api := m.processByName("api")
	deploy := m.processByName("deploy")
	if api == nil || api.Group() != "hub" {
		t.Fatalf("expected api runtime group hub, got %#v", api)
	}
	if deploy == nil || deploy.Group() != "hub" {
		t.Fatalf("expected deploy runtime group hub, got %#v", deploy)
	}
	deploy.SetGroup("runtime")
	if got := deploy.Group(); got != "runtime" {
		t.Fatalf("expected runtime group, got %q", got)
	}

	reloadedPath := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: echo ok
tasks:
  deploy:
    command: echo deploy
    group: " platform "
`)
	reloaded, err := loadConfig(reloadedPath)
	if err != nil {
		t.Fatalf("load reloaded config: %v", err)
	}
	m.applyConfigDiff(reloaded)
	if got := m.processByName("deploy").Group(); got != "platform" {
		t.Fatalf("expected reloaded task group platform, got %q", got)
	}
}

func TestLoadConfigRejectsInvalidInput(t *testing.T) {
	tests := map[string]string{
		"unknown field": `
version: 1
unexpected: true
processes:
  app:
    command: true
`,
		"unsupported version": `
version: 2
processes:
  app:
    command: true
`,
		"empty command": `
version: 1
processes:
  app:
    command: ""
`,
		"invalid timeout": `
version: 1
processes:
  app:
    command: true
    graceful_timeout: later
`,
		"invalid color": `
version: 1
processes:
  app:
    command: true
    color: "#12345g"
`,
		"multiple documents": `
version: 1
processes:
  app:
    command: true
---
version: 1
processes:
  other:
    command: true
`,
	}

	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), contents)
			if _, err := loadConfig(path); err == nil {
				t.Fatal("expected config to be rejected")
			}
		})
	}
}

func writeConfig(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "stacker.yml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
