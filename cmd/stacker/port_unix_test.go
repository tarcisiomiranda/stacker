//go:build unix

package main

import "testing"

func TestListenersProcfsFindsHolder(t *testing.T) {
	port, stop := startPortHolder(t)
	defer stop()

	pids, err := listenersProcfs(port)
	if err != nil {
		t.Fatalf("listenersProcfs: %v", err)
	}
	if len(pids) == 0 {
		t.Fatal("procfs found no listener pids")
	}
}
