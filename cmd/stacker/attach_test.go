package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// An attached TUI is the only interface to a headless `serve` supervisor, so `w`
// has to reach the web viewer through the control plane. It used to be an
// unhandled key: nothing started, and no URL or port was ever shown.
func TestAttachWebKeyTogglesViewerAndReportsURL(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, `
version: 1
processes:
  demo:
    command: true
`)
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	m := newModel(cfg)
	m.mode = "serve"
	cs, err := startControlServer(m, cfgPath)
	if err != nil {
		t.Fatalf("startControlServer: %v", err)
	}
	defer cs.Close()
	defer m.stopWeb()

	client, _, err := newControlClient(cfgPath)
	if err != nil {
		t.Fatalf("newControlClient: %v", err)
	}
	am := newAttachModel(client, cfgPath)
	am.procs = m.processInfos()

	_, cmd := am.Update(webKeyMsg())
	if cmd == nil {
		t.Fatal("`w` produced no command: an attached TUI cannot open the web viewer")
	}
	web, ok := cmd().(attachWebMsg)
	if !ok {
		t.Fatalf("unexpected message type %T", cmd())
	}
	if web.err != nil || !web.enabled || web.addr == "" {
		t.Fatalf("web toggle: %+v", web)
	}
	if m.webAddr() == "" {
		t.Fatal("supervisor did not start the web viewer")
	}

	// The status line must carry a pasteable URL for the selected process.
	updated, _ := am.Update(web)
	am, ok = updated.(*attachModel)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
	if !strings.Contains(am.statusText, "http://") || !strings.Contains(am.statusText, "/logs/demo") {
		t.Fatalf("status line without a usable URL: %q", am.statusText)
	}

	// Pressing it again shuts the viewer down.
	_, cmd = am.Update(webKeyMsg())
	if cmd == nil {
		t.Fatal("second `w` produced no command")
	}
	off, ok := cmd().(attachWebMsg)
	if !ok {
		t.Fatalf("unexpected message type %T", cmd())
	}
	if off.err != nil || off.enabled {
		t.Fatalf("second toggle should disable the viewer: %+v", off)
	}
	if m.webAddr() != "" {
		t.Fatal("supervisor still serving the web viewer")
	}
}

// The help overlay is the only place these keys are discoverable.
func TestAttachHelpListsWebKey(t *testing.T) {
	am := newAttachModel(nil, "stacker.yml")
	if !strings.Contains(am.helpView(), "web logs") {
		t.Fatalf("attach help does not mention the web viewer:\n%s", am.helpView())
	}
}

// The footer advertises the primary actions without opening help. The session
// TUI shows `w web` there, so an attached TUI must match or the key looks absent.
func TestAttachFooterAdvertisesWebKey(t *testing.T) {
	am := newAttachModel(nil, "stacker.yml")
	if !strings.Contains(am.footerView(), "web") {
		t.Fatalf("attach footer does not advertise the web key: %q", am.footerView())
	}
}

func TestAttachSectionsRenderSharedHeadersAndRows(t *testing.T) {
	am := newAttachModel(nil, "stacker.yml")
	am.width = 100
	am.procs = []ProcessInfo{
		{Name: "hub-migrate", Group: "hub", OneShot: true, Status: string(StatusStopped)},
		{Name: "loose", Status: string(StatusStopped)},
		{Name: "api", Group: "hub", Status: string(StatusRunning)},
		{Name: "worker", Group: "jobs", Status: string(StatusFailed), Errors: 1},
	}

	rows := am.rows()
	var got []string
	for _, row := range rows {
		if row.Header != nil {
			got = append(got, "header:"+row.Header.Name)
		} else if row.Member != nil {
			got = append(got, row.Member.Name)
		}
	}
	want := []string{"header:hub", "api", "hub-migrate", "header:jobs", "worker", "header:Other", "loose"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attach rows = %v, want %v", got, want)
	}

	sections := am.sections()
	var hub *section
	for index := range sections {
		if sections[index].Name == "hub" {
			hub = &sections[index]
		}
	}
	if hub == nil {
		t.Fatal("missing hub section")
	}
	wantHeader := formatSectionHeader("hub", summarizeSection([]memberState{
		{Status: StatusRunning},
		{Status: StatusStopped, OneShot: true},
	}), false, false, max(1, am.leftWidth()-5))
	if !strings.Contains(am.processList(), wantHeader) {
		t.Fatalf("attach process list does not use the shared section header %q:\n%s", wantHeader, am.processList())
	}
	line := ansi.Strip(strings.Split(am.processList(), "\n")[1])
	if !strings.Contains(line, "− hub") || !strings.Contains(line, "─") {
		t.Fatalf("attach section header does not use the expanded divider treatment: %q", line)
	}
	am.collapsed["hub"] = true
	line = ansi.Strip(strings.Split(am.processList(), "\n")[1])
	if !strings.Contains(line, "+ hub") || !strings.Contains(line, "─") {
		t.Fatalf("attach section header does not use the collapsed divider treatment: %q", line)
	}
}

