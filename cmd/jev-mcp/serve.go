package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/tphakala/jev-mcp/internal/config"
	"github.com/tphakala/jev-mcp/internal/jev"
	"github.com/tphakala/jev-mcp/internal/mcptools"
)

// Log messages are constants; variable data goes in attributes.
const (
	logMsgServeStdio     = "serving over stdio"
	logMsgServeHTTP      = "serving Streamable HTTP"
	logMsgAuthFlagUnused = "-http-token has no effect without -http"
	logMsgHTTPNoAuth     = "HTTP mode is unauthenticated: any local process can call the tools"
)

// flagHTTPToken is the -http-token flag name, shared by its definition and
// the check that tells an explicit empty value from an omitted flag.
const flagHTTPToken = "http-token"

// serveOptions are the parsed serve flags.
type serveOptions struct {
	httpAddr     string
	httpToken    string
	httpTokenSet bool
	logLevel     slog.Level
	logJSON      bool
}

// serveCommand resolves the configuration, builds the Jev client, and serves
// the MCP tools over stdio (the default) or loopback Streamable HTTP until ctx
// is cancelled or, for stdio, the client disconnects. getenv supplies the
// environment and lookup resolves an -http host name, so tests can drive both.
func serveCommand(ctx context.Context, opts serveOptions, getenv func(string) string, lookup func(string) ([]string, error), stdin io.ReadCloser, stdout, stderr io.Writer) int {
	logger := newLogger(stderr, opts.logLevel, opts.logJSON)

	cfg, err := config.Resolve(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\nrun 'jev-mcp doctor' to check the configuration\n", err)
		return exitError
	}
	providers, err := config.Select(&cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\nrun 'jev-mcp doctor' to check the configuration\n", err)
		return exitError
	}
	client, err := jev.New(providers,
		jev.WithBudget(cfg.Timeout),
		jev.WithMaxRetries(cfg.MaxRetries),
		jev.WithLogger(logger))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	deps := mcptools.Deps{Client: client, DefaultModel: cfg.DefaultModel, Logger: logger}

	if opts.httpAddr == "" {
		if opts.httpTokenSet {
			logger.Warn(logMsgAuthFlagUnused)
		}
		logger.Info(logMsgServeStdio, slog.Int("providers", len(providers)))
		if err := mcptools.ServeStdio(ctx, deps, stdin, stdout); err != nil && !isShutdown(err) {
			_, _ = fmt.Fprintln(stderr, "error: stdio serve:", err)
			return exitError
		}
		return exitOK
	}

	if err := checkLoopbackAddrResolved(opts.httpAddr, lookup); err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	// The token is normalized here, where HTTP mode uses it, rather than in
	// config.Resolve: stdio never reads it, and a flag overrides the
	// environment even when the environment's value is blank.
	rawToken, tokenSource := cfg.HTTPToken, config.EnvHTTPToken
	if opts.httpTokenSet {
		rawToken, tokenSource = opts.httpToken, "-"+flagHTTPToken
	}
	token, err := config.NormalizeHTTPToken(rawToken)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s: %v\n", tokenSource, err)
		return exitError
	}
	if token == "" {
		// A warning, not only auth=false on the info line below, so an
		// unauthenticated server stays visible at -log-level warn, including when
		// "-http-token $TOK" expanded an unset variable to an empty value.
		logger.Warn(logMsgHTTPNoAuth, slog.String("addr", opts.httpAddr))
	}
	logger.Info(logMsgServeHTTP,
		slog.String("addr", opts.httpAddr),
		slog.Bool("auth", token != ""),
		slog.Int("providers", len(providers)))
	if err := mcptools.ServeHTTP(ctx, deps, opts.httpAddr, token); err != nil {
		_, _ = fmt.Fprintln(stderr, "error: http serve:", err)
		return exitError
	}
	return exitOK
}

// isShutdown reports whether a stdio serve error is an ordinary end of
// session. A cancelled context (SIGTERM) returns context.Canceled. A client
// that closes its end of the pipe normally ends the session with a nil error;
// io.EOF is accepted as well so a transport that reports the hang-up that way
// is not treated as a failure.
func isShutdown(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, context.Canceled)
}

// newLogger builds the process logger on stderr. stdout carries the stdio
// JSON-RPC stream, so no log line may reach it.
func newLogger(w io.Writer, level slog.Level, asJSON bool) *slog.Logger {
	hopts := &slog.HandlerOptions{Level: level}
	if asJSON {
		return slog.New(slog.NewJSONHandler(w, hopts))
	}
	return slog.New(slog.NewTextHandler(w, hopts))
}

// registerServeFlags adds the serve flags to fs and returns a function that
// reports the parsed options once fs has been parsed.
func registerServeFlags(fs *flag.FlagSet) func() serveOptions {
	var opts serveOptions
	fs.StringVar(&opts.httpAddr, "http", "", "serve Streamable HTTP on this loopback address (e.g. 127.0.0.1:8765) instead of stdio")
	fs.StringVar(&opts.httpToken, flagHTTPToken, "", "require Authorization: Bearer <token> in HTTP mode (overrides JEV_MCP_HTTP_TOKEN; surrounding spaces, tabs, and line breaks are trimmed; an empty value forces unauthenticated)")
	fs.TextVar(&opts.logLevel, "log-level", slog.LevelInfo, "log level: debug, info, warn, or error")
	fs.BoolVar(&opts.logJSON, "log-json", false, "write logs as JSON instead of text")
	return func() serveOptions {
		// An explicit -http-token, even an empty one, overrides the environment;
		// flag.Visit reports only the flags present on the command line, which
		// is how "-http-token ''" (force unauthenticated) differs from omission.
		fs.Visit(func(f *flag.Flag) {
			if f.Name == flagHTTPToken {
				opts.httpTokenSet = true
			}
		})
		return opts
	}
}

// checkLoopbackAddrResolved rejects an HTTP bind address that is not loopback.
// The bearer token is optional, so a non-loopback bind could expose an
// unauthenticated server; this refuses instead of relying on the docs. A
// literal IP is checked directly; a host name is resolved with lookup and every
// address must be loopback, so a hosts-file remap of "localhost" to a routable
// address is caught.
//
// net.Listen resolves a host name again at bind time, so the answer can change
// between this check and the bind: a hosts-file edit needs root, but a name
// served by DNS is under the DNS server's control. The check guards against
// an accidental non-loopback bind; for a guarantee, pass a loopback IP
// literal, which is never resolved.
func checkLoopbackAddrResolved(addr string, lookup func(string) ([]string, error)) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr // no port; treat the whole value as the host
	}
	// A bracketed IPv6 literal without a port keeps its brackets after the
	// fallback above; strip them so net.ParseIP recognises it.
	if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
		host = host[1 : len(host)-1]
	}
	if host == "" {
		return fmt.Errorf("http address %q binds all interfaces; give a loopback host such as 127.0.0.1:8765", addr)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("http address %q is not loopback; use localhost, 127.0.0.1, or ::1", addr)
	}
	addrs, err := lookup(host)
	if err != nil {
		return fmt.Errorf("resolve http host %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("http host %q resolves to no addresses; give a loopback host", host)
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("http host %q resolves to non-loopback address %s; give a loopback host such as 127.0.0.1:8765", host, a)
		}
	}
	return nil
}
