package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

type cachedUIState struct {
	Collapsed    []string `json:"collapsed"`
	SidebarWidth int      `json:"sidebar_width,omitempty"`
}

func uiStatePath(configPath string) (string, error) {
	_, id, err := instanceID(configPath)
	if err != nil {
		return "", err
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "stacker", "ui", id+".json"), nil
}

func loadUIState(path string) (cachedUIState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cachedUIState{Collapsed: []string{}}, nil
	}
	if err != nil {
		return cachedUIState{}, err
	}
	var state cachedUIState
	if err := json.Unmarshal(data, &state); err != nil {
		return cachedUIState{}, err
	}
	if state.Collapsed == nil {
		state.Collapsed = []string{}
	}
	return state, nil
}

func loadCollapsed(path string) (map[string]bool, error) {
	state, err := loadUIState(path)
	if err != nil {
		return nil, err
	}

	collapsed := make(map[string]bool, len(state.Collapsed))
	for _, name := range state.Collapsed {
		collapsed[name] = true
	}
	return collapsed, nil
}

func loadCollapsedOrEmpty(path string) map[string]bool {
	collapsed, err := loadCollapsed(path)
	if err != nil {
		return map[string]bool{}
	}
	return collapsed
}

func saveCollapsed(path string, state map[string]bool, sections []section) error {
	stored, err := loadUIState(path)
	if err != nil {
		stored = cachedUIState{}
	}
	known := make(map[string]bool, len(sections))
	for _, current := range sections {
		known[current.Name] = true
	}

	names := make([]string, 0, len(state))
	for name, collapsed := range state {
		if collapsed && known[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	stored.Collapsed = names
	return writeUIState(path, stored)
}

func saveSidebarWidth(path string, width int) error {
	stored, err := loadUIState(path)
	if err != nil {
		stored = cachedUIState{Collapsed: []string{}}
	}
	stored.SidebarWidth = width
	return writeUIState(path, stored)
}

func writeUIState(path string, state cachedUIState) error {
	if state.Collapsed == nil {
		state.Collapsed = []string{}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ui-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return nil
}