func TestAttachSectionHeaderLogViewShowsSummary(t *testing.T) {
	am := newAttachModel(nil, "stacker.yml")
	am.width = 100
	am.height = 20
	am.procs = []ProcessInfo{
		{Name: "api", Group: "hub", Status: string(StatusRunning)},
		{Name: "worker", Group: "hub", Status: string(StatusStopped)},
		{Name: "migrate", Group: "hub", OneShot: true, Status: string(StatusStopped)},
	}
	if !am.selectAttachHeader("hub") {
		t.Fatal("could not select hub header")
	}

	if got := am.currentName(); got != "" {
		t.Fatalf("currentName() for a header = %q, want empty", got)
	}
	if got := am.current(); got != nil {
		t.Fatalf("current() for a header = %#v, want nil", got)
	}
	if got := am.pollLogs(); got != nil {
		t.Fatalf("pollLogs() for a header = %v, want nil", got)
	}
	if got := am.logView(); !strings.Contains(got, "1/2 services running · 1 task") {
		t.Fatalf("section summary missing from log panel:\n%s", got)
	}
}

func TestAttachSnapshotRestoresMemberAndHeaderSelection(t *testing.T) {
	am := newAttachModel(nil, "stacker.yml")
	first := []ProcessInfo{
		{Name: "migrate", Group: "hub", OneShot: true, Status: string(StatusStopped)},
		{Name: "worker", Group: "jobs", Status: string(StatusRunning)},
		{Name: "api", Group: "hub", Status: string(StatusRunning)},
	}
	am.Update(attachSnapMsg{procs: first})
	if current := am.current(); current == nil || current.Name != "api" {
		t.Fatalf("first snapshot selection = %#v, want first service api", current)
	}
	if !am.selectAttachMember("worker") {
		t.Fatal("could not select worker")
	}

	second := []ProcessInfo{
		{Name: "api", Group: "hub", Status: string(StatusRunning)},
		{Name: "worker", Group: "jobs", Status: string(StatusRunning)},
		{Name: "migrate", Group: "hub", OneShot: true, Status: string(StatusStopped)},
	}
	am.Update(attachSnapMsg{procs: second})
	if got := am.currentName(); got != "worker" {
		t.Fatalf("member selection after snapshot = %q, want worker", got)
	}

	if !am.selectAttachHeader("hub") {
		t.Fatal("could not select hub header")
	}
	am.Update(attachSnapMsg{procs: first})
	if got := am.selectedHeaderName(); got != "hub" {
		t.Fatalf("header selection after snapshot = %q, want hub", got)
	}

	hidden := newAttachModel(nil, "stacker.yml")
	hidden.collapsed["hub"] = true
	hidden.Update(attachSnapMsg{procs: []ProcessInfo{
		{Name: "api", Group: "core", Status: string(StatusRunning)},
		{Name: "worker", Group: "jobs", Status: string(StatusRunning)},
	}})
	if got := hidden.currentName(); got != "api" {
		t.Fatalf("selection before entering persisted collapsed section = %q, want api", got)
	}
	hidden.Update(attachSnapMsg{procs: []ProcessInfo{
		{Name: "api", Group: "hub", Status: string(StatusRunning)},
		{Name: "worker", Group: "jobs", Status: string(StatusRunning)},
	}})
	if got := hidden.selectedHeaderName(); got != "hub" {
		t.Fatalf("selection hidden by persisted collapsed section = %q, want hub header", got)
	}
}

