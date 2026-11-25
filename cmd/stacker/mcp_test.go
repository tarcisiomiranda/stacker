package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("STACKER_TEST_MCP_STDIO") == "1" {
		path, explicit, args := parseArgs(os.Args[slices.Index(os.Args, "--")+1:])
		os.Exit(runCLI(path, explicit, args))
	}
}

type mcpTestBearer struct{ http.RoundTripper }

func (transport mcpTestBearer) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer test-token")
	return transport.RoundTripper.RoundTrip(request)
}

func TestMCPTransports(t *testing.T) {
	for _, mode := range []string{"stdio", "http", "https"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			_, other := startLogInstance(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "stacker.yml")
			var transport mcp.Transport
			switch mode {
			case "stdio":
				cmd := exec.Command(os.Args[0], "-test.run=^TestMCPStdioHelper$", "--", "--config", path, "mcp")
				cmd.Env = append(os.Environ(), "STACKER_TEST_MCP_STDIO=1")
				transport = &mcp.CommandTransport{Command: cmd}
			case "http":
				server := httptest.NewServer(mcpHTTPHandler(newMCPServer(path), nil))
				defer server.Close()
				transport = &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: server.Client()}
			case "https":
				digest := sha256.Sum256([]byte("test-token"))
				server := httptest.NewTLSServer(mcpHTTPHandler(newMCPServer(path), &digest))
				defer server.Close()
				client := &http.Client{Transport: mcpTestBearer{server.Client().Transport}}
				transport = &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: client}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			session, err := client.Connect(ctx, transport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			listed, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, tool := range listed.Tools {
				names = append(names, tool.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, []string{"stacker_list", "stacker_logs", "stacker_restart", "stacker_start", "stacker_status", "stacker_stop"}) {
				t.Fatalf("tools = %v", names)
			}
			call := func(tool string, args any, out any) {
				t.Helper()
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
				if err != nil {
					t.Fatal(err)
				}
				if result.IsError != (out == nil) {
					t.Fatalf("%s(%v): %+v", tool, args, result)
				}
				if out != nil {
					body, err := json.Marshal(result.StructuredContent)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(body, out); err != nil {
						t.Fatal(err)
					}
				}
			}

			call("stacker_list", struct{}{}, nil)
			call("stacker_start", map[string]string{"name": other.Name}, nil)
			if other.Status() != StatusStopped || len(other.Logs()) != 0 {
				t.Fatal("a tool targeted the other supervisor")
			}
			writeConfig(t, dir, `
version: 1
processes:
  "worker /%23 #?":
    command: sleep 60
tasks:
  probe:
    command: echo finished
`)
			cfg, err := loadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			model := newModel(cfg)
			control, err := startControlServer(model, path)
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			defer model.stopAll()
			process := model.processes[0]
			var list mcpProcessList
			call("stacker_list", struct{}{}, &list)
			host, _ := os.Hostname()
			if list.Host != host || list.Config != path || len(list.Processes) != 2 || list.Processes[0].Name != process.Name {
				t.Fatalf("list = %+v", list)
			}
			for i := range 5 {
				process.appendLog(fmt.Sprint(i))
			}
			var logs logsResponse
			call("stacker_logs", map[string]any{"name": process.Name, "limit": 2}, &logs)
			if logs.From != 3 || logs.Next != 5 || !slices.Equal(logs.Lines, []string{"3", "4"}) {
				t.Fatalf("tail = %+v", logs)
			}
			call("stacker_logs", map[string]any{"name": process.Name, "since": 0, "limit": 2}, &logs)
			if logs.From != 0 || logs.Next != 2 || !slices.Equal(logs.Lines, []string{"0", "1"}) {
				t.Fatalf("first page = %+v", logs)
			}
			call("stacker_logs", map[string]any{"name": process.Name, "since": logs.Next, "limit": 2}, &logs)
			if logs.From != 2 || logs.Next != 4 || !slices.Equal(logs.Lines, []string{"2", "3"}) {
				t.Fatalf("second page = %+v", logs)
			}
			call("stacker_logs", map[string]any{"name": process.Name, "limit": 0}, nil)
			call("stacker_logs", map[string]any{"name": process.Name, "since": -1}, nil)
			call("stacker_start", map[string]string{"name": "unknown"}, nil)

			args := mcpProcessInput{Name: process.Name}
			var result mcpProcessResult
			call("stacker_start", args, &result)
			pid := process.PID()
			if result.Process.Name != process.Name || result.Process.Status != "running" || pid == 0 {
				t.Fatalf("start = %+v", result)
			}
			call("stacker_restart", args, &result)
			if result.Process.Status != "running" || process.PID() == pid {
				t.Fatalf("restart = %+v", result)
			}
			call("stacker_stop", args, &result)
			call("stacker_status", args, &result)
			if result.Process.Status != "stopped" || process.PID() != 0 {
				t.Fatalf("status after stop = %+v", result)
			}
			probe := mcpProcessInput{Name: "probe"}
			call("stacker_start", probe, &result)
			waitFor(t, 3*time.Second, func() bool { return model.processByName(probe.Name).Status() == StatusStopped })
			call("stacker_logs", probe, &logs)
			if !slices.Contains(logs.Lines, "[stdout] finished") {
				t.Fatalf("finished task log = %q", logs.Lines)
			}
		})
	}
}

