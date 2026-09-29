package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *model) logChoices() []string {
	sections := buildSections(sectionMembers(m.procs()))
	choices := make([]string, 0)
	for _, current := range sections {
		for _, members := range [][]sectionMember{current.Services, current.Tasks} {
			for _, member := range members {
				if member.Orphaned || m.current() != nil && member.Name == m.current().Name {
					continue
				}
				choices = append(choices, member.Name)
			}
		}
	}
	return choices
}

func (m *model) openLogPicker() {
	if m.current() == nil {
		m.statusText = "Select a service or standalone task first"
		return
	}
	choices := m.logChoices()
	if len(choices) == 0 {
		m.statusText = "No other service or standalone task available"
		return
	}
	m.logChoice = 0
	for i, choice := range choices {
		if choice == m.secondaryName {
			m.logChoice = i
			break
		}
	}
	m.showLogPicker = true
}

func (m *model) handleLogPickerKey(key string) {
	choices := m.logChoices()
	if len(choices) == 0 {
		m.showLogPicker = false
		return
	}
	switch key {
	case "up", "k":
		m.logChoice = max(0, m.logChoice-1)
	case "down", "j":
		m.logChoice = min(len(choices)-1, m.logChoice+1)
	case "enter":
		m.secondaryName = choices[clamp(m.logChoice, 0, len(choices)-1)]
		m.secondaryPane = logPaneState{Follow: true, Wrap: m.cfg.UI.WordWrap, SelStart: -1, SelEnd: -1}
		m.requestedOrientation = stackedOrientation
		m.activePane = 1
		m.showLogPicker = false
		m.statusText = "Comparing with " + m.secondaryName
	case "esc":
		m.showLogPicker = false
	default:
		m.showLogPicker = false
	}
}

func (m *model) logPickerView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Choose second log"))
	b.WriteByte('\n')
	for i, name := range m.logChoices() {
		label := "  " + sanitizeLogLine(name)
		if i == m.logChoice {
			label = selectedProcessStyle.Render("› " + sanitizeLogLine(name))
		}
		b.WriteString(label)
		b.WriteByte('\n')
	}
	b.WriteString(mutedStyle.Render("↑/↓ select · enter show · esc cancel"))
	return lipgloss.NewStyle().MaxWidth(m.width).Render(b.String())
}

func (m *model) reconcileSecondary() {
	if m.secondaryName == "" {
		return
	}
	if primary := m.current(); primary != nil && primary.Name == m.secondaryName {
		m.secondaryName = ""
		m.activePane = 0
		m.statusText = "Comparison closed: selected service is already the second log"
		return
	}
	if m.processByName(m.secondaryName) == nil {
		name := m.secondaryName
		m.secondaryName = ""
		m.activePane = 0
		m.statusText = "Comparison closed: " + name + " is no longer available"
	}
}

func (m *model) visiblePaneRects() (paneRect, paneRect, bool) {
	leftWidth := m.leftWidth()
	primary := paneRect{X: leftWidth, Y: 0, Width: max(20, m.width-leftWidth-1), Height: max(5, m.height-2)}
	if m.secondaryName == "" || m.processByName(m.secondaryName) == nil {
		return primary, paneRect{}, false
	}
	first, second, _, visible := logPaneRects(m.width, m.height, leftWidth, m.requestedOrientation)
	if !visible {
		return primary, paneRect{}, false
	}
	return first, second, true
}

func (m *model) activePaneRect() paneRect {
	primary, secondary, visible := m.visiblePaneRects()
	if visible && m.activePane == 1 {
		return secondary
	}
	return primary
}

func (m *model) scrollActivePane(delta int) {
	if p, pane := m.activeLogPane(); p != nil {
		scrollLogPane(p, pane, m.activePaneRect(), delta)
	}
}

func (m *model) paneAt(x, y int) (*Process, *logPaneState, paneRect, int, bool) {
	primary, secondary, visible := m.visiblePaneRects()
	inside := func(rect paneRect) bool {
		return x >= rect.X+1 && x < rect.X+rect.Width-1 && y >= rect.Y+2 && y < rect.Y+rect.Height-1
	}
	if visible && inside(secondary) {
		return m.processByName(m.secondaryName), &m.secondaryPane, secondary, 1, true
	}
	if inside(primary) {
		return m.current(), &m.primaryPane, primary, 0, true
	}
	return nil, nil, paneRect{}, 0, false
}
