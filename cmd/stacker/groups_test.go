package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGroupActionStartStartsServicesAndSkipsTasks(t *testing.T) {
	m := groupActionModel()
	for _, process := range m.processes {
		if !process.oneShot {
			process.Config.Command = "sleep 30"
		}
	}
	m.processByName("api").status = StatusFailed
	defer m.stopAll()

	names, err := m.groupAction("hub", "start")
	if err != nil {
		t.Fatalf("groupAction(start) error = %v", err)
	}
	if !reflect.DeepEqual(names, []string{"api", "worker"}) {
		t.Fatalf("groupAction(start) names = %#v, want both services", names)
	}
	for _, name := range []string{"api", "worker"} {
		if status := m.processByName(name).Status(); status != StatusRunning {
			t.Fatalf("groupAction(start) status for %s = %q, want running", name, status)
		}
	}
	for _, name := range []string{"migrate", "deploy"} {
		process := m.processByName(name)
		if process.Status() != StatusStopped || len(process.Logs()) != 0 {
			t.Fatalf("groupAction(start) changed task %s: status=%q logs=%#v", name, process.Status(), process.Logs())
		}
	}
}

func TestGroupActionStopOnlyStopsActiveServices(t *testing.T) {
	for _, status := range []ProcessStatus{StatusRunning, StatusStarting, StatusStopping} {
		t.Run(string(status), func(t *testing.T) {
			m := groupActionModel()
			m.processByName("api").status = status
			m.processByName("worker").status = StatusFailed
			m.processByName("migrate").status = StatusRunning

			names, err := m.groupAction("hub", "stop")
			if err != nil {
				t.Fatalf("groupAction(stop) error = %v", err)
			}
			if !reflect.DeepEqual(names, []string{"api"}) {
				t.Fatalf("groupAction(stop) names = %#v, want api only", names)
			}
			if got := m.processByName("api").Status(); got != StatusStopped {
				t.Fatalf("api status = %q, want stopped", got)
			}
			if got := m.processByName("worker").Status(); got != StatusFailed {
				t.Fatalf("worker status = %q, want failed", got)
			}
			if got := m.processByName("migrate").Status(); got != StatusRunning {
				t.Fatalf("migrate status = %q, want running", got)
			}
		})
	}
}

func TestGroupActionRestartOnlyRestartsActiveServices(t *testing.T) {
	m := groupActionModel()
	m.processByName("api").status = StatusStopping
	m.processByName("worker").status = StatusFailed
	m.processByName("migrate").status = StatusRunning

	names, err := m.groupAction("hub", "restart")
	if err != nil {
		t.Fatalf("groupAction(restart) error = %v", err)
	}
	if !reflect.DeepEqual(names, []string{"api"}) {
		t.Fatalf("groupAction(restart) names = %#v, want api only", names)
	}
	if got := m.processByName("worker").Status(); got != StatusFailed {
		t.Fatalf("worker status = %q, want failed", got)
	}
	if got := m.processByName("migrate").Status(); got != StatusRunning {
		t.Fatalf("migrate status = %q, want running", got)
	}
}

func TestGroupActionRestartFailureIsReturnedAndShown(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(cwd, []byte("file"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true", Cwd: cwd, Group: "hub"},
	}})
	m.processByName("api").status = StatusRunning
	m.selected = 0

	_, command := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if command == nil {
		t.Fatal("restart on a section header returned no command")
	}
	rawMessage := command()
	msg, ok := rawMessage.(groupActionMsg)
	if !ok {
		t.Fatalf("restart command message = %T, want groupActionMsg", rawMessage)
	}
	if !reflect.DeepEqual(msg.names, []string{"api"}) {
		t.Fatalf("restart command names = %#v, want api", msg.names)
	}
	if msg.err == nil || !strings.Contains(msg.err.Error(), "api") {
		t.Fatalf("restart command error = %v, want an error naming api", msg.err)
	}
	m.Update(msg)
	if !strings.Contains(strings.ToLower(m.statusText), "failed") || !strings.Contains(m.statusText, "api") {
		t.Fatalf("restart status = %q, want a failure naming api", m.statusText)
	}
}

