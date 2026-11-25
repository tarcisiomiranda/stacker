package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPickerDiscoversProjectsAndOffersWorkspace(t *testing.T) {
	root := t.TempDir()
	setup := `
version: 1
processes:
  api:
    command: echo api
    port: 8599
`
	for _, dir := range []string{"beta app", "alpha", "skip", "skip/deeper"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if dir != "skip" {
			writeConfig(t, filepath.Join(root, dir), setup)
		}
	}
	parentConfig := filepath.Join(root, "stacker.yml")
	projects := discoverProjects(parentConfig)
	if len(projects) != 2 || projects[0].name != "alpha" || projects[1].name != "beta app" {
		t.Fatalf("discovered %#v, want alpha and beta app", projects)
	}
	running := []instanceSummary{{
		State: InstanceState{PID: 123, Config: projects[0].config, Mode: "serve"},
		Ports: []int{8599},
	}}
	rows := pickerProjectRows(projects, running)
	if len(rows) != 2 || rows[0].State.PID != 123 || rows[1].State.PID != 0 || rows[1].Total != 1 {
		t.Fatalf("picker rows = %+v", rows)
	}
	if !equalInts(rows[0].Collide, []int{8599}) || !equalInts(rows[1].Collide, []int{8599}) {
		t.Fatalf("picker rows lack collision: %+v", rows)
	}
	if list := formatProjectChoices(rows); !strings.Contains(list, "stacker --config '"+projects[1].config+"'") {
		t.Fatalf("missing stopped project's command in %q", list)
	}
	model := newPickerModel(rows, "")
	model.projects, model.discoveryPath = projects, parentConfig
	for index, want := range []pickerAction{pickerWorkspace, pickerAttach, pickerStart} {
		model.selected = index
		model.Update(key("enter"))
		if model.action != want {
			t.Fatalf("picker row %d chose %v, want %v", index, model.action, want)
		}
	}
}

func TestWorkspaceUsesTheSelectedProjectsControlPlane(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	projectConfig := `
version: 1
processes:
  api:
    command: echo api
    autostart: true
`
	sources := map[string]*model{}
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := writeConfig(t, dir, projectConfig)
		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		m := newModel(cfg)
		m.processes[0].appendLog(name + " log")
		server, err := startControlServer(m, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(server.Close)
		sources[name] = m
	}
	rootConfig := filepath.Join(root, "stacker.yml")
	ui := newAttachModel(nil, rootConfig)
	ui.workspace = &workspaceView{projects: discoverProjects(rootConfig)}
	ui.Update(ui.pollSnap()())
	if len(ui.sections()) != 2 || !ui.selectByName("beta/api") {
		t.Fatalf("workspace snapshot = %+v", ui.procs)
	}
	ui.Update(ui.pollLogs()())
	if len(ui.logs) != 1 || ui.logs[0] != "beta log" {
		t.Fatalf("selected logs = %q, want beta log", ui.logs)
	}
	ui.Update(ui.postAction("mark")())
	if sources["alpha"].processes[0].LogNext() != 1 || !strings.Contains(strings.Join(sources["beta"].processes[0].Logs(), "\n"), "mark ") {
		t.Fatal("mark should reach only beta/api")
	}
}

func TestControlServerRespectsAnInProgressClaim(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "stacker.yml")
	_, path, err := statePathFor(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := lockInstance(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if server, err := startControlServer(newModel(Config{}), configPath); err == nil {
		server.Close()
		t.Fatal("a concurrent start claimed the same config")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	server, err := startControlServer(newModel(Config{}), configPath)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
}
