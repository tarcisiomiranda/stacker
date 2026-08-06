package main

import (
	"strings"
	"testing"
)

func inst(config string) *InstanceState {
	return &InstanceState{PID: 4510, Config: config, Addr: "127.0.0.1:1", Mode: "serve"}
}

// The resolution table from the instance-disambiguation design. The row that
// matters most is "explicit, target not running, another instance alive": that
// used to attach to the unrelated instance, so `stacker --config stacker.yml`
// inside a real project opened a different project entirely.
func TestResolveLaunch(t *testing.T) {
	other := []InstanceState{*inst("/srv/other/stacker.yml")}
	twoOthers := []InstanceState{*inst("/srv/a/stacker.yml"), *inst("/srv/b/stacker.yml")}

	cases := []struct {
		name       string
		explicit   bool
		canStart   bool
		target     *InstanceState
		others     []InstanceState
		wantMode   launchMode
		wantConfig string
	}{
		{
			name:     "explicit and target running attaches to it",
			explicit: true, canStart: true,
			target:   inst("/srv/mine/stacker.yml"),
			others:   other,
			wantMode: launchAttach, wantConfig: "/srv/mine/stacker.yml",
		},
		{
			name:     "explicit and target down starts it despite another instance",
			explicit: true, canStart: true,
			target:   nil,
			others:   other,
			wantMode: launchSession, wantConfig: "mine.yml",
		},
		{
			name:     "explicit and target down starts it when nothing else runs",
			explicit: true, canStart: true,
			target:   nil,
			others:   nil,
			wantMode: launchSession, wantConfig: "mine.yml",
		},
		{
			name:     "implicit and target running attaches to it",
			explicit: false, canStart: true,
			target:   inst("/srv/mine/stacker.yml"),
			others:   other,
			wantMode: launchAttach, wantConfig: "/srv/mine/stacker.yml",
		},
		{
			name:     "implicit with one other instance shows the picker",
			explicit: false, canStart: true,
			target:   nil,
			others:   other,
			wantMode: launchPicker, wantConfig: "mine.yml",
		},
		{
			name:     "implicit with several other instances shows the picker",
			explicit: false, canStart: true,
			target:   nil,
			others:   twoOthers,
			wantMode: launchPicker, wantConfig: "mine.yml",
		},
		{
			name:     "implicit with nothing running starts the local config",
			explicit: false, canStart: true,
			target:   nil,
			others:   nil,
			wantMode: launchSession, wantConfig: "mine.yml",
		},
		// `attach` must never start a supervisor.
		{
			name:     "attach with explicit target down is an error",
			explicit: true, canStart: false,
			target:   nil,
			others:   other,
			wantMode: launchNoInstance, wantConfig: "mine.yml",
		},
		{
			name:     "attach with nothing running at all is an error",
			explicit: false, canStart: false,
			target:   nil,
			others:   nil,
			wantMode: launchNoInstance, wantConfig: "mine.yml",
		},
		{
			name:     "attach without a flag picks among live instances",
			explicit: false, canStart: false,
			target:   nil,
			others:   other,
			wantMode: launchPicker, wantConfig: "mine.yml",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, config := resolveLaunch("mine.yml", tc.explicit, tc.canStart, tc.target, tc.others)
			if mode != tc.wantMode || config != tc.wantConfig {
				t.Fatalf("resolveLaunch = (%v, %q), want (%v, %q)",
					mode, config, tc.wantMode, tc.wantConfig)
			}
		})
	}
}

// Starting a second stack is legitimate, but a shared port is not obvious until
// something dies: free-port terminates whatever holds the port, so the user has
// to be told before it happens.
func TestPortClashes(t *testing.T) {
	cfg := Config{Processes: map[string]ProcessConfig{
		"api":      {Port: 8080},
		"front":    {Port: 3000},
		"worker":   {},
		"database": {Port: 5432},
	}}
	rows := []instanceSummary{
		{Label: "other", Ports: []int{8080, 9999}},
		{Label: "third", Ports: []int{5432}},
	}
	if got := portClashes(cfg, rows); !equalInts(got, []int{5432, 8080}) {
		t.Fatalf("portClashes = %#v, want [5432 8080]", got)
	}
}

func TestPortClashesEmptyWhenNothingShared(t *testing.T) {
	cfg := Config{Processes: map[string]ProcessConfig{"api": {Port: 8080}}}
	rows := []instanceSummary{{Label: "other", Ports: []int{3000}}}
	if got := portClashes(cfg, rows); len(got) != 0 {
		t.Fatalf("portClashes = %#v, want none", got)
	}
}

func TestFormatOtherInstancesNotice(t *testing.T) {
	rows := []instanceSummary{
		{Label: "bunker-orchestrator", Ports: []int{8080}},
		{Label: "_cyber_", Ports: []int{3001}},
	}

	plain := formatOtherInstancesNotice(rows, nil)
	if !strings.Contains(plain, "2") {
		t.Fatalf("notice should count the other instances: %q", plain)
	}
	for _, label := range []string{"bunker-orchestrator", "_cyber_"} {
		if !strings.Contains(plain, label) {
			t.Fatalf("notice missing %q: %q", label, plain)
		}
	}

	withClash := formatOtherInstancesNotice(rows, []int{8080})
	if !strings.Contains(withClash, "8080") {
		t.Fatalf("notice should name the shared port: %q", withClash)
	}
}

func TestFormatOtherInstancesNoticeEmptyWithoutOthers(t *testing.T) {
	if got := formatOtherInstancesNotice(nil, nil); got != "" {
		t.Fatalf("notice = %q, want empty", got)
	}
}

// A running target wins regardless of mode: attaching to a session-mode TUI is
// valid (q detaches), and is far better than the "already running" error a
// second session used to produce.
func TestResolveLaunchAttachesToSessionModeTarget(t *testing.T) {
	target := &InstanceState{PID: 1, Config: "/srv/mine/stacker.yml", Mode: "session"}
	mode, config := resolveLaunch("mine.yml", true, true, target, nil)
	if mode != launchAttach || config != "/srv/mine/stacker.yml" {
		t.Fatalf("resolveLaunch = (%v, %q), want attach to the session instance", mode, config)
	}
}
