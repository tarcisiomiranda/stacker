package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCollapsedStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui", "collapsed.json")
	want := map[string]bool{"core": true, "data": true}
	sections := []section{{Name: "core"}, {Name: "data"}}

	if err := saveCollapsed(path, want, sections); err != nil {
		t.Fatalf("saveCollapsed() error = %v", err)
	}

	got, err := loadCollapsed(path)
	if err != nil {
		t.Fatalf("loadCollapsed() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadCollapsed() = %#v, want %#v", got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "{\"collapsed\":[\"core\",\"data\"]}\n" {
		t.Fatalf("saved state = %q, want sorted names", data)
	}
}

func TestCollapsedStateMissingFileReturnsEmptyMap(t *testing.T) {
	got, err := loadCollapsed(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("loadCollapsed() error = %v", err)
	}
	if !reflect.DeepEqual(got, map[string]bool{}) {
		t.Fatalf("loadCollapsed() = %#v, want empty map", got)
	}
}

func TestCollapsedStateMalformedJSONReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collapsed.json")
	if err := os.WriteFile(path, []byte("["), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := loadCollapsed(path); err == nil {
		t.Fatal("loadCollapsed() error = nil, want malformed JSON error")
	}
}

func TestCollapsedStateStartupRecoversMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collapsed.json")
	if err := os.WriteFile(path, []byte("["), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if got := loadCollapsedOrEmpty(path); !reflect.DeepEqual(got, map[string]bool{}) {
		t.Fatalf("loadCollapsedOrEmpty() = %#v, want empty map", got)
	}
}

func TestCollapsedStateSaveFiltersStaleNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collapsed.json")
	state := map[string]bool{"core": true, "removed": true, "data": false}
	sections := []section{{Name: "core"}, {Name: "data"}}

	if err := saveCollapsed(path, state, sections); err != nil {
		t.Fatalf("saveCollapsed() error = %v", err)
	}

	got, err := loadCollapsed(path)
	if err != nil {
		t.Fatalf("loadCollapsed() error = %v", err)
	}
	if !reflect.DeepEqual(got, map[string]bool{"core": true}) {
		t.Fatalf("loadCollapsed() = %#v, want only the known collapsed section", got)
	}
}

func TestCollapsedStateCreatesUserOnlyDirectories(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private", "ui")
	if err := saveCollapsed(filepath.Join(directory, "collapsed.json"), map[string]bool{"core": true}, []section{{Name: "core"}}); err != nil {
		t.Fatalf("saveCollapsed() error = %v", err)
	}

	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Fatalf("directory permissions = %#o, want %#o", got, 0700)
	}
}

func TestCollapsedStatePathIsDistinctPerConfig(t *testing.T) {
	configDir := t.TempDir()
	firstConfig := filepath.Join(configDir, "first.yml")
	secondConfig := filepath.Join(configDir, "second.yml")

	firstPath, err := uiStatePath(firstConfig)
	if err != nil {
		t.Fatalf("uiStatePath(first) error = %v", err)
	}
	secondPath, err := uiStatePath(secondConfig)
	if err != nil {
		t.Fatalf("uiStatePath(second) error = %v", err)
	}
	if firstPath == secondPath {
		t.Fatalf("uiStatePath() returned the same path for distinct configs: %q", firstPath)
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir() error = %v", err)
	}
	_, firstID, err := instanceID(firstConfig)
	if err != nil {
		t.Fatalf("instanceID(first) error = %v", err)
	}
	if want := filepath.Join(cacheDir, "stacker", "ui", firstID+".json"); firstPath != want {
		t.Fatalf("uiStatePath(first) = %q, want %q", firstPath, want)
	}
}

func TestCollapsedStateFoldInputsPersistSessionState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collapsed.json")
	m := groupedModel()
	m.collapsedPath = path
	m.selected = 1
	m.foldSelectedSection(true)

	got, err := loadCollapsed(path)
	if err != nil {
		t.Fatalf("loadCollapsed() after keyboard fold error = %v", err)
	}
	if !reflect.DeepEqual(got, map[string]bool{"core": true}) {
		t.Fatalf("loadCollapsed() after keyboard fold = %#v, want core collapsed", got)
	}

	m.width = 100
	m.handleMouse(tea.MouseMsg{X: 1, Y: 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	got, err = loadCollapsed(path)
	if err != nil {
		t.Fatalf("loadCollapsed() after mouse fold error = %v", err)
	}
	if !reflect.DeepEqual(got, map[string]bool{}) {
		t.Fatalf("loadCollapsed() after mouse fold = %#v, want expanded sections", got)
	}
}

func TestCollapsedStateMovesHiddenSelectionToSectionHeader(t *testing.T) {
	m := groupedModel()
	selectedName, selectedHeader := m.selectedIdentity()
	path := filepath.Join(t.TempDir(), "collapsed.json")
	if err := saveCollapsed(path, map[string]bool{"core": true}, m.sections()); err != nil {
		t.Fatalf("saveCollapsed() error = %v", err)
	}
	collapsed, err := loadCollapsed(path)
	if err != nil {
		t.Fatalf("loadCollapsed() error = %v", err)
	}
	m.collapsed = collapsed
	m.restoreSelection(selectedName, selectedHeader)

	section := m.selectedSection()
	if section == nil || section.Name != "core" {
		t.Fatalf("selected section after restoring collapsed state = %#v, want core header", section)
	}
}
