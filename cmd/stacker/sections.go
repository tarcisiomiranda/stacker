package main

import "strings"

type sectionMember struct {
	Name     string
	Group    string
	OneShot  bool
	Orphaned bool
	Index    int
}

type section struct {
	Name     string
	Implicit bool
	Services []sectionMember
	Tasks    []sectionMember
}

type listRow struct {
	Header *section
	Member *sectionMember
}

type memberState struct {
	Status  ProcessStatus
	Errors  int
	OneShot bool
}

type sectionSummary struct {
	Running int
	Total   int
	State   string
}

func summarizeSection(members []memberState) sectionSummary {
	summary := sectionSummary{State: "muted"}
	for _, member := range members {
		if member.Errors > 0 {
			summary.State = "error"
		}
		if member.Status == StatusFailed && summary.State != "error" {
			summary.State = "failed"
		}
		if member.OneShot {
			continue
		}
		summary.Total++
		if member.Status == StatusRunning {
			summary.Running++
		}
	}
	if summary.State == "error" || summary.State == "failed" {
		return summary
	}
	if summary.Total > 0 && summary.Running == summary.Total {
		summary.State = "running"
	}
	return summary
}

func buildSections(members []sectionMember) []section {
	var sections []section
	sectionIndexes := make(map[string]int)

	for _, member := range members {
		group := strings.TrimSpace(member.Group)
		implicit := group == ""
		if implicit {
			group = "Other"
		}

		index, exists := sectionIndexes[group]
		if !exists {
			index = len(sections)
			sectionIndexes[group] = index
			sections = append(sections, section{Name: group, Implicit: group == "Other" && implicit})
		} else if group == "Other" && !implicit {
			sections[index].Implicit = false
		}

		if member.OneShot {
			sections[index].Tasks = append(sections[index].Tasks, member)
		} else {
			sections[index].Services = append(sections[index].Services, member)
		}
	}

	var serviceSections []section
	var taskSections []section
	var other section
	hasOther := false
	for _, current := range sections {
		if current.Name == "Other" {
			other = current
			hasOther = true
		} else if len(current.Services) > 0 {
			serviceSections = append(serviceSections, current)
		} else {
			taskSections = append(taskSections, current)
		}
	}

	sections = append(serviceSections, taskSections...)
	if hasOther {
		sections = append(sections, other)
	}
	return sections
}

func sectionDisplayOrder(sections []section) ([]string, []string) {
	var services []string
	var tasks []string
	for _, current := range sections {
		for _, member := range current.Services {
			if !member.Orphaned {
				services = append(services, member.Name)
			}
		}
		for _, member := range current.Tasks {
			if !member.Orphaned {
				tasks = append(tasks, member.Name)
			}
		}
	}
	return services, tasks
}

func sectionMoveRefusal(sections []section, index, delta int) string {
	if index < 0 || index >= len(sections) {
		return "Selected section can't be reordered"
	}
	current := sections[index]
	if current.Name == "Other" {
		return "Other section is fixed last"
	}
	target := index + delta
	if delta != -1 && delta != 1 {
		return "Sections move one position at a time"
	}
	if target < 0 || target >= len(sections) {
		return "Section is at the display edge"
	}
	adjacent := sections[target]
	if adjacent.Name == "Other" {
		return "Other section is fixed last"
	}
	currentHasServices := len(current.Services) > 0
	adjacentHasServices := len(adjacent.Services) > 0
	if currentHasServices != adjacentHasServices {
		return "Section can't cross service/task tiers"
	}
	if currentHasServices {
		if hasConfiguredMember(current.Services) && hasConfiguredMember(adjacent.Services) {
			return ""
		}
	} else if hasConfiguredMember(current.Tasks) && hasConfiguredMember(adjacent.Tasks) {
		return ""
	}
	return "Section order can't be represented by YAML mappings"
}

func hasConfiguredMember(members []sectionMember) bool {
	for _, member := range members {
		if !member.Orphaned {
			return true
		}
	}
	return false
}

func buildRows(sections []section, collapsed map[string]bool) []listRow {
	var rows []listRow
	hideHeader := len(sections) == 1 && sections[0].Name == "Other" && sections[0].Implicit

	for i := range sections {
		current := &sections[i]
		if !hideHeader {
			rows = append(rows, listRow{Header: current})
		}
		if collapsed[current.Name] && !hideHeader {
			continue
		}
		for j := range current.Services {
			rows = append(rows, listRow{Member: &current.Services[j]})
		}
		for j := range current.Tasks {
			rows = append(rows, listRow{Member: &current.Tasks[j]})
		}
	}

	return rows
}
