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

func TestLogPaneRectsRespectIndependentSplitShares(t *testing.T) {
	primary, secondary, orientation, visible := logPaneRectsWithShares(120, 30, 30, sideBySideOrientation, 0.75, 0.6)
	if !visible || orientation != sideBySideOrientation || primary.Width <= secondary.Width || primary.Width < 28 || secondary.Width < 28 {
		t.Fatalf("horizontal split = %+v %+v %s %v", primary, secondary, orientation, visible)
	}
	primary, secondary, orientation, visible = logPaneRectsWithShares(80, 30, 27, sideBySideOrientation, 0.75, 0.6)
	if !visible || orientation != stackedOrientation || primary.Height <= secondary.Height || primary.Height < 5 || secondary.Height < 5 {
		t.Fatalf("stacked fallback did not use its own share: %+v %+v %s %v", primary, secondary, orientation, visible)
	}
	primary, secondary, orientation, visible = logPaneRectsWithShares(120, 30, 30, sideBySideOrientation, 0.75, 0.6)
	if !visible || orientation != sideBySideOrientation || primary.Width <= secondary.Width {
		t.Fatalf("restored horizontal share = %+v %+v %s %v", primary, secondary, orientation, visible)
	}
}

func TestLogPaneRectsClampSplitToMinimumSizes(t *testing.T) {
	primary, secondary, _, _ := logPaneRectsWithShares(120, 30, 30, sideBySideOrientation, 0.5, 0.98)
	if primary.Width < 28 || secondary.Width < 28 {
		t.Fatalf("side-by-side minimum violated: %+v %+v", primary, secondary)
	}
	primary, secondary, _, _ = logPaneRectsWithShares(80, 30, 27, stackedOrientation, 0.98, 0.5)
	if primary.Height < 5 || secondary.Height < 5 {
		t.Fatalf("stacked minimum violated: %+v %+v", primary, secondary)
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

func TestComparePaneResizeKeysKeepSharesByOrientation(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true"}, "worker": {Command: "true"},
	}})
	m.width, m.height = 120, 30
	m.selectByName("api")
	m.secondaryName = "worker"
	first, _, _, _ := m.comparisonRects()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	stacked, _, _, _ := m.comparisonRects()
	if stacked.Height != first.Height+2 || m.sideShare != 0.5 {
		t.Fatalf("primary stacked resize = %d → %d, side share = %v", first.Height, stacked.Height, m.sideShare)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}})
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	horizontal, _, _, _ := m.comparisonRects()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	resized, _, _, _ := m.comparisonRects()
	if resized.Width != horizontal.Width-2 || m.activePane != 1 {
		t.Fatalf("secondary horizontal resize = %d → %d, focus = %d", horizontal.Width, resized.Width, m.activePane)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'-'}})
	shrunk, _, _, _ := m.comparisonRects()
	if shrunk.Width != horizontal.Width {
		t.Fatalf("shrinking the secondary did not restore primary width: got %d, want %d", shrunk.Width, horizontal.Width)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}})
	restored, _, _, _ := m.comparisonRects()
	if restored.Height != stacked.Height {
		t.Fatalf("stacked ratio was lost: want %d got %d", stacked.Height, restored.Height)
	}
}

func TestComparePaneDividerDragDoesNotSelectLogText(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true"}, "worker": {Command: "true"},
	}})
	m.width, m.height = 120, 30
	m.selectByName("api")
	m.secondaryName = "worker"
	primary, secondary, _, _ := m.comparisonRects()
	m.handleMouse(tea.MouseMsg{X: primary.X + 8, Y: secondary.Y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !m.resizingPane {
		t.Fatal("pressing the log divider did not start a resize")
	}
	m.handleMouse(tea.MouseMsg{X: primary.X + 8, Y: secondary.Y + 3, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m.handleMouse(tea.MouseMsg{X: primary.X + 8, Y: secondary.Y + 3, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	after, _, _, _ := m.comparisonRects()
	if after.Height != primary.Height+3 || m.resizingPane || m.primaryPane.Selecting || m.secondaryPane.Selecting {
		t.Fatalf("drag result = %+v, resizing=%v, selections=%v/%v", after, m.resizingPane, m.primaryPane.Selecting, m.secondaryPane.Selecting)
	}
	if m.preferredSidebarWidth != 0 {
		t.Fatal("log divider drag changed sidebar width")
	}
}

func TestSideBySideDividerDragPreservesStackedShareAndBounds(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true"}, "worker": {Command: "true"},
	}})
	m.width, m.height = 120, 30
	m.selectByName("api")
	m.secondaryName = "worker"
	m.requestedOrientation = sideBySideOrientation
	m.stackedShare = 0.4
	primary, secondary, _, _ := m.comparisonRects()
	m.handleMouse(tea.MouseMsg{X: secondary.X - 1, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m.handleMouse(tea.MouseMsg{X: secondary.X + 3, Y: 3, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m.handleMouse(tea.MouseMsg{X: secondary.X + 3, Y: 3, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	after, second, _, _ := m.comparisonRects()
	if after.Width != primary.Width+3 || second.Width < 28 || m.stackedShare != 0.4 {
		t.Fatalf("horizontal drag = %+v %+v, stacked share = %v", after, second, m.stackedShare)
	}
	m.width = 80
	_, _, fallback, visible := m.comparisonRects()
	if !visible || fallback != stackedOrientation {
		t.Fatal("side-by-side preference did not fall back to stacked")
	}
	m.width = 120
	restored, _, _, _ := m.comparisonRects()
	if restored.Width != after.Width {
		t.Fatalf("horizontal size was not restored: %d -> %d", after.Width, restored.Width)
	}
}

func TestComparePaneResizeKeysEnforceMinimums(t *testing.T) {
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true"}, "worker": {Command: "true"},
	}})
	m.width, m.height = 120, 30
	m.selectByName("api")
	m.secondaryName = "worker"
	m.requestedOrientation = sideBySideOrientation
	for range 100 {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	}
	primary, secondary, _, _ := m.comparisonRects()
	if primary.Width < 28 || secondary.Width != 28 {
		t.Fatalf("minimum side-by-side widths = %+v %+v", primary, secondary)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	for range 100 {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	}
	primary, secondary, _, _ = m.comparisonRects()
	if primary.Width != 28 || secondary.Width < 28 {
		t.Fatalf("opposite minimum widths = %+v %+v", primary, secondary)
	}
}
