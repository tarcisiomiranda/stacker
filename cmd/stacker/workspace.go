package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type workspaceProject struct {
	name   string
	config string
}

func discoverProjects(configPath string) []workspaceProject {
	root, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var projects []workspaceProject
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), "stacker.yml")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			projects = append(projects, workspaceProject{name: entry.Name(), config: path})
		}
	}
	return projects
}

func (project workspaceProject) configuredProcesses() ([]ProcessInfo, error) {
	cfg, err := loadConfig(project.config)
	if err != nil {
		return nil, err
	}
	return newModel(cfg).processInfos(), nil
}

func (project workspaceProject) processes() ([]ProcessInfo, error) {
	state, err := findRunningInstance(project.config)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return project.configuredProcesses()
	}
	return readProcessInfos(clientForInstance(state, 2*time.Second))
}

type workspaceView struct {
	projects []workspaceProject
}

func (w *workspaceView) snapshot() ([]ProcessInfo, error) {
	procs := make([]ProcessInfo, 0)
	var problems []error
	for _, project := range w.projects {
		entries, err := project.processes()
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", project.name, err))
			continue
		}
		for _, process := range entries {
			process.Name = project.name + "/" + process.Name
			process.Group = project.name
			procs = append(procs, process)
		}
	}
	return procs, errors.Join(problems...)
}

func (w *workspaceView) projectFor(name string) workspaceProject {
	for _, project := range w.projects {
		if project.name == name {
			return project
		}
	}
	return workspaceProject{}
}

func (w *workspaceView) sections(members []sectionMember) []section {
	sections := make([]section, len(w.projects))
	indexes := make(map[string]int, len(w.projects))
	for i, project := range w.projects {
		sections[i].Name = project.name
		indexes[project.name] = i
	}
	for _, member := range members {
		index, ok := indexes[member.Group]
		if !ok {
			continue
		}
		current := &sections[index]
		if member.OneShot {
			current.Tasks = append(current.Tasks, member)
		} else {
			current.Services = append(current.Services, member)
		}
	}
	return sections
}

func (project workspaceProject) connect(start bool, timeout time.Duration) (*controlClient, InstanceState, error) {
	state, err := findRunningInstance(project.config)
	if err == nil && state == nil && start {
		state, _, err = startServeDaemon(project.config, false, true)
	}
	if err != nil {
		return nil, InstanceState{}, fmt.Errorf("%s: %w", project.name, err)
	}
	if state == nil {
		return nil, InstanceState{}, nil
	}
	return clientForInstance(state, timeout), *state, nil
}

func workspaceProcessName(qualified string) string {
	_, name, _ := strings.Cut(qualified, "/")
	return name
}
