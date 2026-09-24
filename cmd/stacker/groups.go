package main

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type groupActionMsg struct {
	group  string
	action string
	names  []string
	err    error
}

type groupSavedMsg struct {
	name  string
	group string
	err   error
}

func (m *model) groupChoices() []string {
	choices := make([]string, 0)
	for _, current := range m.sections() {
		if !current.Implicit {
			choices = append(choices, current.Name)
		}
	}
	return append(choices, "")
}

func (m *model) groupsView() string {
	process := m.current()
	if process == nil {
		return panelStyle.Render("No member selected")
	}

	choices := m.groupChoices()
	var b strings.Builder
	b.WriteString(titleStyle.Render("Group — " + sanitizeLogLine(process.Name)))
	b.WriteString("\n\n")
	for index, group := range choices[:len(choices)-1] {
		num := "  "
		if index < 9 {
			num = fmt.Sprintf("%d ", index+1)
		}
		if index == m.groupChoice {
			b.WriteString("> ")
		} else {
			b.WriteString("  ")
		}
		b.WriteString(num)
		b.WriteString(sanitizeLogLine(group))
		b.WriteByte('\n')
	}
	if m.groupChoice == len(choices)-1 {
		b.WriteString("> ")
	} else {
		b.WriteString("  ")
	}
	b.WriteString("0 none\n\n")
	b.WriteString(mutedStyle.Render("↑/k previous • ↓/j next • enter select • 1-9 shortcut • 0 none • esc close"))
	return panelStyle.Render(b.String())
}

func (m *model) openGroupPicker() {
	process := m.current()
	if process == nil {
		m.statusText = "Select a member to change its group"
		return
	}
	if process.orphaned {
		m.statusText = "Orphaned entries can't change groups"
		return
	}
	m.statusText = ""
	choices := m.groupChoices()
	m.groupChoice = len(choices) - 1
	for index, group := range choices {
		if group == process.Group() {
			m.groupChoice = index
			break
		}
	}
	m.showGroups = true
}

func (m *model) handleGroupKey(key string) tea.Cmd {
	choices := m.groupChoices()
	switch key {
	case "up", "k":
		if m.groupChoice > 0 {
			m.groupChoice--
		}
		return nil
	case "down", "j":
		if m.groupChoice+1 < len(choices) {
			m.groupChoice++
		}
		return nil
	case "esc":
		m.showGroups = false
		return nil
	}

	choice := -1
	switch key {
	case "enter":
		choice = m.groupChoice
	case "0":
		choice = len(choices) - 1
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			choice = int(key[0] - '1')
		}
	}
	if choice < 0 || choice >= len(choices) {
		m.showGroups = false
		return nil
	}
	m.showGroups = false
	return m.saveGroupChoice(choices[choice])
}

func (m *model) saveGroupChoice(group string) tea.Cmd {
	process := m.current()
	if process == nil {
		m.statusText = "Select a member to change its group"
		return nil
	}
	if process.orphaned {
		m.statusText = "Orphaned entries can't change groups"
		return nil
	}

	m.statusText = "Saving group for " + process.Name
	path, name := m.configPath, process.Name
	return func() tea.Msg {
		_, err := m.setConfiguredGroup(path, name, group)
		return groupSavedMsg{
			name:  name,
			group: group,
			err:   err,
		}
	}
}

func (m *model) updateGroupSavedStatus(msg groupSavedMsg) {
	if msg.err != nil {
		m.statusText = "Group save failed: " + msg.err.Error()
		return
	}
	destination := strings.TrimSpace(msg.group)
	if destination == "" {
		destination = "Other"
	}
	var foldStateErr error
	if m.collapsed[destination] {
		m.collapsed[destination] = false
		foldStateErr = m.persistCollapsedState()
	}
	m.restoreSelection(msg.name, false)
	status := ""
	if msg.group == "" {
		status = fmt.Sprintf("Group removed from %s (saved to YAML)", msg.name)
	} else {
		status = fmt.Sprintf("Group %s set on %s (saved to YAML)", msg.group, msg.name)
	}
	if foldStateErr != nil {
		m.statusText = fmt.Sprintf("%s; fold-state persistence failed: %v", status, foldStateErr)
		return
	}
	m.statusText = status
}

