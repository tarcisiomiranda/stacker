package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestPausedPaneKeepsAbsoluteAnchorWhileLogsArrive(t *testing.T) {
	p := NewProcess("api", ProcessConfig{}, 10)
	for _, line := range []string{"first", "second", "third"} {
		p.appendLog(line)
	}
	paused := logPaneState{TopLine: 0, Follow: false, ExplicitPause: true, SelStart: -1, SelEnd: -1}
	live := logPaneState{Follow: true, SelStart: -1, SelEnd: -1}
	rect := paneRect{Width: 40, Height: 5}
	before := renderLogPane(p, &paused, rect, true)
	p.appendLog("fourth")
	after := renderLogPane(p, &paused, rect, true)
	if paused.TopLine != 0 || !strings.Contains(before, "first") || !strings.Contains(after, "first") || strings.Contains(after, "fourth") || !live.Follow {
		t.Fatalf("paused pane moved or lost independence: state=%+v before=%q after=%q live=%+v", paused, before, after, live)
	}
}

func TestPausedPaneClampsAfterBufferEviction(t *testing.T) {
	p := NewProcess("api", ProcessConfig{}, 2)
	p.appendLog("first")
	p.appendLog("second")
	state := logPaneState{Follow: false, ExplicitPause: true, TopLine: 0, SelStart: -1, SelEnd: -1}
	p.appendLog("third")
	view := renderLogPane(p, &state, paneRect{Width: 40, Height: 6}, true)
	if state.TopLine != 1 || !state.Evicted || strings.Contains(view, "first") || !strings.Contains(view, "second") {
		t.Fatalf("evicted pane state=%+v view=%q", state, view)
	}
}

func TestSelectedPaneTextUsesFocusedProcess(t *testing.T) {
	p := NewProcess("worker", ProcessConfig{}, 10)
	p.appendLog("worker line")
	state := logPaneState{SelStart: 0, SelEnd: 0}
	text, count := selectedPaneText(p, &state)
	if text != "worker line" || count != 1 {
		t.Fatalf("selected text = %q (%d), want worker line", text, count)
	}
}

func TestSessionLogViewUsesAbsolutePaneAnchor(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{"api": {Command: "true"}}})
	m.width, m.height = 100, 18
	p := m.current()
	p.setLogLimits(2, 4096)
	p.appendLog("first")
	p.appendLog("second")
	m.primaryPane.Follow = false
	m.primaryPane.TopLine = 0
	p.appendLog("third")
	view := m.logView()
	if m.primaryPane.TopLine != 1 || !m.primaryPane.Evicted || strings.Contains(view, "first") || !strings.Contains(view, "second") {
		t.Fatalf("session pane lost absolute anchor: state=%+v view=%q", m.primaryPane, view)
	}
	m.Update(refreshMsg{})
	if !strings.Contains(m.statusText, "oldest retained") || m.primaryPane.Evicted {
		t.Fatalf("buffer eviction notice missing: %q state=%+v", m.statusText, m.primaryPane)
	}
}

func TestSessionCompareLayoutRendersTwoLogs(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api":    {Command: "true"},
		"worker": {Command: "true"},
	}})
	m.width, m.height = 120, 30
	m.selectByName("api")
	m.secondaryName = "worker"
	m.requestedOrientation = stackedOrientation
	stacked := ansi.Strip(m.View())
	if !strings.Contains(stacked, "Logs: api") || !strings.Contains(stacked, "Logs: worker") {
		t.Fatalf("stacked comparison missing a log: %q", stacked)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if width := ansi.StringWidth(line); width > m.width {
			t.Fatalf("stacked line width %d exceeds terminal %d", width, m.width)
		}
	}
	m.requestedOrientation = sideBySideOrientation
	side := ansi.Strip(m.View())
	if !strings.Contains(side, "Logs: api") || !strings.Contains(side, "Logs: worker") {
		t.Fatalf("side-by-side comparison missing a log: %q", side)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if width := ansi.StringWidth(line); width > m.width {
			t.Fatalf("side-by-side line width %d exceeds terminal %d", width, m.width)
		}
	}
	m.width = 80
	if !strings.Contains(ansi.Strip(m.View()), "Logs: worker") || m.requestedOrientation != sideBySideOrientation {
		t.Fatal("narrow comparison did not stack without losing preferred orientation")
	}
	m.height = 10
	if strings.Contains(ansi.Strip(m.View()), "Logs: worker") || m.secondaryName != "worker" {
		t.Fatal("short viewport did not retain hidden secondary identity")
	}
}

func TestSecondLogPickerChoosesStandaloneTask(t *testing.T) {
	m := newModel(Config{
		Processes: map[string]ProcessConfig{"api": {Command: "true", Tasks: map[string]string{"lint": "true"}}},
		Tasks:     map[string]TaskConfig{"deploy": {Command: "true"}},
	})
	m.width, m.height = 100, 28
	m.selectByName("api")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if !m.showLogPicker || !strings.Contains(m.View(), "deploy") || strings.Contains(m.logPickerView(), "lint") {
		t.Fatalf("picker omitted standalone task or included nested task: %q", m.logPickerView())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.secondaryName != "deploy" || m.showLogPicker {
		t.Fatalf("selected second log = %q, picker open = %v", m.secondaryName, m.showLogPicker)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}})
	if m.requestedOrientation != sideBySideOrientation {
		t.Fatalf("orientation = %s, want side by side", m.requestedOrientation)
	}
}