func TestMCPLogCursorAfterEvictionAndLargeLine(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	path, p := startLogInstance(t)
	client, _, err := newControlClient(path)
	if err != nil {
		t.Fatal(err)
	}
	p.setLogLimits(3, defaultMaxLogBytes)
	long := strings.Repeat("z", 66000)
	for _, line := range []string{"evicted", long, "short", "last"} {
		p.appendLog(line)
	}
	from := 0
	args := mcpLogsInput{Name: p.Name, Since: &from}
	page, err := mcpProcessLogs(t.Context(), client, args)
	if err != nil || page.From != 1 || page.Next != 2 || !slices.Equal(page.Lines, []string{long}) {
		t.Fatalf("first page from %d to %d with %d lines, err %v", page.From, page.Next, len(page.Lines), err)
	}
	from = page.Next
	page, err = mcpProcessLogs(t.Context(), client, args)
	if err != nil || page.From != 2 || page.Next != 4 || !slices.Equal(page.Lines, []string{"short", "last"}) {
		t.Fatalf("second page = %+v, err %v", page, err)
	}
}

const mcpTestInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`

func TestMCPHTTPSAccess(t *testing.T) {
	digest := sha256.Sum256([]byte("test-token"))
	handler := mcpHTTPHandler(newMCPServer("stacker.yml"), &digest)
	for _, test := range []struct {
		path, auth, origin string
		want               int
	}{
		{"/mcp", "", "", http.StatusUnauthorized},
		{"/mcp", "Bearer wrong", "", http.StatusUnauthorized},
		{"/mcp", "Bearer test-token", "https://another.example", http.StatusForbidden},
		{"/mcp", "Bearer test-token", "https://stacker.example", http.StatusOK},
		{"/v1/processes", "Bearer test-token", "", http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodPost, "https://stacker.example"+test.path, strings.NewReader(mcpTestInit))
		req.Header.Set("Authorization", test.auth)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if test.origin != "" {
			req.Header.Set("Origin", test.origin)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != test.want {
			t.Errorf("%s origin %q: got %d, want %d: %s", test.path, test.origin, rec.Code, test.want, rec.Body)
		}
		if test.want == http.StatusUnauthorized && !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
			t.Error("missing bearer challenge")
		}
	}
	if got := cliMCP("stacker.yml", true, []string{"--listen", "127.0.0.1:0"}); got != 2 {
		t.Fatalf("listen without credentials returned %d", got)
	}
	if got := cliMCP("stacker.yml", false, nil); got != 2 {
		t.Fatalf("implicit config returned %d", got)
	}
}

func TestMCPLoopbackOptions(t *testing.T) {
	for _, test := range []struct {
		listen, want string
	}{
		{"", "127.0.0.1:52912"},
		{"localhost:0", "127.0.0.1:0"},
		{"127.0.0.2:49322", "127.0.0.2:49322"},
		{"[::1]:52912", "[::1]:52912"},
		{"[::ffff:127.0.0.1]:52912", "127.0.0.1:52912"},
	} {
		got, err := (mcpHTTPOptions{listen: test.listen, noAuth: true}).listenAddress()
		if err != nil || got != test.want {
			t.Errorf("--no-auth --listen %q: got %q, %v, want %q", test.listen, got, err, test.want)
		}
	}
	for _, listen := range []string{"0.0.0.0:52912", "[::]:52912", ":52912", "192.0.2.10:52912", "other.example:52912", "127.0.0.1", "localhost:"} {
		if _, err := (mcpHTTPOptions{listen: listen, noAuth: true}).listenAddress(); err == nil {
			t.Errorf("--no-auth accepted --listen %q", listen)
		}
	}
	for _, opts := range []mcpHTTPOptions{
		{noAuth: true, certFile: "cert.pem"},
		{noAuth: true, keyFile: "key.pem"},
		{noAuth: true, tokenFile: "token"},
		{listen: "192.0.2.10:9443", certFile: "cert.pem", keyFile: "key.pem"},
	} {
		if _, err := opts.listenAddress(); err == nil {
			t.Errorf("accepted incomplete or conflicting options %+v", opts)
		}
	}
}

func TestMCPLocalHTTPAccess(t *testing.T) {
	server := httptest.NewServer(mcpHTTPHandler(newMCPServer("stacker.yml"), nil))
	defer server.Close()
	for _, test := range []struct {
		host, origin, fetchSite string
		want                    int
	}{
		{"", "", "", http.StatusOK},
		{"", server.URL, "same-origin", http.StatusOK},
		{"", "https://other.example", "", http.StatusForbidden},
		{"", strings.Replace(server.URL, "http://", "https://", 1), "", http.StatusForbidden},
		{"", "", "cross-site", http.StatusForbidden},
		{"other.example", "", "", http.StatusForbidden},
	} {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(mcpTestInit))
		if err != nil {
			t.Fatal(err)
		}
		if test.host != "" {
			req.Host = test.host
		}
		if test.origin != "" {
			req.Header.Set("Origin", test.origin)
		}
		if test.fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", test.fetchSite)
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != test.want {
			t.Errorf("host %q, origin %q, fetch-site %q: got %d, want %d", test.host, test.origin, test.fetchSite, resp.StatusCode, test.want)
		}
	}
}