func TestGroupActionMarkCoversActiveServicesAndTasks(t *testing.T) {
	m := groupActionModel()
	m.processByName("api").status = StatusRunning
	m.processByName("worker").status = StatusStopping
	m.processByName("migrate").status = StatusStarting

	names, err := m.groupAction("hub", "mark")
	if err != nil {
		t.Fatalf("groupAction(mark hub) error = %v", err)
	}
	if !reflect.DeepEqual(names, []string{"api", "worker", "migrate"}) {
		t.Fatalf("groupAction(mark hub) names = %#v, want active services and grouped task", names)
	}
	for _, name := range names {
		if len(m.processByName(name).Logs()) == 0 {
			t.Fatalf("groupAction(mark hub) did not mark %s", name)
		}
	}

	m.processByName("deploy").status = StatusRunning
	names, err = m.groupAction("Other", "mark")
	if err != nil {
		t.Fatalf("groupAction(mark Other) error = %v", err)
	}
	if !reflect.DeepEqual(names, []string{"deploy"}) {
		t.Fatalf("groupAction(mark Other) names = %#v, want ungrouped task", names)
	}
	if len(m.processByName("deploy").Logs()) == 0 {
		t.Fatal("groupAction(mark Other) did not mark the ungrouped task")
	}
	select {
	case <-m.refreshCh:
	default:
		t.Fatal("groupAction(mark) did not notify the UI")
	}
}

