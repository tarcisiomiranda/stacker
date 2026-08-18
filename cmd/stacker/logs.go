package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// defaultLogTail bounds an unqualified `stacker logs <name>`. Process logs are
// capped in memory but can still be tens of thousands of lines, and the common
// caller — a human glancing at a failure, or an agent with a context budget —
// wants the end of the log, not all of it.
const defaultLogTail = 200

// logFollowInterval is the poll period of -f. The control plane returns only
// what is new (from/next), so this is cheap.
const logFollowInterval = 400 * time.Millisecond

type logsOptions struct {
	name       string
	tail       int // lines to show; 0 means everything retained
	since      int // absolute log index to start from; -1 when unset
	follow     bool
	supervisor bool
	jsonOut    bool
}

// parseLogsArgs reads the flags of `stacker logs`. Unknown flags are an error
// rather than a silently ignored argument.
func parseLogsArgs(args []string, jsonOut bool) (logsOptions, error) {
	opts := logsOptions{tail: defaultLogTail, since: -1, jsonOut: jsonOut}
	intValue := func(flag, inline string, i *int) (int, error) {
		raw := inline
		if raw == "" {
			if *i+1 >= len(args) {
				return 0, fmt.Errorf("%s needs a number", flag)
			}
			*i++
			raw = args[*i]
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("%s: %q is not a number", flag, raw)
		}
		return n, nil
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag, inline, _ := strings.Cut(arg, "=")
		switch flag {
		case "-f", "--follow":
			opts.follow = true
		case "--supervisor", "--daemon":
			opts.supervisor = true
		case "--json", "-json":
			// runCLI already strips this globally; accepted here too so the
			// parser stands on its own for direct callers.
			opts.jsonOut = true
		case "-n", "--tail", "--lines":
			n, err := intValue(flag, inline, &i)
			if err != nil {
				return opts, err
			}
			if n < 0 {
				return opts, fmt.Errorf("%s cannot be negative", flag)
			}
			opts.tail = n
		case "--all":
			opts.tail = 0
		case "--since":
			n, err := intValue(flag, inline, &i)
			if err != nil {
				return opts, err
			}
			if n < 0 {
				return opts, fmt.Errorf("--since cannot be negative")
			}
			opts.since = n
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown flag %q for stacker logs", arg)
			}
			if opts.name != "" {
				return opts, fmt.Errorf("unexpected argument %q; logs takes one process name", arg)
			}
			opts.name = arg
		}
	}
	return opts, nil
}

func cliLogs(configPath string, args []string, jsonOut bool) int {
	opts, err := parseLogsArgs(args, jsonOut)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Fprintln(os.Stderr, "usage: stacker [--config path] logs <process> [-n N] [--since IDX] [-f] [--json]")
		fmt.Fprintln(os.Stderr, "       stacker [--config path] logs --supervisor [-n N] [-f]")
		return 2
	}
	if opts.supervisor {
		if opts.name != "" {
			fmt.Fprintln(os.Stderr, "error: --supervisor is the daemon's own log; it takes no process name")
			return 2
		}
		return cliSupervisorLogs(configPath, opts)
	}

	client, _, err := newControlClientFor(configPath, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		// The daemon log survives a supervisor that never came up, and is the
		// only log left to read in that case.
		fmt.Fprintln(os.Stderr, "hint: stacker --config", configPath, "logs --supervisor  # the daemon's own log")
		return 1
	}

	if opts.name == "" {
		fmt.Fprintln(os.Stderr, "error: stacker logs needs a process name")
		if names, err := logProcessNames(client); err == nil && len(names) > 0 {
			fmt.Fprintln(os.Stderr, "available:", strings.Join(names, ", "))
		}
		return 2
	}

	from, err := logStartIndex(client, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	next, code := printLogBatch(client, opts, from)
	if code != 0 || !opts.follow {
		return code
	}
	for {
		time.Sleep(logFollowInterval)
		n, code := printLogBatch(client, opts, next)
		if code != 0 {
			return code
		}
		next = n
	}
}

// logStartIndex resolves where to begin: an explicit --since, the tail window,
// or the very beginning.
func logStartIndex(client *controlClient, opts logsOptions) (int, error) {
	if opts.since >= 0 {
		return opts.since, nil
	}
	if opts.tail <= 0 {
		return 0, nil
	}
	// Ask how long the log is, then request only the last N lines. TailLogs
	// clamps a start index that has already been trimmed, so this is safe.
	resp, err := fetchLogs(client, opts.name, 0, true)
	if err != nil {
		return 0, err
	}
	return max(0, resp.Next-opts.tail), nil
}

