package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "stacker-test-cache-")
	if err != nil {
		panic(err)
	}
	if filepath.Clean(filepath.Dir(directory)) != filepath.Clean(os.TempDir()) {
		panic("unexpected test cache directory")
	}
	if err := os.Setenv("XDG_CACHE_HOME", directory); err != nil {
		panic(err)
	}
	if err := os.Setenv("LocalAppData", directory); err != nil {
		panic(err)
	}
	code := m.Run()
	if err := os.RemoveAll(directory); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

func isolateTestCache(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", directory)
	t.Setenv("LocalAppData", directory)
}

func testCommand(command string) string {
	if runtime.GOOS != "windows" {
		return command
	}
	switch command {
	case "true":
		return "exit /b 0"
	case "pwd":
		return "cd"
	case "sleep 30", "sleep 60", "trap 'exit 0' TERM; while :; do sleep 1; done":
		return "powershell -NoProfile -NonInteractive -Command Start-Sleep -Seconds 60"
	}
	return command
}

func testConfigCommands(contents string) string {
	if runtime.GOOS != "windows" {
		return contents
	}
	for _, command := range []string{"true", "sleep 30", "sleep 60", "trap 'exit 0' TERM; while :; do sleep 1; done"} {
		contents = strings.ReplaceAll(contents, "command: \""+command+"\"", "command: "+strconv.Quote(testCommand(command)))
		contents = strings.ReplaceAll(contents, "command: "+command, "command: "+testCommand(command))
	}
	return contents
}

func testWindowsProcessTree(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STACKER_TEST_PROCESS_TREE", "parent")
	t.Setenv("STACKER_TEST_CHILD_PID", filepath.Join(dir, "child.pid"))
	p := NewProcess("tree", ProcessConfig{Command: `"` + os.Args[0] + `" -test.run=^TestProcessTreeHelper$`, Cwd: dir, GracefulTimeout: "500ms"}, 20)
	defer func() {
		if t.Failed() {
			t.Log(p.Logs())
		}
	}()
	if err := p.Start(func() {}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(func() {}) })
	var pid int
	waitFor(t, 5*time.Second, func() bool {
		body, err := os.ReadFile(filepath.Join(dir, "child.pid"))
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(body))
		return err == nil && pid > 0
	})
	if err := p.Stop(func() {}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return true
		}
		defer proc.Release()
		return !processSignalAlive(proc)
	})
}

func TestProcessTreeHelper(t *testing.T) {
	mode := os.Getenv("STACKER_TEST_PROCESS_TREE")
	if mode == "" {
		return
	}
	if mode == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$")
		child.Env = append(os.Environ(), "STACKER_TEST_PROCESS_TREE=child")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("STACKER_TEST_CHILD_PID"), []byte(fmt.Sprint(child.Process.Pid)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(time.Minute)
}
