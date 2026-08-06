package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func pickerRows() []instanceSummary {
	return []instanceSummary{
		{
			State: InstanceState{PID: 4510, Config: "/srv/_vsentinel_/bunker-orchestrator/stacker.yml", Mode: "serve"},
			Label: "bunker-orchestrator",
			Ports: []int{4510, 4520, 8080},
			Up:    1, Total: 4,
		},
		{
			State: InstanceState{PID: 29649, Config: "/srv/_cyber_/stacker.yml", Mode: "session"},
			Label: "_cyber_",
			Ports: []int{3001, 8080},
			Up:    2, Total: 20,
			Collide: []int{8080},
		},
	}
}

func key(k string) tea.KeyMsg {
	switch k {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
}

// The picker exists so the user can tell instances apart, so every fact needed
// for that has to be on screen: which project, which file, and what it holds.
func TestPickerViewShowsEachInstance(t *testing.T) {
	m := newPickerModel(pickerRows(), "/srv/stacker/stacker.yml")
	m.width, m.height = 100, 30
	view := m.View()

	for _, want := range []string{
		"bunker-orchestrator",
		"_cyber_",
		"/srv/_cyber_/stacker.yml",
		"serve",
		"session",
		"1/4",
		"2/20",
		"8080",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view missing %q:\n%s", want, view)
		}
	}
}

func TestPickerOffersLocalConfigWhenPresent(t *testing.T) {
	m := newPickerModel(pickerRows(), "/srv/stacker/stacker.yml")
	m.width, m.height = 100, 30
	if !strings.Contains(m.View(), "/srv/stacker/stacker.yml") {
		t.Fatalf("picker should offer starting the local config:\n%s", m.View())
	}
}

// Showing an entry that cannot work is worse than not showing it.
func TestPickerOmitsLocalConfigWhenAbsent(t *testing.T) {
	m := newPickerModel(pickerRows(), "")
	m.width, m.height = 100, 30
	if strings.Contains(m.View(), "start the config here") {
		t.Fatalf("picker should not offer a config that does not exist:\n%s", m.View())
	}
	if got := len(m.entries()); got != 2 {
		t.Fatalf("entries = %d, want 2 instances and no start row", got)
	}
}

func TestPickerEnterChoosesSelectedInstance(t *testing.T) {
	m := newPickerModel(pickerRows(), "/srv/stacker/stacker.yml")
	updated, _ := m.Update(key("down"))
	m = updated.(*pickerModel)
	updated, cmd := m.Update(key("enter"))
	m = updated.(*pickerModel)

	if m.action != pickerAttach {
		t.Fatalf("action = %v, want pickerAttach", m.action)
	}
	if m.chosen != "/srv/_cyber_/stacker.yml" {
		t.Fatalf("chosen = %q, want the second instance", m.chosen)
	}
	if cmd == nil {
		t.Fatal("enter should quit the picker")
	}
}

// The start row sits after the instances, so reaching it means moving past them.
func TestPickerEnterOnStartRowPicksLocalConfig(t *testing.T) {
	m := newPickerModel(pickerRows(), "/srv/stacker/stacker.yml")
	for i := 0; i < 2; i++ {
		updated, _ := m.Update(key("down"))
		m = updated.(*pickerModel)
	}
	updated, _ := m.Update(key("enter"))
	m = updated.(*pickerModel)

	if m.action != pickerStart {
		t.Fatalf("action = %v, want pickerStart", m.action)
	}
	if m.chosen != "/srv/stacker/stacker.yml" {
		t.Fatalf("chosen = %q, want the local config", m.chosen)
	}
}

// `n` is the short path to start the working-directory config without scrolling.
func TestPickerNStartsLocalConfig(t *testing.T) {
	m := newPickerModel(pickerRows(), "/srv/stacker/stacker.yml")
	updated, cmd := m.Update(key("n"))
	m = updated.(*pickerModel)
	if m.action != pickerStart {
		t.Fatalf("action = %v, want pickerStart", m.action)
	}
	if m.chosen != "/srv/stacker/stacker.yml" {
		t.Fatalf("chosen = %q, want the local config", m.chosen)
	}
	if cmd == nil {
		t.Fatal("n should quit the picker")
	}
}

func TestPickerNWithoutLocalConfigIsNoop(t *testing.T) {
	m := newPickerModel(pickerRows(), "")
	updated, cmd := m.Update(key("n"))
	m = updated.(*pickerModel)
	if m.action != pickerCancel {
		t.Fatalf("action = %v, want still cancel (no start)", m.action)
	}
	if cmd != nil {
		t.Fatal("n without a local config must not quit")
	}
	if !strings.Contains(m.statusText, "no local") {
		t.Fatalf("status should explain why: %q", m.statusText)
	}
}

func TestPickerQuitCancels(t *testing.T) {
	m := newPickerModel(pickerRows(), "")
	updated, cmd := m.Update(key("q"))
	m = updated.(*pickerModel)
	if m.action != pickerCancel {
		t.Fatalf("action = %v, want pickerCancel", m.action)
	}
	if cmd == nil {
		t.Fatal("q should quit the picker")
	}
}

// Selection must not run past the end of the list, with or without a start row.
func TestPickerSelectionStaysInRange(t *testing.T) {
	for _, local := range []string{"", "/srv/stacker/stacker.yml"} {
		m := newPickerModel(pickerRows(), local)
		for i := 0; i < 10; i++ {
			updated, _ := m.Update(key("down"))
			m = updated.(*pickerModel)
		}
		if m.selected >= len(m.entries()) {
			t.Fatalf("selected %d out of %d entries (local=%q)", m.selected, len(m.entries()), local)
		}
		for i := 0; i < 10; i++ {
			updated, _ := m.Update(key("up"))
			m = updated.(*pickerModel)
		}
		if m.selected != 0 {
			t.Fatalf("selected = %d after scrolling up, want 0", m.selected)
		}
	}
}

// A collision is the reason the user may want to stop one of them, so the key
// has to be discoverable right there.
func TestPickerFooterMentionsStopAndStart(t *testing.T) {
	m := newPickerModel(pickerRows(), "/srv/stacker/stacker.yml")
	m.width, m.height = 100, 30
	view := m.View()
	for _, want := range []string{"enter", "start here", "stop", "quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker footer missing %q:\n%s", want, view)
		}
	}
}

func TestPickerFooterOmitsStartWhenNoLocalConfig(t *testing.T) {
	m := newPickerModel(pickerRows(), "")
	m.width, m.height = 100, 30
	if strings.Contains(m.View(), "start here") {
		t.Fatalf("footer should not advertise start without a local config:\n%s", m.View())
	}
}