func TestGroupActionMarkSkipsStoppedAndFailedOneShotTasks(t *testing.T) {
	m := newModel(Config{
		Processes: map[string]ProcessConfig{
			"api": {Command: "true", Group: "hub"},
		},
		processOrder: []string{"api"},
		Tasks: map[string]TaskConfig{
			"migrate": {Command: "true", Group: "hub"},
			"seed":    {Command: "true", Group: "hub"},
		},
		taskOrder: []string{"migrate", "seed"},
	})
	m.processByName("migrate").status = StatusStopped
	m.processByName("seed").status = StatusFailed

	names, err := m.groupAction("hub", "mark")
	if err != nil {
		t.Fatalf("groupAction(mark) error = %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("groupAction(mark) names = %#v, want no inactive tasks", names)
	}
	for _, name := range []string{"migrate", "seed"} {
		if logs := m.processByName(name).Logs(); len(logs) != 0 {
			t.Errorf("groupAction(mark) marked inactive task %s: %#v", name, logs)
		}
	}
}

func TestGroupActionReportsStartFailuresWithAffectedNames(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(cwd, []byte("file"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	m := newModel(Config{Processes: map[string]ProcessConfig{
		"api": {Command: "true", Cwd: cwd, Group: "hub"},
	}})

	names, err := m.groupAction("hub", "start")
	if err == nil {
		t.Fatal("groupAction(start) error = nil, want start failure")
	}
	if !reflect.DeepEqual(names, []string{"api"}) {
		t.Fatalf("groupAction(start) names = %#v, want attempted api", names)
	}
	if !strings.Contains(err.Error(), "api") {
		t.Fatalf("groupAction(start) error = %q, want process name", err)
	}
}

func TestGroupActionRejectsUnknownGroupsAndActions(t *testing.T) {
	m := groupActionModel()
	for _, test := range []struct {
		group  string
		action string
	}{
		{group: "missing", action: "start"},
		{group: "hub", action: "launch"},
	} {
		if names, err := m.groupAction(test.group, test.action); err == nil || names != nil {
			t.Errorf("groupAction(%q, %q) = %#v, %v; want no names and an error", test.group, test.action, names, err)
		}
	}
}

func TestGroupPickerOpensForSelectedMemberAndClosesOnUnsupportedKey(t *testing.T) {
	m, _ := groupPickerModel(t)
	m.width, m.height = 80, 24
	m.selectByName("api")

	m.Update(groupPickerKey("g"))
	if !m.showGroups {
		t.Fatal("g did not open the group picker for a selected member")
	}
	if view := m.View(); !strings.Contains(view, "> 1 core") || !strings.Contains(view, "none") {
		t.Fatalf("View() did not render the group picker: %q", view)
	}

	m.Update(groupPickerKey("x"))
	if m.showGroups {
		t.Fatal("unsupported key did not close the group picker")
	}
	m.Update(groupPickerKey("g"))
	m.Update(groupPickerKey("esc"))
	if m.showGroups {
		t.Fatal("esc did not close the group picker")
	}
	if got := m.processByName("api").Group(); got != "core" {
		t.Fatalf("closing the picker changed api group to %q", got)
	}
}

func TestGroupPickerHighlightsCurrentGroupOrNone(t *testing.T) {
	m, _ := groupPickerModel(t)
	m.selectByName("api")
	m.Update(groupPickerKey("g"))
	if view := m.groupsView(); !strings.Contains(view, "> 1 core") {
		t.Fatalf("current group is not highlighted: %q", view)
	}

	m.Update(groupPickerKey("esc"))
	m.selectByName("loose")
	m.Update(groupPickerKey("g"))
	if view := m.groupsView(); !strings.Contains(view, "> 0 none") {
		t.Fatalf("none is not highlighted for an ungrouped member: %q", view)
	}
}

func TestGroupPickerNavigationSupportsViKeys(t *testing.T) {
	m, _ := groupPickerModel(t)
	m.selectByName("api")
	m.Update(groupPickerKey("g"))
	m.Update(groupPickerKey("j"))
	if view := m.groupsView(); !strings.Contains(view, "> 2 jobs") {
		t.Fatalf("j did not move to the next group: %q", view)
	}
	m.Update(groupPickerKey("k"))
	if view := m.groupsView(); !strings.Contains(view, "> 1 core") {
		t.Fatalf("k did not move to the previous group: %q", view)
	}
}

func TestGroupPickerListsExplicitGroupsInDisplayOrder(t *testing.T) {
	m, _ := groupPickerModel(t)
	want := []string{"core", "jobs", "release", ""}
	if got := m.groupChoices(); !reflect.DeepEqual(got, want) {
		t.Fatalf("groupChoices() = %#v, want %#v", got, want)
	}

	view := m.groupsView()
	lastIndex := -1
	for _, choice := range []string{"core", "jobs", "release", "none"} {
		index := strings.Index(view, choice)
		if index <= lastIndex {
			t.Fatalf("groups view does not list %q after the previous choice: %q", choice, view)
		}
		lastIndex = index
	}
	for _, shortcut := range []string{"1 core", "2 jobs", "3 release", "0 none"} {
		if !strings.Contains(view, shortcut) {
			t.Errorf("groups view is missing shortcut %q: %q", shortcut, view)
		}
	}
	if strings.Contains(view, "loose") {
		t.Fatalf("groups view included an implicit section member: %q", view)
	}
}

func TestGroupPickerIncludesExplicitOtherAndExcludesImplicitOther(t *testing.T) {
	m := newModel(Config{
		Processes: map[string]ProcessConfig{
			"api":      {Command: "true", Group: "core"},
			"loose":    {Command: "true"},
			"explicit": {Command: "true", Group: "Other"},
		},
		processOrder: []string{"api", "loose", "explicit"},
		Tasks: map[string]TaskConfig{
			"deploy": {Command: "true", Group: "release"},
		},
		taskOrder: []string{"deploy"},
	})

	want := []string{"core", "release", "Other", ""}
	if got := m.groupChoices(); !reflect.DeepEqual(got, want) {
		t.Fatalf("groupChoices() = %#v, want %#v", got, want)
	}
}

func TestGroupPickerReservesZeroForNoneAfterNineExplicitGroups(t *testing.T) {
	processes := make(map[string]ProcessConfig, 10)
	order := make([]string, 0, 10)
	for index := 1; index <= 10; index++ {
		name := fmt.Sprintf("service-%d", index)
		processes[name] = ProcessConfig{Command: "true", Group: fmt.Sprintf("group-%d", index)}
		order = append(order, name)
	}
	m := newModel(Config{Processes: processes, processOrder: order})
	view := m.groupsView()
	if !strings.Contains(view, "9 group-9") || !strings.Contains(view, "  group-10") || !strings.Contains(view, "0 none") {
		t.Fatalf("groups view shortcuts = %q, want 1-9, unnumbered tenth group, and 0 none", view)
	}
}

func TestGroupPickerNavigatesToTenthGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stacker.yml")
	var config strings.Builder
	config.WriteString("version: 1\nprocesses:\n")
	for index := 1; index <= 10; index++ {
		fmt.Fprintf(&config, "  service-%d:\n    command: true\n    group: group-%d\n", index, index)
	}
	if err := os.WriteFile(path, []byte(config.String()), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	m := newModel(cfg)
	m.configPath = path
	m.selectByName("service-1")
	m.Update(groupPickerKey("g"))
	for range 9 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if view := m.groupsView(); !strings.Contains(view, ">   group-10") {
		t.Fatalf("tenth group is not highlighted after navigation: %q", view)
	}
	_, command := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil {
		t.Fatal("enter on the highlighted tenth group returned no save command")
	}
	m.Update(command())

	if got := m.processByName("service-1").Group(); got != "group-10" {
		t.Fatalf("service-1 group = %q, want group-10", got)
	}
	cfg, err = loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() after group selection: %v", err)
	}
	if got := cfg.Processes["service-1"].Group; got != "group-10" {
		t.Fatalf("persisted service-1 group = %q, want group-10", got)
	}
}

func TestGroupPickerSaveUpdatesConfiguredStateBeforeStatusHandling(t *testing.T) {
	m, path := groupPickerModel(t)
	m.selectByName("api")
	m.Update(groupPickerKey("g"))
	_, command := m.Update(groupPickerKey("2"))
	if command == nil {
		t.Fatal("selecting a group returned no save command")
	}
	result := command()
	msg, ok := result.(groupSavedMsg)
	if !ok {
		t.Fatalf("group save message = %T, want groupSavedMsg", result)
	}
	if msg.err != nil {
		t.Fatalf("group save error = %v", msg.err)
	}
	if got := m.cfg.Processes["api"].Group; got != "jobs" {
		t.Fatalf("configured api group before status handling = %q, want jobs", got)
	}
	if got := m.processByName("api").Group(); got != "jobs" {
		t.Fatalf("live api group before status handling = %q, want jobs", got)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() after group save: %v", err)
	}
	if got := cfg.Processes["api"].Group; got != "jobs" {
		t.Fatalf("persisted api group = %q, want jobs", got)
	}
}

func TestConcurrentGroupMutationsKeepPickerConfigAndProcessSynchronized(t *testing.T) {
	m, path := groupPickerModel(t)
	for range 32 {
		m.selectByName("api")
		m.Update(groupPickerKey("g"))
		_, pickerSave := m.Update(groupPickerKey("2"))
		if pickerSave == nil {
			t.Fatal("selecting a group returned no save command")
		}

		start := make(chan struct{})
		var wait sync.WaitGroup
		mutationErrors := make(chan error, 1)
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			m.Update(pickerSave())
		}()
		go func() {
			defer wait.Done()
			<-start
			_, err := m.setConfiguredGroup(path, "api", "release")
			mutationErrors <- err
		}()
		close(start)
		wait.Wait()
		if err := <-mutationErrors; err != nil {
			t.Fatalf("concurrent configured group mutation error = %v", err)
		}

		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatalf("loadConfig() after concurrent mutations: %v", err)
		}
		persisted := cfg.Processes["api"].Group
		if configured := m.cfg.Processes["api"].Group; configured != persisted {
			t.Fatalf("configured api group = %q, persisted group = %q", configured, persisted)
		}
		if live := m.processByName("api").Group(); live != persisted {
			t.Fatalf("live api group = %q, persisted group = %q", live, persisted)
		}
	}
}

