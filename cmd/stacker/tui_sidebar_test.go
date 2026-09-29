package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func sidebarProcesses() map[string]ProcessConfig {
	processes := make(map[string]ProcessConfig, 30)
	for i := range 30 {
		processes[fmt.Sprintf("service-%02d", i)] = ProcessConfig{Command: "true", Group: "core"}
	}
	return processes
}

func TestSessionSidebarScrollKeepsSelectedRowVisible(t *testing.T) {
	m := newModel(Config{Processes: sidebarProcesses()})
	m.width, m.height = 100, 12
	m.selected = 20
	m.View()
	list := ansi.Strip(m.processList())
	if got, want := len(strings.Split(list, "\n")), 1+sidebarVisibleRows(m.height, len(m.rows())); got != want {
		t.Fatalf("sidebar rows = %d, want %d", got, want)
	}
	if m.listOffset == 0 || !strings.Contains(list, m.current().Name) || strings.Contains(list, "service-00") {
		t.Fatalf("selected row not visible in clipped list: offset=%d list=%q", m.listOffset, list)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if !strings.Contains(ansi.Strip(m.processList()), m.current().Name) {
		t.Fatal("keyboard selection left the sidebar viewport")
	}
}

func TestSessionSidebarWheelAndClickRespectScrollOffset(t *testing.T) {
	m := newModel(Config{Processes: sidebarProcesses()})
	m.width, m.height = 100, 12
	m.handleMouse(tea.MouseMsg{X: 2, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m.listOffset <= 0 {
		t.Fatal("wheel over sidebar did not scroll the list")
	}
	row := m.listOffset + 1
	m.handleMouse(tea.MouseMsg{X: 2, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.selected != row || !strings.Contains(ansi.Strip(m.processList()), m.current().Name) {
		t.Fatalf("clicked row = %d, want %d at offset %d", m.selected, row, m.listOffset)
	}
}

func TestAttachSidebarScrollKeepsSelectionAndWheelInBounds(t *testing.T) {
	processes := make([]ProcessInfo, 30)
	for i := range processes {
		processes[i] = ProcessInfo{Name: fmt.Sprintf("service-%02d", i), Group: "core", Status: "stopped"}
	}
	m := newAttachModel(nil, t.TempDir()+"/stacker.yml")
	m.procs = processes
	m.width, m.height = 100, 12
	m.selected = 20
	m.View()
	if m.listOffset == 0 || !strings.Contains(ansi.Strip(m.processList()), m.currentName()) {
		t.Fatalf("attach selected row not visible at offset %d", m.listOffset)
	}
	m.Update(tea.MouseMsg{X: 2, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m.selected < m.listOffset || m.selected >= m.listOffset+sidebarVisibleRows(m.height, len(m.rows())) {
		t.Fatalf("attach wheel left selection outside viewport: selected=%d offset=%d", m.selected, m.listOffset)
	}
}

func TestSidebarScrollHelpExplainsWheelRouting(t *testing.T) {
	sessionHelp := ansi.Strip(newModel(Config{}).helpView())
	attachHelp := ansi.Strip(newAttachModel(nil, t.TempDir()+"/stacker.yml").helpView())
	for _, help := range []string{sessionHelp, attachHelp} {
		if !strings.Contains(help, "scroll process list") {
			t.Fatalf("help does not explain sidebar scroll: %q", help)
		}
	}
}
