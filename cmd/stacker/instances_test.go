package main

import (
	"strings"
	"testing"
	"time"
)

// The label is what tells two instances apart at a glance, so it has to stay
// unique even when several projects name their config directory the same.
func TestLabelInstances(t *testing.T) {
	t.Run("basename of the config directory", func(t *testing.T) {
		got := labelInstances([]InstanceState{
			{Config: "/srv/_cyber_/stacker.yml"},
			{Config: "/srv/_vsentinel_/bunker-orchestrator/stacker.yml"},
		})
		want := []string{"_cyber_", "bunker-orchestrator"}
		if !equalStrings(got, want) {
			t.Fatalf("labelInstances = %#v, want %#v", got, want)
		}
	})

	t.Run("colliding basenames fall back to two segments", func(t *testing.T) {
		got := labelInstances([]InstanceState{
			{Config: "/srv/alpha/frontend/stacker.yml"},
			{Config: "/srv/beta/frontend/stacker.yml"},
			{Config: "/srv/_cyber_/stacker.yml"},
		})
		want := []string{"alpha/frontend", "beta/frontend", "_cyber_"}
		if !equalStrings(got, want) {
			t.Fatalf("labelInstances = %#v, want %#v", got, want)
		}
	})

	t.Run("a config at the filesystem root still yields something", func(t *testing.T) {
		got := labelInstances([]InstanceState{{Config: "/stacker.yml"}})
		if len(got) != 1 || strings.TrimSpace(got[0]) == "" {
			t.Fatalf("labelInstances = %#v, want a non-empty label", got)
		}
	})
}

// Without a TTY the picker must never block: it prints the same information and
// exits, so agents and scripts get a readable error instead of a hung prompt.
func TestFormatInstanceList(t *testing.T) {
	rows := []instanceSummary{
		{
			State: InstanceState{PID: 4510, Config: "/srv/_vsentinel_/bunker-orchestrator/stacker.yml", Mode: "serve"},
			Label: "bunker-orchestrator",
			Ports: []int{4510, 4520, 8080},
			Up:    1, Total: 7,
		},
		{
			State: InstanceState{PID: 29649, Config: "/srv/_cyber_/stacker.yml", Mode: "session"},
			Label: "_cyber_",
			Ports: []int{3001, 8080},
			Up:    2, Total: 20,
			Collide: []int{8080},
		},
	}
	out := formatInstanceList(rows, "/srv/stacker/stacker.yml")

	for _, want := range []string{
		"bunker-orchestrator",
		"_cyber_",
		"/srv/_cyber_/stacker.yml", // full path, so the choice is unambiguous
		"4510",                     // pid or port, both appear
		"serve",
		"session",
		"1/7",
		"2/20",
		"8080", // the collision must be visible
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("formatInstanceList output missing %q:\n%s", want, out)
		}
	}

	// The local config is offered as a way out, not silently ignored.
	if !strings.Contains(out, "/srv/stacker/stacker.yml") {
		t.Fatalf("output does not offer the local config:\n%s", out)
	}
}

func TestFormatInstanceListWithoutLocalConfig(t *testing.T) {
	rows := []instanceSummary{{
		State: InstanceState{PID: 1, Config: "/srv/a/stacker.yml", Mode: "serve"},
		Label: "a",
	}}
	// An empty local config means the working directory has no stacker.yml, so
	// there is nothing to offer starting.
	out := formatInstanceList(rows, "")
	if strings.Contains(out, "stacker --config  ") {
		t.Fatalf("output suggests starting an empty config:\n%s", out)
	}
}

// A PID makes a cross-instance port collision provable: the listener found on a
// port can be matched to the exact process that owns it, instead of guessing
// from declared ports alone.
func TestProcessInfoIncludesPID(t *testing.T) {
	p := NewProcess("demo", ProcessConfig{
		Command: "trap 'exit 0' TERM; while :; do sleep 1; done",
		Cwd:     ".",
	}, 100)
	if got := processInfo(p).PID; got != 0 {
		t.Fatalf("stopped process reported pid %d, want 0", got)
	}
	if err := p.Start(func() {}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = p.Stop(func() {}) }()
	waitFor(t, 3*time.Second, func() bool { return p.Status() == StatusRunning })
	if got := processInfo(p).PID; got <= 0 {
		t.Fatalf("running process reported pid %d, want a real pid", got)
	}
}

// This is the situation the user cannot see today: two projects declaring the
// same port, where starting one terminates the other's service.
func TestMarkCollisions(t *testing.T) {
	rows := []instanceSummary{
		{Label: "a", Ports: []int{8080, 3000}},
		{Label: "b", Ports: []int{4000, 8080}},
		{Label: "c", Ports: []int{5000}},
	}
	markCollisions(rows)

	if !equalInts(rows[0].Collide, []int{8080}) {
		t.Fatalf("row a collide = %#v, want [8080]", rows[0].Collide)
	}
	if !equalInts(rows[1].Collide, []int{8080}) {
		t.Fatalf("row b collide = %#v, want [8080]", rows[1].Collide)
	}
	if len(rows[2].Collide) != 0 {
		t.Fatalf("row c collide = %#v, want none", rows[2].Collide)
	}
}