func (m *model) groupAction(group, action string) ([]string, error) {
	procs := m.procs()
	sections := buildSections(sectionMembers(procs))
	var current *section
	for index := range sections {
		if sections[index].Name == group {
			current = &sections[index]
			break
		}
	}
	if current == nil {
		return nil, fmt.Errorf("unknown group %q", group)
	}
	if action != "start" && action != "stop" && action != "restart" && action != "mark" {
		return nil, fmt.Errorf("unknown group action %q", action)
	}

	names := make([]string, 0, len(current.Services)+len(current.Tasks))
	var failures []error
	apply := func(member sectionMember, run func(*Process) error) {
		if member.Index < 0 || member.Index >= len(procs) {
			return
		}
		process := procs[member.Index]
		names = append(names, process.Name)
		if err := run(process); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", process.Name, err))
		}
	}

	switch action {
	case "start":
		for _, member := range current.Services {
			process := procs[member.Index]
			status := process.Status()
			if processIsActive(status) || status == StatusDisabled {
				continue
			}
			apply(member, func(process *Process) error { return process.Start(m.notify) })
		}
	case "stop":
		for _, member := range current.Services {
			process := procs[member.Index]
			if !processIsActive(process.Status()) {
				continue
			}
			apply(member, func(process *Process) error { return process.Stop(m.notify) })
		}
	case "restart":
		for _, member := range current.Services {
			process := procs[member.Index]
			if !processIsActive(process.Status()) {
				continue
			}
			apply(member, func(process *Process) error { return process.restart(m.notify) })
		}
	case "mark":
		for _, member := range current.Services {
			process := procs[member.Index]
			if !processIsActive(process.Status()) {
				continue
			}
			apply(member, func(process *Process) error {
				process.Mark()
				return nil
			})
		}
		for _, member := range current.Tasks {
			process := procs[member.Index]
			if !processIsActive(process.Status()) {
				continue
			}
			apply(member, func(process *Process) error {
				process.Mark()
				return nil
			})
		}
	}

	if len(names) > 0 {
		m.notify()
	}
	return names, errors.Join(failures...)
}

func processIsActive(status ProcessStatus) bool {
	switch status {
	case StatusRunning, StatusStarting, StatusStopping:
		return true
	default:
		return false
	}
}

func (m *model) groupActionCmd(group, action string) tea.Cmd {
	verbs := map[string]string{
		"start":   "Starting",
		"stop":    "Stopping",
		"restart": "Restarting",
		"mark":    "Marking",
	}
	m.statusText = verbs[action] + " " + group + "…"
	return func() tea.Msg {
		names, err := m.groupAction(group, action)
		return groupActionMsg{group: group, action: action, names: names, err: err}
	}
}

func (m *model) updateGroupActionStatus(msg groupActionMsg) {
	if msg.err != nil {
		m.statusText = fmt.Sprintf("%s %s failed (%d affected): %v", msg.group, msg.action, len(msg.names), msg.err)
		return
	}
	if len(msg.names) == 0 {
		noun := "services"
		if msg.action == "mark" {
			noun = "entries"
		}
		m.statusText = fmt.Sprintf("No %s to %s in %s", noun, msg.action, msg.group)
		return
	}
	verb := map[string]string{
		"start":   "Started",
		"stop":    "Stopped",
		"restart": "Restarted",
		"mark":    "Marked",
	}[msg.action]
	noun := "service"
	if msg.action == "mark" {
		noun = "entry"
	}
	if len(msg.names) != 1 {
		noun += "s"
	}
	m.statusText = fmt.Sprintf("%s %d %s in %s", verb, len(msg.names), noun, msg.group)
}
