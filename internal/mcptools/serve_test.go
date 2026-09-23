package mcptools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tphakala/jev-mcp/internal/jev"
)

// postJSON builds a JSON POST bound to the test's context.
func postJSON(t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return req
}

// statusOf sends req and returns the response status.
func statusOf(t *testing.T, req *http.Request) int {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// headerRoundTripper injects a fixed header on every request, so a go-sdk
// client can present a bearer token.
type headerRoundTripper struct {
	key, val string
	base     http.RoundTripper
}

func (h headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set(h.key, h.val)
	return h.base.RoundTrip(req)
}

// connectHTTP connects a go-sdk client to url over Streamable HTTP.
func connectHTTP(t *testing.T, url string, hc *http.Client) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: hc}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestHTTPRejectsCrossOriginPost(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(HTTPHandler(Deps{}, ""))
	t.Cleanup(ts.Close)

	req := postJSON(t, ts.URL, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if got := statusOf(t, req); got != http.StatusForbidden {
		t.Fatalf("cross-origin POST status = %d, want 403", got)
	}
}

func TestHTTPBearerAuth(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(HTTPHandler(Deps{}, "s3cret"))
	t.Cleanup(ts.Close)

	tests := []struct {
		name       string
		authHeader string
		wantStatus int // 0 means "anything but 401"
	}{
		{name: "missing token", authHeader: "", wantStatus: http.StatusUnauthorized},
		{name: "wrong token", authHeader: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", authHeader: "Basic s3cret", wantStatus: http.StatusUnauthorized},
		{name: "token prefix only", authHeader: "Bearer s3cre", wantStatus: http.StatusUnauthorized},
		// RFC 7235: the auth-scheme is case-insensitive.
		{name: "lowercase scheme", authHeader: "bearer s3cret"},
		{name: "correct token", authHeader: "Bearer s3cret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := postJSON(t, ts.URL, `{}`)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			_ = resp.Body.Close()
			switch {
			case tt.wantStatus == 0 && resp.StatusCode == http.StatusUnauthorized:
				t.Fatalf("status = 401, want the request to pass auth")
			case tt.wantStatus != 0 && resp.StatusCode != tt.wantStatus:
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			case tt.wantStatus == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") != "Bearer":
				t.Fatalf("WWW-Authenticate = %q, want Bearer", resp.Header.Get("WWW-Authenticate"))
			}
		})
	}

	t.Run("client with token lists tools", func(t *testing.T) {
		t.Parallel()
		hc := &http.Client{Transport: headerRoundTripper{key: "Authorization", val: "Bearer s3cret", base: http.DefaultTransport}}
		if _, err := connectHTTP(t, ts.URL, hc).ListTools(t.Context(), nil); err != nil {
			t.Fatalf("list tools with valid token: %v", err)
		}
	})
}

// TestHTTPNoTokenSkipsAuth pins the default: with no token configured, a
// request without Authorization reaches the MCP handler.
func TestHTTPNoTokenSkipsAuth(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(HTTPHandler(Deps{}, ""))
	t.Cleanup(ts.Close)
	if got := statusOf(t, postJSON(t, ts.URL, `{}`)); got == http.StatusUnauthorized {
		t.Fatal("no token configured: request must not be rejected with 401")
	}
}

// TestHTTPServeAdvertisesToolAndInstructions checks the discovery surface over
// the real transport: the one tool is listed and the server sends its
// instructions at initialize.
func TestHTTPServeAdvertisesToolAndInstructions(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(HTTPHandler(Deps{}, ""))
	t.Cleanup(ts.Close)
	cs := connectHTTP(t, ts.URL, nil)

	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != toolEvaluate {
		t.Fatalf("tools = %v, want exactly %s", tools.Tools, toolEvaluate)
	}
	init := cs.InitializeResult()
	if init == nil {
		t.Fatal("no InitializeResult")
	}
	for _, want := range []string{toolEvaluate, "confidence", "detail", "secrets"} {
		if !strings.Contains(init.Instructions, want) {
			t.Errorf("server instructions missing %q", want)
		}
	}
}

// TestServeHTTPShutsDownOnCancel starts the real listener, confirms it
// answers, and checks that cancelling the context stops it cleanly.
func TestServeHTTPShutsDownOnCancel(t *testing.T) {
	t.Parallel()

	addr := freeLoopbackAddr(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- ServeHTTP(ctx, Deps{}, addr, "") }()

	url := "http://" + addr
	deadline := time.Now().Add(5 * time.Second)
	for {
		req := postJSON(t, url, `{}`)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeHTTP after cancel = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeHTTP did not return after cancel")
	}
}

func TestServeHTTPBindFailure(t *testing.T) {
	t.Parallel()

	// Hold a port so the server cannot bind it.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	err = ServeHTTP(t.Context(), Deps{}, ln.Addr().String(), "")
	if err == nil {
		t.Fatal("ServeHTTP on a taken port = nil, want a bind error")
	}
}

// freeLoopbackAddr returns a loopback address with a port that was free a
// moment ago.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

// TestServeStdio runs a full session over pipes: initialize, list tools, then
// the client hangs up and ServeStdio returns.
func TestServeStdio(t *testing.T) {
	t.Parallel()

	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- ServeStdio(t.Context(), Deps{}, serverIn, serverOut)
		_ = serverOut.Close()
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools.Tools))
	}
	_ = cs.Close()

	select {
	case err := <-done:
		// The client hanging up ends the session; how the SDK reports that
		// (nil or EOF) is not this test's concern, only that it returns.
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("ServeStdio = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeStdio did not return after the client closed")
	}
}

