package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

func webKeyMsg() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}}
}
