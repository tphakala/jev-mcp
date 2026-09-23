package mcptools

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tphakala/jev-mcp/internal/jev"
)

// HTTP server timeouts.
const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 5 * time.Second
)

const logMsgShutdown = "http shutdown"

// ServeStdio serves the tools over newline-delimited JSON-RPC on r and w until
// the client disconnects or ctx is cancelled. w is the protocol stream, so
// callers must keep every log line off it. Cancelling ctx also cancels any
// evaluation in flight, so shutdown does not wait out the call budget.
//
// The transport closes r when the session ends; w is left open. A read blocked
// on r does not have to return for the session to end: the SDK stops waiting
// on it (go-sdk v1.8.0 mcp/transport.go ioConn.Read selects on its closed
// channel) and leaves that read to the process exit.
func ServeStdio(ctx context.Context, d Deps, r io.ReadCloser, w io.Writer) error {
	t := &mcp.IOTransport{Reader: r, Writer: nopWriteCloser{w}}
	return NewServer(withStop(ctx, d)).Run(ctx, t)
}

// nopWriteCloser adds a no-op Close to an io.Writer, so the transport never
// closes the process's stdout.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// evaluatorFunc adapts a function to the Evaluator interface.
type evaluatorFunc func(context.Context, jev.Request) (*jev.Result, error)

func (f evaluatorFunc) Evaluate(ctx context.Context, req jev.Request) (*jev.Result, error) {
	return f(ctx, req)
}

// withStop returns d with its client wrapped so every evaluation is also
// cancelled when ctx is. Over stdio the SDK detaches handler contexts from the
// serve context (go-sdk v1.8.0 internal/jsonrpc2/conn.go NewConnection wraps it
// in notDone unless cancellation propagation is on), so without this a
// shutdown waits for an in-flight evaluation to finish.
func withStop(ctx context.Context, d Deps) Deps {
	if d.Client == nil {
		return d
	}
	inner := d.Client
	d.Client = evaluatorFunc(func(callCtx context.Context, req jev.Request) (*jev.Result, error) {
		callCtx, cancel := context.WithCancel(callCtx)
		defer cancel()
		unregister := context.AfterFunc(ctx, cancel)
		defer unregister()
		return inner.Evaluate(callCtx, req)
	})
	return d
}

// HTTPHandler returns an http.Handler that serves the tools over the
// Streamable HTTP transport. One server backs every session, which the SDK
// allows (go-sdk v1.8.0 mcp/streamable.go NewStreamableHTTPHandler doc), so
// the tool schemas are built once rather than per request.
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
	srv := NewServer(d)
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, &mcp.StreamableHTTPOptions{Logger: d.Logger})
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

// ServeHTTP runs the Streamable HTTP server on addr until ctx is cancelled. A
// non-empty token requires Authorization: Bearer <token> on every request.
//
// Every request runs under ctx, so cancelling it ends open event streams and
// in-flight evaluations; the server then shuts down, waiting up to
// shutdownTimeout for handlers to return.
func ServeHTTP(ctx context.Context, d Deps, addr, token string) error {
	logger := d.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	// A derived context lets the shutdown goroutine exit when ListenAndServe
	// returns on its own (a bind failure, for example).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := &http.Server{
		Addr:              addr,
		Handler:           HTTPHandler(withStop(ctx, d), token),
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
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
