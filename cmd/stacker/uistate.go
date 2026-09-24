package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

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

func loadCollapsed(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}

	var state struct {
		Collapsed []string `json:"collapsed"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Collapsed == nil {
		return nil, fmt.Errorf("collapsed state must contain a collapsed array")
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

	data, err := json.Marshal(struct {
		Collapsed []string `json:"collapsed"`
	}{Collapsed: names})
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return nil
}
