package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

const okBody = `{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.42}},"usage":{"input_tokens":5,"output_tokens":2}}`

// sampleRequest is a minimal valid request (a single noul question) so the tests
// exercise the transport, not validation.
func sampleRequest() Request {
	return Request{
		Model: "jev-latest",
		State: json.RawMessage(`"hi"`),
		Questions: map[string]Question{
			"q": {Type: TypeNoul, Instructions: json.RawMessage(`"decide"`)},
		},
	}
}

// configureDeterministic removes real time from a client: backoff never sleeps,
// the clock is frozen, and the jitter factor is exactly 1.
func configureDeterministic(c *Client) {
	c.sleep = func(context.Context, time.Duration) error { return nil }
	c.now = func() time.Time { return time.Unix(0, 0) }
	c.rand = func() float64 { return 0.5 }
}

// newTestClient builds a single-provider client aimed at srvURL, with time
// removed.
func newTestClient(t *testing.T, srvURL string, opts ...Option) *Client {
	t.Helper()
	p := Provider{Name: ProviderTypeSafe, BaseURL: srvURL, Path: SystemOnePath, APIKey: "secret-key"}
	c, err := New([]Provider{p}, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	configureDeterministic(c)
	return c
}

// serveStatus starts a server that always returns status and body, registered
// for cleanup.
func serveStatus(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestNewNoProviders(t *testing.T) {
	t.Parallel()
	if _, err := New(nil); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("New(nil) error = %v, want ErrNoProvider", err)
	}
}

func TestEvaluateRequestShapeAndSuccess(t *testing.T) {
	t.Parallel()

	var (
		gotUA   string
		gotBody []byte
		req     recordedRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req.method, req.path = r.Method, r.URL.Path
		req.auth = r.Header.Get("Authorization")
		req.contentType = r.Header.Get("Content-Type")
		req.accept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, okBody)
	}))
	t.Cleanup(srv.Close)

	p := Provider{
		Name: ProviderTypeSafe, BaseURL: srv.URL, Path: SystemOnePath, APIKey: "secret-key",
		ModelID: func(string) string { return "jev-1.13.0" },
	}
	c, err := New([]Provider{p})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.now = func() time.Time { return time.Unix(0, 0) }

	res, err := c.Evaluate(t.Context(), sampleRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	var sent Request
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}

	// Grouped string equality keeps the branch count (and this test's cyclomatic
	// complexity) low.
	checks := []struct{ name, got, want string }{
		{"method", req.method, http.MethodPost},
		{"path", req.path, SystemOnePath},
		{"authorization", req.auth, "Bearer secret-key"},
		{"content-type", req.contentType, contentTypeJSON},
		{"accept", req.accept, contentTypeJSON},
		{"sent model", sent.Model, "jev-1.13.0"}, // ModelID normalisation applied
		{"result provider", res.ProviderName, ProviderTypeSafe},
		{"result model", res.Model, "jev-1.13.0"},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %q, want %q", ch.name, ch.got, ch.want)
		}
	}

	if !strings.HasPrefix(gotUA, userAgentPrefix) {
		t.Errorf("User-Agent = %q, want prefix %q", gotUA, userAgentPrefix)
	}
	if res.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", res.Attempts)
	}
	if a := res.Answers["q"]; a.Noul == nil || *a.Noul != 0.42 {
		t.Errorf("answer q = %+v, want noul 0.42", a)
	}
}

// recordedRequest captures the request fields the shape test asserts.
type recordedRequest struct {
	method, path, auth, contentType, accept string
}

func TestEvaluateStatusMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusUnauthorized, `{"error":{"message":"bad key"}}`, ErrUnauthorized},
		{http.StatusForbidden, "", ErrUnauthorized},
		{http.StatusBadRequest, "", ErrInvalidRequest},
		{http.StatusUnprocessableEntity, `{"detail":"nope"}`, ErrInvalidRequest},
		{http.StatusRequestEntityTooLarge, "", ErrInvalidRequest},
		{http.StatusTooManyRequests, "", ErrRateLimited},
		{http.StatusServiceUnavailable, "", ErrOverloaded},
		{statusOverloaded, "", ErrOverloaded},
		{http.StatusInternalServerError, "", ErrServer},
		{http.StatusRequestTimeout, "", ErrTransport},
		// An unmapped sub-500 status falls to the default: a rejected request.
		{http.StatusNotFound, "", ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			t.Parallel()
			c := newTestClient(t, serveStatus(t, tc.status, tc.body), WithMaxRetries(0))
			_, err := c.Evaluate(t.Context(), sampleRequest())
			if !errors.Is(err, tc.want) {
				t.Fatalf("status %d: error %v, want wrapping %v", tc.status, err, tc.want)
			}
		})
	}
}

func TestEvaluateAPIErrorDetails(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-ID", "rid-9")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, WithMaxRetries(0))
	_, err := c.Evaluate(t.Context(), sampleRequest())
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		t.Fatalf("error %v is not an *APIError", err)
	}
	if apiErr.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want 401", apiErr.Status)
	}
	if apiErr.Message != "invalid api key" {
		t.Errorf("Message = %q, want invalid api key", apiErr.Message)
	}
	if apiErr.RequestID != "rid-9" {
		t.Errorf("RequestID = %q, want rid-9", apiErr.RequestID)
	}
}

func TestEvaluateRetriesStopAtMax(t *testing.T) {
	t.Parallel()

	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, WithMaxRetries(2))
	_, err := c.Evaluate(t.Context(), sampleRequest())
	if !errors.Is(err, ErrServer) {
		t.Fatalf("error = %v, want ErrServer", err)
	}
	if got := count.Load(); got != 3 {
		t.Errorf("request count = %d, want 3 (1 initial + 2 retries)", got)
	}
}

