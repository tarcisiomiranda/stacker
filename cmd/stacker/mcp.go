package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpProcessInput struct {
	Name string `json:"name" jsonschema:"Exact process or standalone task name from stacker_list."`
}

type mcpLogsInput struct {
	Name  string `json:"name" jsonschema:"Exact process or standalone task name from stacker_list."`
	Since *int   `json:"since,omitempty" jsonschema:"Absolute log index. Pass next from the previous call to continue."`
	Limit *int   `json:"limit,omitempty" jsonschema:"Maximum lines per page (1 to 500). Defaults to the last 200 lines."`
}

type mcpProcessList struct {
	Host      string        `json:"host,omitempty"`
	Config    string        `json:"config"`
	Processes []ProcessInfo `json:"processes"`
}

type mcpProcessResult struct {
	Process ProcessInfo `json:"process"`
}

func newMCPServer(configPath string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "stacker", Version: resolveVersion()}, &mcp.ServerOptions{
		Instructions: fmt.Sprintf("Manage the Stacker supervisor for %q. Use stacker_list to discover the configured names.", configPath),
	})
	addSupervisorTool(server, configPath, "stacker_list", "List the supervisor host, config path, services, standalone tasks and their status.", true,
		func(ctx context.Context, client *controlClient, _ struct{}) (mcpProcessList, error) {
			host, _ := os.Hostname()
			result := mcpProcessList{Host: host, Config: configPath}
			err := client.request(ctx, http.MethodGet, "/v1/processes", nil, &result)
			return result, err
		})
	for _, action := range []struct{ name, method, description string }{
		{"status", http.MethodGet, "Get the current status of a process."},
		{"start", http.MethodPost, "Start a configured process or run a standalone task. Starting reclaims its configured port."},
		{"stop", http.MethodPost, "Stop the named process."},
		{"restart", http.MethodPost, "Stop and start the named process. Starting reclaims its configured port."},
	} {
		addSupervisorTool(server, configPath, "stacker_"+action.name, action.description, action.method == http.MethodGet,
			func(ctx context.Context, client *controlClient, input mcpProcessInput) (mcpProcessResult, error) {
				if strings.TrimSpace(input.Name) == "" {
					return mcpProcessResult{}, errors.New("name is required")
				}
				if action.method == http.MethodPost && strings.ContainsAny(input.Name, "/%") {
					if _, err := requestMCPProcess(ctx, client, http.MethodGet, input.Name, "status"); err != nil {
						return mcpProcessResult{}, err
					}
				}
				return requestMCPProcess(ctx, client, action.method, input.Name, action.name)
			})
	}
	addSupervisorTool(server, configPath, "stacker_logs", "Read a page of process output. Pass next as since for the following page.", true, mcpProcessLogs)
	return server
}

func requestMCPProcess(ctx context.Context, client *controlClient, method, name, action string) (mcpProcessResult, error) {
	var result mcpProcessResult
	err := client.request(ctx, method, processActionPath(name, action), nil, &result)
	if err == nil && result.Process.Name != name {
		return result, fmt.Errorf("expected process %q, supervisor returned %q. Update the supervisor to use this name", name, result.Process.Name)
	}
	return result, err
}

func addSupervisorTool[In, Out any](server *mcp.Server, configPath, name, description string, readOnly bool, call func(context.Context, *controlClient, In) (Out, error)) {
	tool := &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly}}
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
		var result Out
		state, err := findRunningInstance(configPath)
		if err != nil {
			return nil, result, err
		}
		if state == nil {
			return nil, result, fmt.Errorf("no running Stacker for %q. Check the config path and that the supervisor and MCP use the same OS account and runtime directory", configPath)
		}
		result, err = call(ctx, clientForInstance(state, 0), input)
		return nil, result, err
	})
}

func mcpProcessLogs(ctx context.Context, client *controlClient, input mcpLogsInput) (logsResponse, error) {
	var empty logsResponse
	if strings.TrimSpace(input.Name) == "" {
		return empty, errors.New("name is required")
	}
	opts := logsOptions{name: input.Name, tail: defaultLogTail, since: -1}
	if input.Limit != nil {
		opts.tail = *input.Limit
		if opts.tail < 1 || opts.tail > 500 {
			return empty, errors.New("limit must be between 1 and 500 lines")
		}
	}
	if input.Since != nil {
		opts.since = *input.Since
		if opts.since < 0 {
			return empty, errors.New("since must be a non-negative log index")
		}
	}
	from, err := logStartIndex(ctx, client, opts)
	if err != nil {
		return empty, err
	}
	return fetchLogs(ctx, client, opts.name, from, opts.tail, false)
}

func printMCPHelp(w io.Writer) {
	fmt.Fprint(w, `Usage:
  stacker --config PATH mcp
  stacker --config PATH mcp --no-auth [--listen LOOPBACK:PORT] [--tls-cert CERT.pem --tls-key KEY.pem]
  stacker --config PATH mcp --listen HOST:PORT --tls-cert CERT.pem --tls-key KEY.pem --token-file FILE

The default uses stdin/stdout and can be launched locally or through ssh -T.
--no-auth serves MCP on loopback at http://127.0.0.1:52912/mcp by default.
For remote HTTPS, clients send the token in the Authorization: Bearer header.
Run MCP under the same OS account as the supervisor for PATH.
`)
}

func cliMCP(configPath string, explicit bool, args []string) int {
	var opts mcpHTTPOptions
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.Usage = func() { printMCPHelp(flags.Output()) }
	flags.StringVar(&opts.listen, "listen", "", "MCP listen address")
	flags.BoolVar(&opts.noAuth, "no-auth", false, "Serve MCP on loopback without a token")
	flags.StringVar(&opts.certFile, "tls-cert", "", "PEM certificate chain")
	flags.StringVar(&opts.keyFile, "tls-key", "", "PEM private key")
	flags.StringVar(&opts.tokenFile, "token-file", "", "File containing the bearer token")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if !explicit || strings.TrimSpace(configPath) == "" || flags.NArg() != 0 {
		flags.Usage()
		return 2
	}
	path, err := filepath.Abs(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := newMCPServer(path)
	if flags.NFlag() == 0 {
		err = server.Run(ctx, &mcp.StdioTransport{})
	} else {
		err = serveMCPHTTP(ctx, server, opts)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "MCP:", err)
		if errors.Is(err, errMCPHTTPOptions) {
			return 2
		}
		return 1
	}
	return 0
}