func TestGroupPickerMovesProcessAndRestoresSelectionByName(t *testing.T) {
	m, path := groupPickerModel(t)
	m.selectByName("api")
	m.Update(groupPickerKey("g"))
	_, command := m.Update(groupPickerKey("2"))
	if command == nil {
		t.Fatal("selecting a group returned no save command")
	}
	m.Update(command())

	if got := m.current().Name; got != "api" {
		t.Fatalf("current process after moving api = %q, want api", got)
	}
	rows := m.rows()
	if rows[m.selected].Member == nil || rows[m.selected].Member.Name != "api" {
		t.Fatalf("selected row after moving api = %#v, want api member", rows[m.selected])
	}
	if got := m.processByName("api").Group(); got != "jobs" {
		t.Fatalf("api group = %q, want jobs", got)
	}
	if section := m.selectedOrEnclosingSection(); section == nil || section.Name != "jobs" {
		t.Fatalf("selected section = %#v, want jobs", section)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() after group selection: %v", err)
	}
	if got := cfg.Processes["api"].Group; got != "jobs" {
		t.Fatalf("persisted api group = %q, want jobs", got)
	}
}

func TestGroupPickerExpandsCollapsedDestinationAndPreservesSelection(t *testing.T) {
	for _, test := range []struct {
		name        string
		choice      string
		destination string
	}{
		{name: "named group", choice: "2", destination: "jobs"},
		{name: "none uses Other", choice: "0", destination: "Other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, _ := groupPickerModel(t)
			m.collapsed[test.destination] = true
			m.collapsedPath = filepath.Join(t.TempDir(), "ui-state.json")
			m.persistCollapsedState()

			m.selectByName("api")
			m.Update(groupPickerKey("g"))
			_, command := m.Update(groupPickerKey(test.choice))
			if command == nil {
				t.Fatal("selecting a destination returned no save command")
			}
			m.Update(command())

			if current := m.current(); current == nil || current.Name != "api" {
				t.Fatalf("current process after move = %#v, want api", current)
			}
			if m.collapsed[test.destination] {
				t.Fatalf("destination %q remains collapsed", test.destination)
			}
			collapsed, err := loadCollapsed(m.collapsedPath)
			if err != nil {
				t.Fatalf("load collapsed state: %v", err)
			}
			if collapsed[test.destination] {
				t.Fatalf("persisted destination %q remains collapsed", test.destination)
			}
		})
	}
}