func TestEvaluateRetryAfterHonoured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setHeader func(http.Header)
		want      time.Duration
	}{
		{"seconds", func(h http.Header) { h.Set("Retry-After", "1") }, time.Second},
		{"milliseconds", func(h http.Header) { h.Set("Retry-After-Ms", "250") }, 250 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if n.Add(1) == 1 {
					tt.setHeader(w.Header())
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				_, _ = io.WriteString(w, okBody)
			}))
			t.Cleanup(srv.Close)

			var delays []time.Duration
			c := newTestClient(t, srv.URL, WithMaxRetries(2))
			c.sleep = func(_ context.Context, d time.Duration) error {
				delays = append(delays, d)
				return nil
			}
			res, err := c.Evaluate(t.Context(), sampleRequest())
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if len(delays) != 1 {
				t.Fatalf("sleeps = %d, want 1", len(delays))
			}
			if delays[0] != tt.want {
				t.Errorf("backoff delay = %v, want the server's %v", delays[0], tt.want)
			}
			if res.Attempts != 2 {
				t.Errorf("Attempts = %d, want 2", res.Attempts)
			}
		})
	}
}

func TestEvaluateResponseCap(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, serveStatus(t, http.StatusOK, strings.Repeat("a", 100)))
	c.maxResponseBytes = 16
	_, err := c.Evaluate(t.Context(), sampleRequest())
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
}

func TestEvaluateMalformedResponse(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, serveStatus(t, http.StatusOK, "{"))
	_, err := c.Evaluate(t.Context(), sampleRequest())
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
}

func TestEvaluateContextCancelDuringBackoff(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, serveStatus(t, http.StatusTooManyRequests, ""), WithMaxRetries(3))
	ctx, cancel := context.WithCancel(t.Context())
	c.sleep = func(sctx context.Context, _ time.Duration) error {
		cancel()
		return sctx.Err()
	}
	_, err := c.Evaluate(ctx, sampleRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestEvaluateBackoffSleepErrorStops(t *testing.T) {
	t.Parallel()

	// A sleep failure between attempts must stop the retry loop and surface, so
	// no further request is made. Using a distinct (non-ctx) sleep error pins the
	// early-return on the sleep error specifically, not the downstream ctx guard.
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	sleepErr := errors.New("sleep failed")
	c := newTestClient(t, srv.URL, WithMaxRetries(3))
	c.sleep = func(context.Context, time.Duration) error { return sleepErr }

	_, err := c.Evaluate(t.Context(), sampleRequest())
	if !errors.Is(err, sleepErr) {
		t.Fatalf("error = %v, want the sleep error to surface", err)
	}
	if got := count.Load(); got != 1 {
		t.Errorf("request count = %d, want 1 (the loop stops at the failed backoff sleep)", got)
	}
}

func TestEvaluateTypeSafeRequestID(t *testing.T) {
	t.Parallel()

	// When both request-id headers are present the TypeSafe one wins.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Typesafe-Request-Id", "ts-7")
		w.Header().Set("X-Request-ID", "or-9")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, WithMaxRetries(0))
	_, err := c.Evaluate(t.Context(), sampleRequest())
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		t.Fatalf("error %v is not an *APIError", err)
	}
	if apiErr.RequestID != "ts-7" {
		t.Errorf("RequestID = %q, want ts-7 (the TypeSafe header takes precedence)", apiErr.RequestID)
	}
}

func TestEvaluateTransportFailure(t *testing.T) {
	t.Parallel()

	// A server that is closed before the call leaves nothing listening, so the
	// dial fails: the transport-error path (no HTTP status) must yield
	// ErrTransport with a zero Status.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := newTestClient(t, url, WithMaxRetries(0))
	_, err := c.Evaluate(t.Context(), sampleRequest())
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("error = %v, want ErrTransport", err)
	}
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != 0 {
		t.Errorf("APIError = %+v, want a transport error with Status 0", apiErr)
	}
}

func TestSleepCtx(t *testing.T) {
	t.Parallel()

	// A non-positive duration returns immediately with the context's status,
	// which is nil for a live context.
	if err := sleepCtx(t.Context(), 0); err != nil {
		t.Errorf("sleepCtx(live ctx, 0) = %v, want nil", err)
	}
	// An already-cancelled context returns its error even for a long duration,
	// without waiting.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx(cancelled ctx, 1h) = %v, want context.Canceled", err)
	}
	// A short positive duration on a live context completes and returns nil.
	if err := sleepCtx(t.Context(), time.Millisecond); err != nil {
		t.Errorf("sleepCtx(live ctx, 1ms) = %v, want nil", err)
	}
}

func TestTruncateMessage(t *testing.T) {
	t.Parallel()

	if got := truncateMessage([]byte("hello")); got != "hello" {
		t.Errorf("truncateMessage(short) = %q, want hello", got)
	}
	// A body longer than the cap is bounded, and a cut that lands inside a
	// multibyte rune must back off so the result stays valid UTF-8. "€" is 3
	// bytes, and maxMessageBytes is not a multiple of 3, so the naive cut splits
	// a rune.
	body := bytes.Repeat([]byte("€"), maxMessageBytes)
	got := truncateMessage(body)
	if len(got) > maxMessageBytes {
		t.Errorf("truncateMessage length = %d, want <= %d", len(got), maxMessageBytes)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncateMessage result is not valid UTF-8: %q", got)
	}
}

func TestEvaluateValidationErrorSkipsNetwork(t *testing.T) {
	t.Parallel()

	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called.Store(true)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	_, err := c.Evaluate(t.Context(), Request{}) // no state: a validation error
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
	if called.Load() {
		t.Error("server was called for a request that fails validation")
	}
}
