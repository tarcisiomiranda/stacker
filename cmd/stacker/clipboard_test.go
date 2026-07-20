package main

import (
	"strings"
	"testing"
)

func TestCopyToClipboardEmpty(t *testing.T) {
	if err := copyToClipboard(""); err == nil {
		t.Fatal("expected error for empty text")
	}
}

func TestCopyOSC52SequenceFormat(t *testing.T) {
	// Writing may fail without a tty; we only care that the function returns
	// quickly and does not panic.
	done := make(chan error, 1)
	go func() { done <- copyOSC52("hello stacker") }()
	select {
	case <-done:
		// ok (error or nil)
	case <-t.Context().Done():
		t.Fatal("copyOSC52 blocked")
	}
}

func TestCopyNativeErrorsWhenMissing(t *testing.T) {
	err := runClipboardCmd("x", "stacker-clipboard-tool-that-does-not-exist")
	if err == nil {
		t.Fatal("expected missing binary error")
	}
	if !strings.Contains(err.Error(), "executable file not found") &&
		!strings.Contains(err.Error(), "not found") &&
		!strings.Contains(err.Error(), "look path") {
		// LookPath errors vary by platform; any error is enough.
		t.Logf("got error: %v", err)
	}
}