func TestGroupPickerReportsFoldPersistenceFailureAfterSavingGroup(t *testing.T) {
	m, configPath := groupPickerModel(t)
	m.collapsed["jobs"] = true
	m.collapsedPath = t.TempDir()
	m.selectByName("api")
	m.Update(groupPickerKey("g"))
	_, command := m.Update(groupPickerKey("2"))
	if command == nil {
		t.Fatal("selecting a group returned no save command")
	}
	m.Update(command())

	if !strings.Contains(m.statusText, "saved to YAML") || !strings.Contains(m.statusText, "fold-state persistence failed") {
		t.Fatalf("status = %q, want group-save success and fold-state persistence warning", m.statusText)
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig() after group selection: %v", err)
	}
	if got := cfg.Processes["api"].Group; got != "jobs" {
		t.Fatalf("persisted api group = %q, want jobs", got)
	}
}

func TestGroupPickerClearsProcessGroupToNone(t *testing.T) {
	m, path := groupPickerModel(t)
	m.selectByName("api")
	m.Update(groupPickerKey("g"))
	_, command := m.Update(groupPickerKey("0"))
	if command == nil {
		t.Fatal("selecting none returned no save command")
	}
	m.Update(command())

	if got := m.processByName("api").Group(); got != "" {
		t.Fatalf("api group = %q, want empty", got)
	}
	if got := m.current().Name; got != "api" {
		t.Fatalf("current process after clearing group = %q, want api", got)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() after clearing group: %v", err)
	}
	if got := cfg.Processes["api"].Group; got != "" {
		t.Fatalf("persisted api group = %q, want empty", got)
	}
}

