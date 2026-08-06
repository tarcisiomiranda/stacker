package main

import (
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// portClaim identifies a process in another Stacker instance that holds a port
// this instance is about to free.
type portClaim struct {
	Label   string
	Config  string
	Addr    string
	Process string
	PID     int
}

// findPortOwner looks for an active process in another live instance that
// declares port. selfConfig is excluded: free-port has always handled this
// instance's own processes, and warning about them would be noise.
//
// Returns nil when nobody owns it, which is the ordinary case — a port held by a
// stray npm or an IDE has no Stacker owner and needs no warning.
func findPortOwner(port int, selfConfig string) *portClaim {
	if port <= 0 {
		return nil
	}
	all, err := listRunningInstances()
	if err != nil || len(all) == 0 {
		return nil
	}
	selfAbs := selfConfig
	if abs, _, err := instanceID(selfConfig); err == nil {
		selfAbs = abs
	}

	labels := labelInstances(all)
	for i, st := range all {
		if st.Config == selfAbs {
			continue
		}
		client := &controlClient{
			addr:   st.Addr,
			client: &http.Client{Timeout: 2 * time.Second},
		}
		var resp struct {
			OK        bool          `json:"ok"`
			Processes []ProcessInfo `json:"processes"`
		}
		if err := client.get("/v1/processes", &resp); err != nil {
			continue
		}
		for _, pi := range resp.Processes {
			if pi.Port != port {
				continue
			}
			// Only a process that is actually up can be holding the listener.
			switch pi.Status {
			case string(StatusRunning), string(StatusStarting):
			default:
				continue
			}
			return &portClaim{
				Label:   labels[i],
				Config:  st.Config,
				Addr:    st.Addr,
				Process: pi.Name,
				PID:     pi.PID,
			}
		}
	}
	return nil
}

// claimPortFromOtherInstances announces that this instance is taking a port away
// from another one, on both sides, and reports whether that happened.
//
// The port is taken regardless: free-port keeps a single meaning, "the port will
// be free when this returns". What changes is that the loss becomes auditable —
// the victim's log says why its service died, instead of showing a bare exit.
func claimPortFromOtherInstances(port int, selfConfig string, logf func(string)) bool {
	claim := findPortOwner(port, selfConfig)
	if claim == nil {
		return false
	}
	if logf != nil {
		logf(fmt.Sprintf("[stacker] port %d reclaimed from %s @ %s (pid %d)",
			port, claim.Process, claim.Label, claim.PID))
	}
	reclaimer := selfConfig
	if reclaimer == "" {
		reclaimer = "another stacker"
	}
	noteOtherInstance(claim, fmt.Sprintf(
		"[stacker] %s terminated: port %d reclaimed by %s",
		claim.Process, port, reclaimer))
	return true
}

// freePortAudited frees the port after leaving a trail on any foreign Stacker
// process that declared it. Use this from every free-port path (Start, TUI `f`,
// control plane, web, CLI) so reclaim is never silent.
func freePortAudited(port int, selfConfig string, logf func(string)) ([]int, error) {
	claimPortFromOtherInstances(port, selfConfig, logf)
	return freePort(port)
}

// noteOtherInstance appends a line to the owning process's log in the other
// instance. Best effort by design: failing to notify must never block the start
// that triggered it.
func noteOtherInstance(claim *portClaim, text string) {
	client := &controlClient{
		addr:   claim.Addr,
		client: &http.Client{Timeout: 2 * time.Second},
	}
	body := map[string]string{"text": text}
	var resp struct {
		OK bool `json:"ok"`
	}
	_ = client.post("/v1/processes/"+url.PathEscape(claim.Process)+"/note", body, &resp)
}

// instanceConfigPath is the absolute config path this supervisor owns. A Stacker
// process serves exactly one config, so this is a genuine singleton rather than
// state that needs threading through every Process.
var instanceConfigPath atomic.Value

func setInstanceConfig(path string) { instanceConfigPath.Store(path) }

// currentInstanceConfig is "" before the control plane starts, which simply
// means port-claim detection treats every owner as foreign — harmless, since no
// process can have started yet either.
func currentInstanceConfig() string {
	if v, ok := instanceConfigPath.Load().(string); ok {
		return v
	}
	return ""
}