type logsResponse struct {
	OK      bool        `json:"ok"`
	Error   string      `json:"error"`
	From    int         `json:"from"`
	Next    int         `json:"next"`
	Lines   []string    `json:"lines"`
	Process ProcessInfo `json:"process"`
}

func fetchLogs(client *controlClient, name string, from int, nolines bool) (logsResponse, error) {
	var resp logsResponse
	path := "/v1/processes/" + url.PathEscape(name) + "/logs?from=" + strconv.Itoa(from)
	if nolines {
		path += "&nolines=1"
	}
	if err := client.get(path, &resp); err != nil {
		if resp.Error != "" {
			return resp, fmt.Errorf("%s", resp.Error)
		}
		return resp, err
	}
	if !resp.OK {
		return resp, fmt.Errorf("%s", orDefault(resp.Error, "log request failed"))
	}
	return resp, nil
}

// printLogBatch writes one batch and returns the index to resume from.
func printLogBatch(client *controlClient, opts logsOptions, from int) (int, int) {
	resp, err := fetchLogs(client, opts.name, from, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return from, 1
	}
	if opts.jsonOut {
		// One object per batch: with -f this is NDJSON, so a reader can
		// consume it incrementally.
		if !opts.follow || len(resp.Lines) > 0 {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"ok":      true,
				"process": opts.name,
				"status":  resp.Process.Status,
				"from":    resp.From,
				"next":    resp.Next,
				"lines":   resp.Lines,
			})
		}
		return resp.Next, 0
	}
	for _, line := range resp.Lines {
		fmt.Println(line)
	}
	return resp.Next, 0
}

func logProcessNames(client *controlClient) ([]string, error) {
	var resp struct {
		OK        bool          `json:"ok"`
		Processes []ProcessInfo `json:"processes"`
	}
	if err := client.get("/v1/processes", &resp); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(resp.Processes))
	for _, p := range resp.Processes {
		names = append(names, p.Name)
	}
	return names, nil
}

// cliSupervisorLogs prints the daemon's own log file. It reads the file
// directly, so it still works when the supervisor died or never started —
// which is exactly when this log matters.
func cliSupervisorLogs(configPath string, opts logsOptions) int {
	path := daemonLogPath(configPath)
	info, err := os.Stat(path)
	if err != nil {
		if opts.jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"ok": false, "supervisor": true, "path": path,
				"error": "no daemon log for this config",
			})
		} else {
			fmt.Fprintf(os.Stderr, "no daemon log for this config: %s\n", path)
			fmt.Fprintln(os.Stderr, "it is written by: stacker --config <path> serve -d")
		}
		return 1
	}

	lines, err := tailFileLines(path, opts.tail)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if opts.jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"ok": true, "supervisor": true, "path": path, "lines": lines,
		})
	} else {
		fmt.Fprintln(os.Stderr, "# "+path)
		for _, line := range lines {
			fmt.Println(line)
		}
	}
	if !opts.follow {
		return 0
	}

	offset := info.Size()
	for {
		time.Sleep(logFollowInterval)
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		size := info.Size()
		if size < offset {
			// Truncated or rotated: start over rather than emit garbage.
			offset = 0
		}
		if size == offset {
			continue
		}
		chunk, err := readFileRange(path, offset, size-offset)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		offset = size
		text := strings.TrimSuffix(string(chunk), "\n")
		if text == "" {
			continue
		}
		if opts.jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"ok": true, "supervisor": true, "path": path,
				"lines": strings.Split(text, "\n"),
			})
			continue
		}
		fmt.Println(text)
	}
}

// tailFileWindow bounds how much of a file the tail reads. The daemon log only
// holds supervisor notices, so this is a guard, not a normal path.
const tailFileWindow = 1 << 20 // 1MB

// tailFileLines returns the last n lines of a file (all of them when n <= 0),
// reading at most the final tailFileWindow bytes.
func tailFileLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	offset := int64(0)
	if size > tailFileWindow {
		offset = size - tailFileWindow
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	if offset > 0 && len(lines) > 0 {
		// The window almost certainly starts mid-line; drop that fragment.
		lines = lines[1:]
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func readFileRange(path string, offset, length int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, length))
}
