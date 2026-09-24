package main

import (
	"reflect"
	"testing"
)

func TestBuildSections(t *testing.T) {
	tests := []struct {
		name    string
		members []sectionMember
		want    []section
	}{
		{
			name: "first appearance and input order",
			members: []sectionMember{
				{Name: "api", Group: "hub", Index: 4},
				{Name: "worker", Group: "jobs", Index: 1},
				{Name: "api-next", Group: "hub", Index: 8},
				{Name: "cron", OneShot: true, Index: 9},
				{Name: "hub-migrate", Group: "hub", OneShot: true, Index: 10},
				{Name: "publish", Group: "release", OneShot: true, Index: 12},
			},
			want: []section{
				{
					Name:     "hub",
					Services: []sectionMember{{Name: "api", Group: "hub", Index: 4}, {Name: "api-next", Group: "hub", Index: 8}},
					Tasks:    []sectionMember{{Name: "hub-migrate", Group: "hub", OneShot: true, Index: 10}},
				},
				{
					Name:     "jobs",
					Services: []sectionMember{{Name: "worker", Group: "jobs", Index: 1}},
				},
				{
					Name:  "release",
					Tasks: []sectionMember{{Name: "publish", Group: "release", OneShot: true, Index: 12}},
				},
				{
					Name:     "Other",
					Implicit: true,
					Tasks:    []sectionMember{{Name: "cron", OneShot: true, Index: 9}},
				},
			},
		},
		{
			name: "explicit Other merges with implicit members and stays last",
			members: []sectionMember{
				{Name: "default-service", Index: 0},
				{Name: "named-service", Group: "Other", Index: 1},
				{Name: "api", Group: "hub", Index: 2},
				{Name: "default-task", OneShot: true, Index: 3},
			},
			want: []section{
				{
					Name:     "hub",
					Services: []sectionMember{{Name: "api", Group: "hub", Index: 2}},
				},
				{
					Name:     "Other",
					Implicit: false,
					Services: []sectionMember{{Name: "default-service", Index: 0}, {Name: "named-service", Group: "Other", Index: 1}},
					Tasks:    []sectionMember{{Name: "default-task", OneShot: true, Index: 3}},
				},
			},
		},
		{
			name: "service-bearing sections precede task-only sections",
			members: []sectionMember{
				{Name: "ops-task", Group: "ops", OneShot: true, Index: 0},
				{Name: "api", Group: "hub", Index: 1},
				{Name: "worker", Group: "jobs", Index: 2},
				{Name: "qa-task", Group: "qa", OneShot: true, Index: 3},
				{Name: "cron", OneShot: true, Index: 4},
			},
			want: []section{
				{Name: "hub", Services: []sectionMember{{Name: "api", Group: "hub", Index: 1}}},
				{Name: "jobs", Services: []sectionMember{{Name: "worker", Group: "jobs", Index: 2}}},
				{Name: "ops", Tasks: []sectionMember{{Name: "ops-task", Group: "ops", OneShot: true, Index: 0}}},
				{Name: "qa", Tasks: []sectionMember{{Name: "qa-task", Group: "qa", OneShot: true, Index: 3}}},
				{Name: "Other", Implicit: true, Tasks: []sectionMember{{Name: "cron", OneShot: true, Index: 4}}},
			},
		},
		{
			name:    "whitespace group is implicit Other",
			members: []sectionMember{{Name: "api", Group: "  \t", Index: 7}},
			want:    []section{{Name: "Other", Implicit: true, Services: []sectionMember{{Name: "api", Group: "  \t", Index: 7}}}},
		},
		{
			name: "empty members",
			want: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := buildSections(test.members); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("buildSections() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestBuildRows(t *testing.T) {
	tests := []struct {
		name      string
		sections  []section
		collapsed map[string]bool
		want      []rowValue
	}{
		{
			name: "single implicit Other hides its header",
			sections: []section{{
				Name:     "Other",
				Implicit: true,
				Services: []sectionMember{{Name: "api"}},
				Tasks:    []sectionMember{{Name: "migrate", OneShot: true}},
			}},
			want: []rowValue{{member: "api"}, {member: "migrate"}},
		},
		{
			name: "collapsed state does not hide headerless implicit Other",
			sections: []section{{
				Name:     "Other",
				Implicit: true,
				Services: []sectionMember{{Name: "api"}},
				Tasks:    []sectionMember{{Name: "migrate", OneShot: true}},
			}},
			collapsed: map[string]bool{"Other": true},
			want:      []rowValue{{member: "api"}, {member: "migrate"}},
		},
		{
			name: "explicit Other shows its header",
			sections: []section{{
				Name:     "Other",
				Implicit: false,
				Services: []sectionMember{{Name: "api"}, {Name: "named", Group: "Other"}},
			}},
			want: []rowValue{{header: "Other"}, {member: "api"}, {member: "named"}},
		},
		{
			name: "sections and members retain service task order",
			sections: []section{
				{
					Name:     "api",
					Services: []sectionMember{{Name: "api"}, {Name: "worker"}},
					Tasks:    []sectionMember{{Name: "migrate", OneShot: true}},
				},
				{
					Name:  "jobs",
					Tasks: []sectionMember{{Name: "seed", OneShot: true}},
				},
			},
			want: []rowValue{
				{header: "api"},
				{member: "api"},
				{member: "worker"},
				{member: "migrate"},
				{header: "jobs"},
				{member: "seed"},
			},
		},
		{
			name: "collapsed section keeps only its header",
			sections: []section{
				{
					Name:     "api",
					Services: []sectionMember{{Name: "api"}},
					Tasks:    []sectionMember{{Name: "migrate", OneShot: true}},
				},
				{
					Name:     "Other",
					Implicit: true,
					Services: []sectionMember{{Name: "standalone"}},
				},
			},
			collapsed: map[string]bool{"api": true},
			want: []rowValue{
				{header: "api"},
				{header: "Other"},
				{member: "standalone"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := rowValues(buildRows(test.sections, test.collapsed))
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("buildRows() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSummarizeSection(t *testing.T) {
	tests := []struct {
		name    string
		members []memberState
		running int
		total   int
		state   string
	}{
		{
			name: "counts only services and errors from tasks take priority",
			members: []memberState{
				{Status: StatusRunning},
				{Status: StatusStopped},
				{Status: StatusFailed, OneShot: true, Errors: 1},
			},
			running: 1,
			total:   2,
			state:   "error",
		},
		{
			name: "task failure wins when no member has errors",
			members: []memberState{
				{Status: StatusFailed, OneShot: true},
				{Status: StatusRunning},
			},
			running: 1,
			total:   1,
			state:   "failed",
		},
		{
			name: "all running services are running even with idle tasks",
			members: []memberState{
				{Status: StatusRunning},
				{Status: StatusStopped, OneShot: true},
			},
			running: 1,
			total:   1,
			state:   "running",
		},
		{
			name:    "task only section is muted rather than vacuously running",
			members: []memberState{{Status: StatusRunning, OneShot: true}},
			state:   "muted",
		},
		{
			name:    "stopped services are muted",
			members: []memberState{{Status: StatusStopped}},
			total:   1,
			state:   "muted",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := summarizeSection(test.members)
			if got.Running != test.running || got.Total != test.total || got.State != test.state {
				t.Fatalf("summarizeSection() = %#v, want running=%d total=%d state=%q", got, test.running, test.total, test.state)
			}
		})
	}
}

type rowValue struct {
	header string
	member string
}

func rowValues(rows []listRow) []rowValue {
	values := make([]rowValue, 0, len(rows))
	for _, row := range rows {
		value := rowValue{}
		if row.Header != nil {
			value.header = row.Header.Name
		}
		if row.Member != nil {
			value.member = row.Member.Name
		}
		values = append(values, value)
	}
	return values
}
