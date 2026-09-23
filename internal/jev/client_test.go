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

func TestNewDeepCopiesHeaders(t *testing.T) {
	t.Parallel()

	// New must own its providers, including each Headers map, so a caller
	// mutating its own copy cannot race a concurrent Evaluate.
	hdr := map[string]string{"X-Title": "orig"}
	p := Provider{Name: ProviderTypeSafe, BaseURL: "https://h.test", Path: SystemOnePath, APIKey: "k", Headers: hdr}
	c, err := New([]Provider{p})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hdr["X-Title"] = "mutated" // mutate the caller's map after construction
	if got := c.providers[0].Headers["X-Title"]; got != "orig" {
		t.Errorf("client Headers[X-Title] = %q, want orig: New must deep-copy the map", got)
	}
}

func TestEvaluateOversizedErrorPreservesStatus(t *testing.T) {
	t.Parallel()

	// A retryable status whose error body exceeds the cap must still classify by
	// status (so retry/fallback stay in play), not collapse to ErrResponseTooLarge.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, strings.Repeat("x", 100))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, WithMaxRetries(0))
	c.maxResponseBytes = 16
	_, err := c.Evaluate(t.Context(), sampleRequest())
	if !errors.Is(err, ErrOverloaded) {
		t.Fatalf("error = %v, want ErrOverloaded (status preserved for an oversized error body)", err)
	}
	if errors.Is(err, ErrResponseTooLarge) {
		t.Error("error should not be ErrResponseTooLarge for a non-200 response")
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

func TestExtractMessage(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", maxMessageBytes+100)
	// A Persian word with a zero-width non-joiner, then an emoji ZWJ sequence.
	const joined = "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645 \U0001F468\u200d\U0001F469"
	cases := []struct {
		name, body, want string
	}{
		{"empty", "  ", ""},
		{"error object", `{"error":{"code":401,"message":"bad key"}}`, "bad key"},
		{"error string", `{"error":"bad key"}`, "bad key"},
		// The TypeSafe envelope as it came back live for an unknown model.
		{"typesafe detail object", `{"detail":{"error_type":"api_usage_error","message":"Unknown model: jev-nope"}}`, "api_usage_error: Unknown model: jev-nope"},
		{"blank error_type is not a prefix", `{"detail":{"error_type":" ","message":"m"}}`, "m"},
		{"error_type without a message is skipped", `{"detail":{"error_type":"x"},"message":"m"}`, "m"},
		{"detail string", `{"detail":"nope"}`, "nope"},
		{"message", `{"message":"slow down"}`, "slow down"},
		{"error wins over detail", `{"error":"first","detail":"second"}`, "first"},
		{"detail wins over message", `{"detail":"first","message":"second"}`, "first"},
		{"only whitespace fields fall back to body", `{"error":"   "}`, `{"error":"   "}`},
		{"whitespace error does not hide detail", `{"error":" \t","detail":{"message":"real reason"}}`, "real reason"},
		// A field of an unexpected shape is skipped, not allowed to hide the others.
		{"mistyped message does not hide detail", `{"detail":"d","message":123}`, "d"},
		{"message object", `{"message":{"message":"inner"}}`, "inner"},
		{"exactly the cap is kept whole", `{"message":"` + long[:maxMessageBytes] + `"}`, long[:maxMessageBytes]},
		{"detail array falls back to body", `{"detail":[{"msg":"field required"}]}`, `{"detail":[{"msg":"field required"}]}`},
		{"not json", "upstream timeout", "upstream timeout"},
		{"long field is capped", `{"detail":{"message":"` + long + `"}}`, long[:maxMessageBytes]},
		// The raw-body path at and just over the cap.
		{"raw body at exactly the cap is kept whole", long[:maxMessageBytes], long[:maxMessageBytes]},
		{"raw body one over the cap is cut", long[:maxMessageBytes+1], long[:maxMessageBytes]},
		// JSON-escaped control characters, such as a terminal escape, are decoded
		// by json.Unmarshal and must not survive into the message.
		{"escaped terminal escape in a field", `{"message":"\u001b[31mred\u001b[0m"}`, "[31mred [0m"},
		{"line breaks in a field", `{"message":"one\ntwo\r\nthree"}`, "one two  three"},
		{"C1 control in a field", `{"message":"a\u0085b"}`, "a b"},
		{"raw body control characters", "bad\x1b[2Jgateway\x00", "bad [2Jgateway"},
		{"raw body invalid UTF-8 run becomes one U+FFFD", "bad \xff\xfe gateway", "bad \uFFFD gateway"},
		// A field that is empty once cleaned does not hide the next one.
		{"control-only field falls through", `{"error":"\u0000","detail":{"message":"real reason"}}`, "real reason"},
		{"blank message with a type falls through", `{"detail":{"error_type":"x","message":" "},"message":"m"}`, "m"},
		{"control message with a type falls through", `{"detail":{"error_type":"x","message":"\u0007"},"message":"m"}`, "m"},
		{"non-string error_type is ignored", `{"detail":{"error_type":404,"message":"Not found"}}`, "Not found"},
		{"object error_type is ignored", `{"detail":{"error_type":{"a":1},"message":"Not found"}}`, "Not found"},
		{"error_type blank once cleaned is not a prefix", `{"detail":{"error_type":"\u0007","message":"m"}}`, "m"},
		// Padding cannot push the text out of the cap.
		{"padded field", `{"message":"` + strings.Repeat(" ", 600) + `real"}`, "real"},
		{"raw body padded with NUL", strings.Repeat("\x00", 600) + "real", "real"},
		// Unicode line separators and bidi controls are replaced; joiners stay.
		{"separators and bidi controls", `{"message":"a\u2028b\u202ec\u2066d\u2029e\u200ff"}`, "a b c d e f"},
		{"zero-width joiners kept", `{"message":"` + joined + `"}`, joined},
		{"long error_type is not a prefix", `{"detail":{"error_type":"` + strings.Repeat("t", maxErrorTypeBytes+1) + `","message":"Unknown model"}}`, "Unknown model"},
		{"error_type at the limit is a prefix", `{"detail":{"error_type":"` + strings.Repeat("t", maxErrorTypeBytes) + `","message":"m"}}`, strings.Repeat("t", maxErrorTypeBytes) + ": m"},
		// The back-off stops three bytes before the cap: one valid byte more
		// would be lost if it went further.
		{"back-off limit", strings.Repeat("x", maxMessageBytes-3) + strings.Repeat("\x80", 8), strings.Repeat("x", maxMessageBytes-3) + "\uFFFD"},
		{"prefixed message cut at a space is trimmed", `{"detail":{"error_type":"t","message":"` + strings.Repeat("x", maxMessageBytes-4) + ` y"}}`, "t: " + strings.Repeat("x", maxMessageBytes-4)},
		// Invalid bytes at the cut become U+FFFD instead of eating valid text.
		{"raw body of continuation bytes at the cut", "ab" + strings.Repeat("\x80", 600), "ab\uFFFD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := extractMessage([]byte(tc.body)); got != tc.want {
				t.Errorf("extractMessage(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestCleanTextStaysWithinCap checks that replacing invalid bytes with the
// three-byte U+FFFD cannot push a text past the cap.
func TestCleanTextStaysWithinCap(t *testing.T) {
	t.Parallel()

	got := CleanText(strings.Repeat("\xff\x1b", maxMessageBytes))
	if len(got) > maxMessageBytes {
		t.Errorf("len = %d, want <= %d", len(got), maxMessageBytes)
	}
	if !strings.HasPrefix(got, "\uFFFD") {
		t.Errorf("want the invalid bytes shown as U+FFFD, got %q", got)
	}
}

// FuzzCleanText pins the properties every printed provider value relies on:
// within the cap, valid UTF-8, no character CleanText promises to replace,
// no surrounding whitespace, and stable when cleaned again.
func FuzzCleanText(f *testing.F) {
	for _, seed := range []string{"", "ok", "quota exceeded for key q-1", "\x1b[2J", "\xff\xfe", "a\u2028b\u202ec", strings.Repeat("\u20ac", 300), strings.Repeat(" ", 600) + "x"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := CleanText(in)
		if len(got) > maxMessageBytes {
			t.Fatalf("len = %d, want <= %d", len(got), maxMessageBytes)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("not valid UTF-8: %q", got)
		}
		if strings.ContainsFunc(got, isUnsafe) {
			t.Fatalf("holds a replaced character: %q", got)
		}
		if strings.TrimSpace(got) != got {
			t.Fatalf("surrounding whitespace: %q", got)
		}
		if again := CleanText(got); again != got {
			t.Fatalf("not stable: %q -> %q", got, again)
		}
		// Safe text is left alone: printable ASCII with no surrounding space
		// that fits the cap comes back unchanged.
		if len(in) <= maxMessageBytes && strings.TrimSpace(in) == in && !strings.ContainsFunc(in, func(r rune) bool { return r < ' ' || r > '~' }) && got != in {
			t.Fatalf("printable text changed: %q -> %q", in, got)
		}
	})
}

// TestRequestIDsAreCleaned checks that a request id from a header or a body
// cannot carry a control sequence into APIError.Error.
func TestRequestIDsAreCleaned(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	h.Set("X-Request-ID", "req-\u009b2J")
	if got := headerRequestID(h); got != "req- 2J" {
		t.Errorf("headerRequestID = %q, want the C1 control replaced", got)
	}
	ts := http.Header{}
	ts.Set("X-Typesafe-Request-Id", "ts-\u202eid")
	if got := headerRequestID(ts); got != "ts- id" {
		t.Errorf("headerRequestID(TypeSafe) = %q, want the bidi control replaced", got)
	}
	both := http.Header{}
	both.Set("X-Typesafe-Request-Id", "\u0085")
	both.Set("X-Request-ID", "req-2")
	if got := headerRequestID(both); got != "req-2" {
		t.Errorf("headerRequestID = %q, want the second header when the first cleans to nothing", got)
	}
	if got := bodyRequestID([]byte(`{"id":"req\u001b]0;x\u0007-9"}`)); got != "req ]0;x -9" {
		t.Errorf("bodyRequestID = %q, want the controls replaced", got)
	}
}

// errTransport is a RoundTripper that fails every request with a fixed error.
type errTransport struct{ err error }

func (e errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }

// TestTransportErrorMessageIsCleaned checks that transport error text, which
// can carry server-controlled bytes (a certificate's names in a hostname
// mismatch, for example), is cleaned like a provider message.
func TestTransportErrorMessageIsCleaned(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, "https://example.invalid",
		WithHTTPClient(&http.Client{Transport: errTransport{errors.New("x509: certificate is valid for \x1b[2Jevil\u202e, not example.invalid")}}),
		WithMaxRetries(0))
	_, err := c.Evaluate(t.Context(), sampleRequest())
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		t.Fatalf("err = %v, want an *APIError", err)
	}
	if strings.ContainsFunc(apiErr.Message, isUnsafe) {
		t.Errorf("Message %q holds a character CleanText replaces", apiErr.Message)
	}
	if !strings.Contains(apiErr.Message, "evil") {
		t.Errorf("Message %q lost the text", apiErr.Message)
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