func TestComparePauseAndFocusStayIndependent(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true"}, "worker": {Command: "true"},
	}})
	m.width, m.height = 120, 30
	m.selectByName("api")
	m.secondaryName = "worker"
	m.secondaryPane.Follow = true
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if m.activePane != 1 || m.secondaryPane.Follow || !m.secondaryPane.ExplicitPause || !m.primaryPane.Follow {
		t.Fatalf("pause leaked: primary=%+v secondary=%+v focus=%d", m.primaryPane, m.secondaryPane, m.activePane)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if !m.secondaryPane.Follow || m.secondaryPane.ExplicitPause {
		t.Fatalf("G did not resume secondary: %+v", m.secondaryPane)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'W'}})
	if !m.secondaryPane.Wrap || m.primaryPane.Wrap {
		t.Fatalf("wrap leaked: primary=%+v secondary=%+v", m.primaryPane, m.secondaryPane)
	}
}

func TestCompareCopyAndMarkUseFocusedProcess(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true"}, "worker": {Command: "true"},
	}})
	m.width, m.height = 120, 30
	m.selectByName("api")
	m.secondaryName = "worker"
	m.activePane = 1
	m.processByName("api").appendLog("api line")
	m.processByName("worker").appendLog("worker line")
	m.secondaryPane.SelStart, m.secondaryPane.SelEnd = 0, 0
	if text, count := m.selectedText(); text != "worker line" || count != 1 {
		t.Fatalf("focused selection = %q (%d)", text, count)
	}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if len(m.processByName("worker").Logs()) != 4 || len(m.processByName("api").Logs()) != 1 {
		t.Fatal("Space marked the list selection instead of the focused log")
	}
}

func TestCompareWheelFocusesTheHoveredLog(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true"}, "worker": {Command: "true"},
	}})
	m.width, m.height = 120, 30
	m.selectByName("api")
	m.secondaryName = "worker"
	for i := range 30 {
		m.processByName("api").appendLog(strings.Repeat("a", i+1))
		m.processByName("worker").appendLog(strings.Repeat("b", i+1))
	}
	m.scrollToBottom()
	_, secondary, _, _ := logPaneRects(m.width, m.height, m.leftWidth(), stackedOrientation)
	m.handleMouse(tea.MouseMsg{X: secondary.X + 4, Y: secondary.Y + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.activePane != 1 || m.secondaryPane.Follow || !m.primaryPane.Follow {
		t.Fatalf("wheel focus/scroll = primary=%+v secondary=%+v focus=%d", m.primaryPane, m.secondaryPane, m.activePane)
	}
}

func TestSideBySideWheelFocusesSecondaryAtItsFirstContentColumn(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true"}, "worker": {Command: "true"},
	}})
	m.width, m.height = 120, 30
	m.selectByName("api")
	m.secondaryName = "worker"
	m.requestedOrientation = sideBySideOrientation
	for range 30 {
		m.processByName("worker").appendLog("line")
	}
	_, secondary, _, _ := logPaneRects(m.width, m.height, m.leftWidth(), sideBySideOrientation)
	m.handleMouse(tea.MouseMsg{X: secondary.X + 1, Y: secondary.Y + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.activePane != 1 || m.secondaryPane.Follow != false || m.primaryPane.Follow != true {
		t.Fatalf("first secondary content column targeted wrong pane: focus=%d primary=%+v secondary=%+v", m.activePane, m.primaryPane, m.secondaryPane)
	}
}

func TestShortComparisonUsesVisiblePrimaryAndTransientNotice(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true"}, "worker": {Command: "true"},
	}})
	m.width, m.height = 80, 10
	m.selectByName("api")
	m.secondaryName = "worker"
	m.activePane = 1
	m.statusText = "Ready"
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Terminal too short") || m.statusText != "Ready" {
		t.Fatalf("short-window notice persisted or was hidden: view=%q status=%q", view, m.statusText)
	}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if len(m.processByName("api").Logs()) != 3 || len(m.processByName("worker").Logs()) != 0 {
		t.Fatal("short comparison marked the hidden secondary rather than visible primary")
	}
}

func TestStackedComparisonKeepsPausedPrimaryAnchor(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true"}, "worker": {Command: "true"},
	}})
	m.width, m.height = 100, 30
	m.selectByName("api")
	m.secondaryName = "worker"
	for i := range 30 {
		m.processByName("api").appendLog(strings.Repeat("a", i+1))
	}
	m.primaryPane.Follow = false
	m.primaryPane.ExplicitPause = true
	m.primaryPane.TopLine = 15
	m.View()
	if m.primaryPane.TopLine != 15 {
		t.Fatalf("rendering stacked panes moved primary anchor to %d", m.primaryPane.TopLine)
	}
}

func TestSessionCompareHelpShowsPaneAndResizeKeys(t *testing.T) {
	m := newModel(Config{})
	help := ansi.Strip(m.helpView())
	for _, text := range []string{"second log", "orientation", "auto-scroll", "sidebar", "Tab"} {
		if !strings.Contains(help, text) {
			t.Fatalf("session help missing %q", text)
		}
	}
	attach := newAttachModel(nil, "/tmp/stacker.yml")
	attachHelp := ansi.Strip(attach.helpView())
	if !strings.Contains(attachHelp, "sidebar") || strings.Contains(attachHelp, "second log") {
		t.Fatalf("attach help should only document sidebar resize: %q", attachHelp)
	}
}