// A port repeated inside one config is that config's own business, not a
// cross-instance collision.
func TestMarkCollisionsIgnoresDuplicatesWithinOneInstance(t *testing.T) {
	rows := []instanceSummary{
		{Label: "a", Ports: []int{8080, 8080}},
		{Label: "b", Ports: []int{3000}},
	}
	markCollisions(rows)
	if len(rows[0].Collide) != 0 || len(rows[1].Collide) != 0 {
		t.Fatalf("no cross-instance collision expected: %#v / %#v", rows[0].Collide, rows[1].Collide)
	}
}

// collectInstanceSummaries talks to each live control plane, so it is exercised
// against two real supervisors rather than stubs.
func TestCollectInstanceSummaries(t *testing.T) {
	dirA := t.TempDir()
	cfgA := writeConfig(t, dirA, `
version: 1
processes:
  api:
    command: true
    port: 8080
  worker:
    command: true
`)
	dirB := t.TempDir()
	cfgB := writeConfig(t, dirB, `
version: 1
processes:
  web:
    command: true
    port: 8080
`)

	for _, path := range []string{cfgA, cfgB} {
		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatalf("loadConfig %s: %v", path, err)
		}
		m := newModel(cfg)
		m.mode = "serve"
		cs, err := startControlServer(m, path)
		if err != nil {
			t.Fatalf("startControlServer %s: %v", path, err)
		}
		defer cs.Close()
	}

	all, err := listRunningInstances()
	if err != nil {
		t.Fatalf("listRunningInstances: %v", err)
	}
	rows := collectInstanceSummaries(all)

	var a, b *instanceSummary
	for i := range rows {
		switch rows[i].State.Config {
		case mustAbs(t, cfgA):
			a = &rows[i]
		case mustAbs(t, cfgB):
			b = &rows[i]
		}
	}
	if a == nil || b == nil {
		t.Fatalf("both instances should be summarised, got %d rows", len(rows))
	}
	if a.Total != 2 || b.Total != 1 {
		t.Fatalf("totals: a=%d b=%d, want 2 and 1", a.Total, b.Total)
	}
	if !equalInts(a.Ports, []int{8080}) || !equalInts(b.Ports, []int{8080}) {
		t.Fatalf("ports: a=%#v b=%#v", a.Ports, b.Ports)
	}
	if !equalInts(a.Collide, []int{8080}) || !equalInts(b.Collide, []int{8080}) {
		t.Fatalf("the shared 8080 must be flagged: a=%#v b=%#v", a.Collide, b.Collide)
	}
	if a.Label == "" || b.Label == "" || a.Label == b.Label {
		t.Fatalf("labels must be present and distinct: %q / %q", a.Label, b.Label)
	}
}

// Scripts and agents consume this shape, so every field the human listing shows
// has to be there too — especially the collision, which is the one fact a script
// cannot infer on its own.
func TestInstancesJSON(t *testing.T) {
	rows := []instanceSummary{{
		State:   InstanceState{PID: 4510, Config: "/srv/a/stacker.yml", Addr: "127.0.0.1:5", Mode: "serve"},
		Label:   "a",
		Ports:   []int{8080},
		Up:      1,
		Total:   3,
		Collide: []int{8080},
	}}
	out := instancesJSON(rows)
	if len(out) != 1 {
		t.Fatalf("instancesJSON returned %d rows, want 1", len(out))
	}
	row := out[0]
	for key, want := range map[string]any{
		"label":  "a",
		"config": "/srv/a/stacker.yml",
		"pid":    4510,
		"mode":   "serve",
		"addr":   "127.0.0.1:5",
		"up":     1,
		"total":  3,
	} {
		if got := row[key]; got != want {
			t.Fatalf("json[%q] = %v, want %v", key, got, want)
		}
	}
	if ports, ok := row["ports"].([]int); !ok || !equalInts(ports, []int{8080}) {
		t.Fatalf("json[ports] = %v", row["ports"])
	}
	if col, ok := row["port_collisions"].([]int); !ok || !equalInts(col, []int{8080}) {
		t.Fatalf("json[port_collisions] = %v", row["port_collisions"])
	}
}

// A supervisor whose mode was never written predates the field; it is a session.
func TestInstancesJSONDefaultsMode(t *testing.T) {
	out := instancesJSON([]instanceSummary{{State: InstanceState{PID: 1}, Label: "x"}})
	if out[0]["mode"] != "session" {
		t.Fatalf("mode = %v, want session", out[0]["mode"])
	}
}

// Scripts iterate ports/collisions; null forces null-checks that [] does not.
func TestInstancesJSONEmptySlicesNotNull(t *testing.T) {
	out := instancesJSON([]instanceSummary{{
		State: InstanceState{PID: 1, Config: "/a.yml"},
		Label: "a",
	}})
	ports, ok := out[0]["ports"].([]int)
	if !ok || ports == nil {
		t.Fatalf("ports = %#v, want non-nil []int", out[0]["ports"])
	}
	col, ok := out[0]["port_collisions"].([]int)
	if !ok || col == nil {
		t.Fatalf("port_collisions = %#v, want non-nil []int", out[0]["port_collisions"])
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, _, err := instanceID(path)
	if err != nil {
		t.Fatalf("instanceID(%s): %v", path, err)
	}
	return abs
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