func TestAttachFoldStatePersistsToPerConfigStateFile(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	configPath := writeConfig(t, t.TempDir(), `
version: 1
processes:
  api:
    command: true
    group: hub
  worker:
    command: true
    group: jobs
`)
	statePath, err := uiStatePath(configPath)
	if err != nil {
		t.Fatalf("uiStatePath: %v", err)
	}
	if err := saveCollapsed(statePath, map[string]bool{"hub": true}, []section{{Name: "hub"}, {Name: "jobs"}}); err != nil {
		t.Fatalf("save initial collapsed state: %v", err)
	}
	am := newAttachModel(nil, configPath)
	if am.collapsedPath != statePath {
		t.Fatalf("collapsedPath = %q, want %q", am.collapsedPath, statePath)
	}
	if !am.collapsed["hub"] {
		t.Fatalf("collapsed state loaded by attach = %#v, want hub folded", am.collapsed)
	}
	am.procs = []ProcessInfo{
		{Name: "api", Group: "hub", Status: string(StatusRunning)},
		{Name: "worker", Group: "jobs", Status: string(StatusStopped)},
	}
	am.Update(attachSnapMsg{procs: am.procs})
	if got := am.selectedHeaderName(); got != "hub" {
		t.Fatalf("initial selection with persisted fold = %q, want hub header", got)
	}
	am.Update(attachKeyMsg("right"))
	collapsed, err := loadCollapsed(statePath)
	if err != nil {
		t.Fatalf("load collapsed state after unfold: %v", err)
	}
	if collapsed["hub"] {
		t.Fatalf("collapsed state after unfold = %#v, want hub expanded", collapsed)
	}
	if !am.selectAttachMember("api") {
		t.Fatal("could not select api")
	}
	am.Update(attachKeyMsg("h"))
	collapsed, err = loadCollapsed(statePath)
	if err != nil {
		t.Fatalf("load collapsed state after fold: %v", err)
	}
	if !collapsed["hub"] {
		t.Fatalf("collapsed state after fold = %#v, want hub folded", collapsed)
	}
	am.Update(attachKeyMsg("l"))
	collapsed, err = loadCollapsed(statePath)
	if err != nil {
		t.Fatalf("load collapsed state after alias unfold: %v", err)
	}
	if collapsed["hub"] {
		t.Fatalf("collapsed state after alias unfold = %#v, want hub expanded", collapsed)
	}
}

func TestAttachSectionFoldSelectionSurvivesStateSaveFailure(t *testing.T) {
	am := newAttachModel(nil, "stacker.yml")
	am.procs = []ProcessInfo{
		{Name: "api", Group: "hub", Status: string(StatusRunning)},
		{Name: "worker", Group: "jobs", Status: string(StatusStopped)},
	}
	am.collapsedPath = t.TempDir()
	if !am.selectAttachMember("api") {
		t.Fatal("could not select api")
	}

	am.Update(attachKeyMsg("left"))

	if got := am.selectedHeaderName(); got != "hub" {
		t.Fatalf("selection after fold-state save failure = %q, want hub header", got)
	}
	if !strings.Contains(am.statusText, "Collapsed state save failed") {
		t.Fatalf("fold-state save failure missing from status: %q", am.statusText)
	}
}

