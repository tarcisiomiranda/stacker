package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// copyToClipboard puts text on the system clipboard.
// Prefer a native tool when available (reliable in iTerm2/Terminal on macOS),
// then fall back to OSC 52 for remote/SSH sessions.
func copyToClipboard(text string) error {
	if text == "" {
		return fmt.Errorf("nothing to copy")
	}
	if err := copyNative(text); err == nil {
		return nil
	}
	if err := copyOSC52(text); err == nil {
		return nil
	}
	return fmt.Errorf("clipboard unavailable (try enabling OSC 52 in the terminal, or install pbcopy/xclip/wl-copy)")
}

func copyNative(text string) error {
	switch runtime.GOOS {
	case "darwin":
		return runClipboardCmd(text, "pbcopy")
	case "windows":
		// PowerShell for Unicode-safe clipboard writes.
		return runClipboardCmd(text, "powershell", "-NoProfile", "-Command",
			"Set-Clipboard -Value ([Console]::In.ReadToEnd())")
	default:
		// Linux and other Unix: Wayland, then X11.
		if err := runClipboardCmd(text, "wl-copy", "--foreground"); err == nil {
			return nil
		}
		// Without --foreground, wl-copy may daemonize; plain form as fallback.
		if err := runClipboardCmd(text, "wl-copy"); err == nil {
			return nil
		}
		if err := runClipboardCmd(text, "xclip", "-selection", "clipboard"); err == nil {
			return nil
		}
		if err := runClipboardCmd(text, "xsel", "--clipboard", "--input"); err == nil {
			return nil
		}
		return fmt.Errorf("no native clipboard tool found")
	}
}

func runClipboardCmd(text string, name string, args ...string) error {
	if _, err := exec.LookPath(name); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s: timed out", name)
		}
		return fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// copyOSC52 asks the terminal emulator to set the clipboard (works over SSH
// when the terminal allows it — e.g. iTerm2 "Applications in terminal may
// access clipboard").
func copyOSC52(text string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	// BEL terminator is widely supported; ST (\x1b\\) as alternate for picky terminals.
	sequences := []string{
		"\x1b]52;c;" + encoded + "\a",
		"\x1b]52;c;" + encoded + "\x1b\\",
	}

	var lastErr error
	for _, sequence := range sequences {
		if err := writeOSC(sequence); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("failed to write OSC 52 sequence")
}

func writeOSC(sequence string) error {
	// Prefer the controlling terminal so the sequence is not swallowed by the
	// Bubble Tea alt-screen pipeline.
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		defer tty.Close()
		_, err = tty.WriteString(sequence)
		return err
	}
	_, err := os.Stderr.WriteString(sequence)
	return err
}