// TestServeStdioStopsOnCancel checks that cancelling the context ends the
// session even before any client has connected.
func TestServeStdioStopsOnCancel(t *testing.T) {
	t.Parallel()

	serverIn, clientOut := io.Pipe()
	t.Cleanup(func() { _ = clientOut.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- ServeStdio(ctx, Deps{}, serverIn, io.Discard) }()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ServeStdio after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeStdio did not return after cancel")
	}
}

// blockingEvaluator blocks every call until its context ends, reporting the
// start of the call on started and the context error on ended.
type blockingEvaluator struct {
	started chan struct{}
	ended   chan error
}

func newBlockingEvaluator() *blockingEvaluator {
	return &blockingEvaluator{started: make(chan struct{}, 1), ended: make(chan error, 1)}
}

func (b *blockingEvaluator) Evaluate(ctx context.Context, _ jev.Request) (*jev.Result, error) {
	b.started <- struct{}{}
	<-ctx.Done()
	b.ended <- ctx.Err()
	return nil, ctx.Err()
}

// oneNoulCall returns a minimal valid jev_evaluate call. It is built fresh
// for every call because the SDK client records the negotiated protocol
// version in the params' _meta and keeps one already there (go-sdk v1.8.0
// mcp/client.go), so a shared value carries one session's version into
// another.
func oneNoulCall() *mcp.CallToolParams {
	return &mcp.CallToolParams{Name: toolEvaluate, Arguments: map[string]any{
		"state":     "s",
		"questions": []any{map[string]any{"name": "q", "type": "noul", "instructions": "q"}},
	}}
}

// waitFor fails the test if ch does not deliver within d.
func waitFor[T any](t *testing.T, ch <-chan T, d time.Duration, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("timed out after %v waiting for %s", d, what)
		var zero T
		return zero
	}
}

// TestServeStdioCancelsInFlightCall checks that cancelling the serve context
// (SIGTERM) cancels an evaluation in flight, so shutdown does not wait out the
// call budget.
func TestServeStdioCancelsInFlightCall(t *testing.T) {
	t.Parallel()

	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	t.Cleanup(func() { _ = clientOut.Close(); _ = serverOut.Close() })
	eval := newBlockingEvaluator()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- ServeStdio(ctx, Deps{Client: eval}, serverIn, serverOut) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	go func() { _, _ = cs.CallTool(t.Context(), oneNoulCall()) }()
	waitFor(t, eval.started, 5*time.Second, "the evaluation to start")

	cancel()
	if err := waitFor(t, eval.ended, 2*time.Second, "the evaluation to be cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("evaluation context ended with %v, want context.Canceled", err)
	}
	if err := waitFor(t, done, 2*time.Second, "ServeStdio to return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeStdio = %v, want context.Canceled", err)
	}
}

// TestServeHTTPShutdownWithOpenSession checks that cancelling the context
// stops the server promptly while a client holds a session open and a call is
// in flight: the event stream and the evaluation both end, and shutdown does
// not run into its timeout.
func TestServeHTTPShutdownWithOpenSession(t *testing.T) {
	t.Parallel()

	addr := freeLoopbackAddr(t)
	eval := newBlockingEvaluator()
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- ServeHTTP(ctx, Deps{Client: eval, Logger: logger}, addr, "") }()

	var cs *mcp.ClientSession
	deadline := time.Now().Add(5 * time.Second)
	for {
		client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
		var err error
		cs, err = client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: "http://" + addr}, nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = cs.Close() })
	callErr := make(chan error, 1)
	go func() {
		res, err := cs.CallTool(t.Context(), oneNoulCall())
		if err == nil && res.IsError {
			err = fmt.Errorf("tool error: %v", res.Content)
		}
		callErr <- err
	}()
	select {
	case <-eval.started:
	case err := <-callErr:
		t.Fatalf("call returned before the evaluation started: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the evaluation to start")
	}

	start := time.Now()
	cancel()
	if err := waitFor(t, eval.ended, 2*time.Second, "the evaluation to be cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("evaluation context ended with %v, want context.Canceled", err)
	}
	if err := waitFor(t, done, shutdownTimeout+5*time.Second, "ServeHTTP to return"); err != nil {
		t.Fatalf("ServeHTTP = %v, want nil", err)
	}
	if took := time.Since(start); took >= shutdownTimeout {
		t.Errorf("shutdown took %v, want well under the %v timeout", took, shutdownTimeout)
	}
	if strings.Contains(logs.String(), logMsgShutdown) {
		t.Errorf("shutdown logged a failure: %s", logs.String())
	}
}

// syncBuffer is a strings.Builder safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
