package mcptools

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTP server timeouts.
const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 5 * time.Second
)

const logMsgShutdown = "http shutdown"

// ServeStdio serves the tools over newline-delimited JSON-RPC on r and w until
// the client disconnects or ctx is cancelled. w is the protocol stream, so
// callers must keep every log line off it. r is closed when the session ends,
// which is what unblocks a pending read on cancellation; w is left open.
func ServeStdio(ctx context.Context, d Deps, r io.ReadCloser, w io.Writer) error {
	t := &mcp.IOTransport{Reader: r, Writer: nopWriteCloser{w}}
	return NewServer(d).Run(ctx, t)
}

// nopWriteCloser adds a no-op Close to an io.Writer, so the transport never
// closes the process's stdout.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// HTTPHandler returns an http.Handler that serves the tools over the
// Streamable HTTP transport.
//
// The handler is wrapped with cross-origin protection: a cross-origin browser
// POST (signalled by Sec-Fetch-Site or a mismatched Origin) is rejected with
// 403, while a request with neither header, as non-browser MCP clients send,
// passes through.
//
// When token is non-empty, a bearer-token check runs in front of that: a
// request without a matching Authorization: Bearer <token> header gets 401. An
// empty token leaves HTTP mode unauthenticated.
func HTTPHandler(d Deps, token string) http.Handler {
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return NewServer(d)
	}, nil)
	return withBearerAuth(token, http.NewCrossOriginProtection().Handler(streamable))
}

// withBearerAuth wraps h to require Authorization: Bearer <token>. An empty
// token disables the check and returns h unchanged.
//
// The auth-scheme is matched case-insensitively (RFC 7235). The token is
// compared by its SHA-256 digest: subtle.ConstantTimeCompare returns early
// when the lengths differ, which would leak the expected token's length
// through timing, and hashing both sides to 32 bytes removes that.
func withBearerAuth(token string, h http.Handler) http.Handler {
	if token == "" {
		return h
	}
	wantHash := sha256.Sum256([]byte(token))
	const prefix = "Bearer "
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
			unauthorized(w)
			return
		}
		gotHash := sha256.Sum256([]byte(auth[len(prefix):]))
		if subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) != 1 {
			unauthorized(w)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// ServeHTTP runs the Streamable HTTP server on addr until ctx is cancelled,
// then shuts down gracefully so in-flight responses can drain. A non-empty
// token requires Authorization: Bearer <token> on every request.
func ServeHTTP(ctx context.Context, d Deps, addr, token string) error {
	logger := d.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           HTTPHandler(d, token),
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	// A derived context lets the shutdown goroutine exit when ListenAndServe
	// returns on its own (a bind failure, for example).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutCtx, c := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer c()
		if err := srv.Shutdown(shutCtx); err != nil {
			logger.Warn(logMsgShutdown, slog.String("error", err.Error()))
		}
	}()
	err := srv.ListenAndServe()
	cancel()
	<-done
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