func TestGroupPickerUsesTasksMappingForOneShotMember(t *testing.T) {
	m, path := groupPickerModel(t)
	m.selectByName("deploy")
	m.Update(groupPickerKey("g"))
	_, command := m.Update(groupPickerKey("1"))
	if command == nil {
		t.Fatal("selecting a group for a task returned no save command")
	}
	m.Update(command())

	if got := m.processByName("deploy").Group(); got != "core" {
		t.Fatalf("deploy group = %q, want core", got)
	}
	if got := m.current().Name; got != "deploy" {
		t.Fatalf("current process after grouping task = %q, want deploy", got)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() after task group selection: %v", err)
	}
	if got := cfg.Tasks["deploy"].Group; got != "core" {
		t.Fatalf("persisted deploy group = %q, want core", got)
	}
	if got := cfg.Processes["api"].Group; got != "core" {
		t.Fatalf("persisted api group = %q, want core", got)
	}
}

func TestGroupPickerRefusesHeadersMissingSelectionAndOrphans(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		m, _ := groupPickerModel(t)
		m.selected = 0
		m.Update(groupPickerKey("g"))
		if m.showGroups || m.statusText == "" {
			t.Fatalf("header opened picker or had no refusal status: showGroups=%v status=%q", m.showGroups, m.statusText)
		}
	})

	t.Run("missing selection", func(t *testing.T) {
		m, _ := groupPickerModel(t)
		m.selected = -1
		m.Update(groupPickerKey("g"))
		if m.showGroups || m.statusText == "" {
			t.Fatalf("missing selection opened picker or had no refusal status: showGroups=%v status=%q", m.showGroups, m.statusText)
		}
	})

	t.Run("orphan", func(t *testing.T) {
		m, _ := groupPickerModel(t)
		m.selectByName("api")
		process := m.current()
		process.orphaned = true
		m.Update(groupPickerKey("g"))
		if m.showGroups {
			t.Fatal("orphaned member opened the group picker")
		}
		if m.statusText != "Orphaned entries can't change groups" {
			t.Fatalf("orphan refusal status = %q", m.statusText)
		}
		if got := process.Group(); got != "core" {
			t.Fatalf("orphan group changed to %q", got)
		}
	})
}

func groupPickerModel(t *testing.T) (*model, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stacker.yml")
	data := []byte(`version: 1
processes:
  api:
    command: "true"
    group: core
  worker:
    command: "true"
    group: jobs
  loose:
    command: "true"
tasks:
  deploy:
    command: "true"
    group: release
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	m := newModel(cfg)
	m.configPath = path
	return m, path
}

func groupPickerKey(key string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

func TestGroupActionKeysOperateOnHeaderAndPreserveMemberBehavior(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyMsg
		text string
	}{
		{name: "enter starts", key: tea.KeyMsg{Type: tea.KeyEnter}, text: "Starting hub…"},
		{name: "s stops", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}, text: "Stopping hub…"},
		{name: "r restarts", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}, text: "Restarting hub…"},
		{name: "space marks", key: tea.KeyMsg{Type: tea.KeySpace}, text: "Marking hub…"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := groupActionModel()
			m.selected = 0
			_, cmd := m.Update(test.key)
			if cmd == nil {
				t.Fatal("section header key returned no group action")
			}
			if m.statusText != test.text {
				t.Fatalf("status text = %q, want %q", m.statusText, test.text)
			}
		})
	}

	m := groupActionModel()
	m.selected = 1
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter on a service member changed its existing command behavior")
	}
}

func groupActionModel() *model {
	return newModel(Config{
		Processes: map[string]ProcessConfig{
			"api":    {Command: "true", Group: "hub"},
			"worker": {Command: "true", Group: "hub"},
		},
		processOrder: []string{"api", "worker"},
		Tasks: map[string]TaskConfig{
			"migrate": {Command: "true", Group: "hub"},
			"deploy":  {Command: "true"},
		},
		taskOrder: []string{"migrate", "deploy"},
	})
}
