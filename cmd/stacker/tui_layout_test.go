package main

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestLogPaneRectsAlignWithRenderedPanels(t *testing.T) {
	primary, secondary, _, _ := logPaneRects(120, 30, 30, sideBySideOrientation)
	first := panelStyle.Width(primary.Width - 3).Height(primary.Height - 2).Render("x")
	if secondary.X != primary.X+lipgloss.Width(first) {
		t.Fatalf("second column starts at %d, rendered first ends at %d", secondary.X, primary.X+lipgloss.Width(first))
	}
	primary, secondary, _, _ = logPaneRects(80, 30, 27, stackedOrientation)
	first = panelStyle.Width(primary.Width - 3).Height(primary.Height - 2).Render("x")
	if secondary.Y != primary.Y+lipgloss.Height(first) {
		t.Fatalf("lower pane starts at %d, rendered upper ends at %d", secondary.Y, primary.Y+lipgloss.Height(first))
	}
}

func TestClampSidebarWidth(t *testing.T) {
	for _, tc := range []struct{ screen, preferred, fallback, want int }{
		{40, 34, 24, 17},
		{80, 28, 26, 28},
		{160, 120, 34, 80},
		{100, 0, 34, 34},
	} {
		if got := clampSidebarWidth(tc.screen, tc.preferred, tc.fallback); got != tc.want {
			t.Fatalf("clampSidebarWidth(%d, %d, %d) = %d, want %d", tc.screen, tc.preferred, tc.fallback, got, tc.want)
		}
	}
}

func TestLogPaneRectsResponsiveFallback(t *testing.T) {
	primary, secondary, layout, visible := logPaneRects(120, 30, 30, sideBySideOrientation)
	if !visible || layout != sideBySideOrientation || primary.Width < 28 || secondary.Width < 28 || primary.X+primary.Width-1 != secondary.X {
		t.Fatalf("side-by-side layout = %+v %+v %s %v", primary, secondary, layout, visible)
	}
	primary, secondary, layout, visible = logPaneRects(80, 30, 27, sideBySideOrientation)
	if !visible || layout != stackedOrientation || primary.Height < 5 || secondary.Height < 5 || primary.Y+primary.Height != secondary.Y {
		t.Fatalf("stacked fallback = %+v %+v %s %v", primary, secondary, layout, visible)
	}
	_, _, _, visible = logPaneRects(80, 10, 27, stackedOrientation)
	if visible {
		t.Fatal("short terminal should hide secondary panel")
	}
}

func TestSessionSidebarResizeKeysPersistWidth(t *testing.T) {
	m := newModel(Config{})
	m.width, m.height = 100, 24
	m.collapsedPath = filepath.Join(t.TempDir(), "ui.json")
	initial := m.leftWidth()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	if m.leftWidth() != initial+2 {
		t.Fatalf("sidebar width = %d, want %d", m.leftWidth(), initial+2)
	}
	state, err := loadUIState(m.collapsedPath)
	if err != nil || state.SidebarWidth != initial+2 {
		t.Fatalf("saved width = %d, err = %v", state.SidebarWidth, err)
	}
}

func TestAttachSidebarResizeKeysPersistWidth(t *testing.T) {
	m := newAttachModel(nil, filepath.Join(t.TempDir(), "stacker.yml"))
	m.width, m.height = 100, 24
	m.collapsedPath = filepath.Join(t.TempDir(), "ui.json")
	initial := m.leftWidth()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	if m.leftWidth() != initial+2 {
		t.Fatalf("attach sidebar width = %d, want %d", m.leftWidth(), initial+2)
	}
	state, err := loadUIState(m.collapsedPath)
	if err != nil || state.SidebarWidth != initial+2 {
		t.Fatalf("attach saved width = %d, err = %v", state.SidebarWidth, err)
	}
}

func TestSessionSidebarDividerDragPersistsWidthWithoutSelectingLog(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{"api": {Command: "true"}}})
	m.width, m.height = 100, 24
	m.collapsedPath = filepath.Join(t.TempDir(), "ui.json")
	initial := m.leftWidth()
	m.handleMouse(tea.MouseMsg{X: initial - 1, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m.handleMouse(tea.MouseMsg{X: initial + 7, Y: 5, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m.handleMouse(tea.MouseMsg{X: initial + 7, Y: 5, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	if m.leftWidth() <= initial || m.primaryPane.Selecting {
		t.Fatalf("drag result width=%d selection=%v", m.leftWidth(), m.primaryPane.Selecting)
	}
	state, err := loadUIState(m.collapsedPath)
	if err != nil || state.SidebarWidth != m.leftWidth() {
		t.Fatalf("persisted width = %d, err = %v", state.SidebarWidth, err)
	}
}
