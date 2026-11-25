package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultMCPLoopback = "127.0.0.1:52912"

var errMCPHTTPOptions = errors.New("invalid MCP listener options")

type mcpHTTPOptions struct {
	listen, certFile, keyFile, tokenFile string
	noAuth                               bool
}

func (opts mcpHTTPOptions) listenAddress() (string, error) {
	if !opts.noAuth {
		if opts.listen == "" || opts.certFile == "" || opts.keyFile == "" || opts.tokenFile == "" {
			return "", errors.New("MCP HTTPS requires --listen, --tls-cert, --tls-key and --token-file together")
		}
		return opts.listen, nil
	}
	if opts.tokenFile != "" {
		return "", errors.New("--no-auth and --token-file cannot be combined")
	}
	if (opts.certFile == "") != (opts.keyFile == "") {
		return "", errors.New("MCP TLS requires both --tls-cert and --tls-key")
	}
	if opts.listen == "" {
		return defaultMCPLoopback, nil
	}
	host, port, err := net.SplitHostPort(opts.listen)
	if err != nil || port == "" {
		return "", fmt.Errorf("invalid --listen %q, expected HOST:PORT", opts.listen)
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return "", errors.New("--no-auth requires a loopback address such as 127.0.0.1, localhost or [::1]")
	}
	return net.JoinHostPort(ip.Unmap().String(), port), nil
}

func (opts mcpHTTPOptions) tokenDigest() (*[sha256.Size]byte, error) {
	if opts.noAuth {
		return nil, nil
	}
	data, err := os.ReadFile(opts.tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read MCP token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if len(strings.Fields(token)) != 1 {
		return nil, errors.New("MCP token file must contain a single non-empty bearer token")
	}
	digest := sha256.Sum256([]byte(token))
	return &digest, nil
}

func serveMCPHTTP(ctx context.Context, server *mcp.Server, opts mcpHTTPOptions) error {
	addr, err := opts.listenAddress()
	if err != nil {
		return fmt.Errorf("%w: %w", errMCPHTTPOptions, err)
	}
	token, err := opts.tokenDigest()
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler:           mcpHTTPHandler(server, token),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	scheme := "http"
	if opts.certFile != "" {
		cert, err := tls.LoadX509KeyPair(opts.certFile, opts.keyFile)
		if err != nil {
			return fmt.Errorf("load MCP TLS certificate and key: %w", err)
		}
		httpServer.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		scheme = "https"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	stop := context.AfterFunc(ctx, func() { _ = httpServer.Close() })
	defer stop()
	fmt.Fprintf(os.Stderr, "Stacker MCP listening on %s://%s/mcp\n", scheme, listener.Addr())
	if httpServer.TLSConfig != nil {
		err = httpServer.ServeTLS(listener, "", "")
	} else {
		err = httpServer.Serve(listener)
	}
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func mcpHTTPHandler(server *mcp.Server, expected *[sha256.Size]byte) http.Handler {
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		PropagateRequestCancellation: true,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if origins := r.Header.Values("Origin"); len(origins) > 0 {
			scheme, defaultPort := "http://", ":80"
			if r.TLS != nil {
				scheme, defaultPort = "https://", ":443"
			}
			origin := scheme + r.Host
			if len(origins) != 1 || (!strings.EqualFold(origins[0], origin) && !strings.EqualFold(origins[0], strings.TrimSuffix(origin, defaultPort))) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		if expected != nil {
			scheme, credential, ok := strings.Cut(r.Header.Get("Authorization"), " ")
			presented := sha256.Sum256([]byte(credential))
			if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(expected[:], presented[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="Stacker"`)
				http.Error(w, "bearer token required", http.StatusUnauthorized)
				return
			}
		}
		transport.ServeHTTP(w, r)
	})
	if expected == nil {
		return http.NewCrossOriginProtection().Handler(mux)
	}
	return mux
}