func TestAttachMouseSelectsMembersAndFoldsSections(t *testing.T) {
	am := newAttachModel(nil, "stacker.yml")
	am.width = 100
	am.height = 20
	am.collapsedPath = t.TempDir() + "/state.json"
	am.procs = []ProcessInfo{
		{Name: "api", Group: "hub", Status: string(StatusRunning)},
		{Name: "worker", Group: "jobs", Status: string(StatusStopped)},
	}

	am.Update(tea.MouseMsg{X: 1, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if got := am.currentName(); got != "api" {
		t.Fatalf("mouse-selected member = %q, want api", got)
	}
	am.Update(tea.MouseMsg{X: 1, Y: 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if got := am.selectedHeaderName(); got != "hub" {
		t.Fatalf("mouse-selected section = %q, want hub", got)
	}
	collapsed, err := loadCollapsed(am.collapsedPath)
	if err != nil {
		t.Fatalf("load collapsed state after mouse fold: %v", err)
	}
	if !collapsed["hub"] {
		t.Fatalf("collapsed state after mouse fold = %#v, want hub folded", collapsed)
	}
}

func TestAttachGroupHeaderActionsPostOnceToControlServer(t *testing.T) {
	m, cs, configPath := startControlTest(t, `
version: 1
processes:
  api:
    command: true
    cwd: ./missing-api
    group: hub
  loose:
    command: true
    cwd: ./missing-loose
`)
	recorder := &attachRecordingTransport{base: http.DefaultTransport}
	client := &controlClient{
		addr:   cs.listener.Addr().String(),
		client: &http.Client{Transport: recorder},
	}
	am := newAttachModel(client, configPath)
	am.procs = m.processInfos()

	for _, test := range []struct {
		key    string
		action string
	}{
		{key: "enter", action: "start"},
		{key: "s", action: "stop"},
		{key: "r", action: "restart"},
		{key: " ", action: "mark"},
	} {
		if !am.selectAttachHeader("hub") {
			t.Fatalf("could not select hub header for %s", test.action)
		}
		previous := len(recorder.requests)
		_, command := am.Update(attachKeyMsg(test.key))
		if command == nil {
			t.Fatalf("group header %s returned no command", test.action)
		}
		if message := command(); message == nil {
			t.Fatalf("group header %s returned no response", test.action)
		}
		requests := recorder.requests[previous:]
		if len(requests) != 1 {
			t.Fatalf("group header %s sent %d requests, want 1", test.action, len(requests))
		}
		if requests[0].method != http.MethodPost || requests[0].path != "/v1/groups/"+test.action {
			t.Fatalf("group header %s request = %s %s, want POST /v1/groups/%s", test.action, requests[0].method, requests[0].path, test.action)
		}
		var body map[string]string
		if err := json.Unmarshal([]byte(requests[0].body), &body); err != nil {
			t.Fatalf("decode %s request body: %v", test.action, err)
		}
		if !reflect.DeepEqual(body, map[string]string{"group": "hub"}) {
			t.Fatalf("group header %s body = %#v, want hub", test.action, body)
		}
	}

	if !am.selectAttachHeader("Other") {
		t.Fatal("could not select Other header")
	}
	previous := len(recorder.requests)
	_, command := am.Update(attachKeyMsg(" "))
	if command == nil {
		t.Fatal("Other header mark returned no command")
	}
	command()
	requests := recorder.requests[previous:]
	if len(requests) != 1 || requests[0].method != http.MethodPost || requests[0].path != "/v1/groups/mark" {
		t.Fatalf("Other header request = %#v, want one POST /v1/groups/mark", requests)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(requests[0].body), &body); err != nil {
		t.Fatalf("decode Other request body: %v", err)
	}
	if !reflect.DeepEqual(body, map[string]string{"group": ""}) {
		t.Fatalf("Other header body = %#v, want empty effective group", body)
	}
}

func TestAttachPollLogsUsesSelectedMemberName(t *testing.T) {
	m, cs, configPath := startControlTest(t, `
version: 1
processes:
  loose:
    command: true
  api:
    command: true
    group: hub
  worker:
    command: true
    group: jobs
`)
	client := &controlClient{addr: cs.listener.Addr().String(), client: http.DefaultClient}
	am := newAttachModel(client, configPath)
	am.procs = m.processInfos()
	if !am.selectAttachMember("worker") {
		t.Fatal("could not select worker")
	}
	command := am.pollLogs()
	if command == nil {
		t.Fatal("selected member log poll returned no command")
	}
	result := command()
	message, ok := result.(attachLogsMsg)
	if !ok {
		t.Fatalf("pollLogs() returned %T, want attachLogsMsg", result)
	}
	if message.err != nil || message.name != "worker" {
		t.Fatalf("pollLogs() for selected member = %+v, want worker logs", message)
	}
}

func TestAttachSnapshotRejectsOlderResponses(t *testing.T) {
	am := newAttachModel(nil, "stacker.yml")
	olderRequest := am.pollSnap()
	newerRequest := am.pollSnap()
	if olderRequest == nil || newerRequest == nil {
		t.Fatal("snapshot poll returned no command")
	}
	if am.snapshotSeq != 2 {
		t.Fatalf("snapshot request sequence = %d, want 2", am.snapshotSeq)
	}

	am.Update(attachSnapMsg{seq: 1, procs: []ProcessInfo{{Name: "old"}}})
	if len(am.procs) != 0 {
		t.Fatalf("older snapshot was applied before latest response: %#v", am.procs)
	}
	am.Update(attachSnapMsg{seq: 2, procs: []ProcessInfo{{Name: "new"}}})
	am.Update(attachSnapMsg{seq: 1, err: errors.New("stale snapshot error")})
	if got := am.currentName(); got != "new" {
		t.Fatalf("selection after stale snapshot = %q, want new", got)
	}
	if am.pollErr != "" {
		t.Fatalf("stale snapshot error was recorded: %q", am.pollErr)
	}
}

func TestAttachLogPollsRejectDuplicateAndOutOfOrderResponses(t *testing.T) {
	am := newAttachModel(nil, "stacker.yml")
	am.procs = []ProcessInfo{{Name: "api", Status: string(StatusRunning)}}
	if !am.selectAttachMember("api") {
		t.Fatal("could not select api")
	}
	firstCommand := am.pollLogs()
	if firstCommand == nil {
		t.Fatal("first log poll returned no command")
	}
	if secondCommand := am.pollLogs(); secondCommand != nil {
		t.Fatal("overlapping log poll returned a command")
	}
	firstResponse := attachLogsMsg{
		seq:        am.logInFlightSeq,
		generation: am.selectionGeneration,
		name:       "api",
		lines:      []string{"first"},
		next:       5,
	}
	am.Update(firstResponse)
	am.Update(firstResponse)
	if !reflect.DeepEqual(am.logs, []string{"first"}) || am.logNext != 5 {
		t.Fatalf("duplicate response state = logs %v cursor %d, want [first] cursor 5", am.logs, am.logNext)
	}

	secondCommand := am.pollLogs()
	if secondCommand == nil {
		t.Fatal("second log poll returned no command after first completed")
	}
	secondResponse := attachLogsMsg{
		seq:        am.logInFlightSeq,
		generation: am.selectionGeneration,
		name:       "api",
		lines:      []string{"second"},
		next:       6,
	}
	am.Update(secondResponse)
	am.Update(attachLogsMsg{
		seq:        firstResponse.seq,
		generation: firstResponse.generation,
		name:       "api",
		lines:      []string{"stale"},
		next:       4,
	})
	if !reflect.DeepEqual(am.logs, []string{"first", "second"}) || am.logNext != 6 {
		t.Fatalf("out-of-order response state = logs %v cursor %d, want [first second] cursor 6", am.logs, am.logNext)
	}
}

func TestAttachLogPollRejectsStaleErrorAfterReturningToMember(t *testing.T) {
	am := newAttachModel(nil, "stacker.yml")
	am.procs = []ProcessInfo{
		{Name: "api", Group: "hub", Status: string(StatusRunning)},
		{Name: "worker", Group: "jobs", Status: string(StatusRunning)},
	}
	am.Update(attachSnapMsg{procs: am.procs})
	if got := am.currentName(); got != "api" {
		t.Fatalf("initial selected member = %q, want api", got)
	}
	command := am.pollLogs()
	if command == nil {
		t.Fatal("log poll returned no command")
	}
	request := am.logInFlightSeq
	generation := am.selectionGeneration
	am.Update(attachKeyMsg("down"))
	am.Update(attachKeyMsg("up"))
	if got := am.currentName(); got != "api" || am.selectionGeneration == generation {
		t.Fatalf("member selection after switching away and back = %q generation %d, want api with a new generation", got, am.selectionGeneration)
	}
	am.pollErr = "current error"
	am.Update(attachLogsMsg{
		seq:        request,
		generation: generation,
		name:       "api",
		err:        errors.New("stale tail error"),
	})
	if am.pollErr != "current error" {
		t.Fatalf("stale log error replaced current error: %q", am.pollErr)
	}
	if am.logInFlight {
		t.Fatal("stale response did not clear its matching in-flight request")
	}
}

func TestAttachFoldPersistenceReportsCachePathResolutionFailure(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	am := newAttachModel(nil, "stacker.yml")
	am.procs = []ProcessInfo{
		{Name: "api", Group: "hub", Status: string(StatusRunning)},
		{Name: "worker", Group: "jobs", Status: string(StatusRunning)},
	}
	if !am.selectAttachMember("api") {
		t.Fatal("could not select api")
	}
	am.Update(attachKeyMsg("left"))
	if !strings.Contains(am.statusText, "Collapsed state save failed") {
		t.Fatalf("cache path error missing from fold status: %q", am.statusText)
	}
	err := am.persistCollapsedState()
	if err == nil {
		t.Fatal("persistCollapsedState() returned nil after cache path resolution failure")
	}
	if !strings.Contains(am.statusText, err.Error()) {
		t.Fatalf("persistCollapsedState() error missing from status %q: %v", am.statusText, err)
	}
}

func (m *attachModel) selectAttachMember(name string) bool {
	for index, row := range m.rows() {
		if row.Member != nil && row.Member.Name == name {
			m.selected = index
			return true
		}
	}
	return false
}

func (m *attachModel) selectAttachHeader(name string) bool {
	for index, row := range m.rows() {
		if row.Header != nil && row.Header.Name == name {
			m.selected = index
			return true
		}
	}
	return false
}

func (m *attachModel) selectedHeaderName() string {
	rows := m.rows()
	if m.selected < 0 || m.selected >= len(rows) || rows[m.selected].Header == nil {
		return ""
	}
	return rows[m.selected].Header.Name
}

func attachKeyMsg(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

type attachRecordedRequest struct {
	method string
	path   string
	body   string
}

type attachRecordingTransport struct {
	base     http.RoundTripper
	requests []attachRecordedRequest
}

func (t *attachRecordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var data []byte
	if request.Body != nil {
		data, _ = io.ReadAll(request.Body)
		request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(data))
	}
	t.requests = append(t.requests, attachRecordedRequest{
		method: request.Method,
		path:   request.URL.Path,
		body:   string(data),
	})
	return t.base.RoundTrip(request)
}

func webKeyMsg() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}}
}
