package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	jevmcp "github.com/tphakala/jev-mcp"
)

const (
	// MaxResponseBytes caps a decoded response body. The read is bounded to
	// MaxResponseBytes+1 bytes; anything larger is rejected with
	// [ErrResponseTooLarge], so a body cannot be read unbounded into memory.
	// Local guard.
	MaxResponseBytes = 4 << 20

	// perRequestTimeout bounds a single HTTP attempt. The overall call budget
	// ([WithBudget]) spans every provider, retry, and fallback; this per-request
	// timeout only ends one hung attempt.
	perRequestTimeout = 10 * time.Second

	// defaultCallBudget is the whole-call timeout when [WithBudget] is not given.
	defaultCallBudget = 30 * time.Second

	// maxMessageBytes bounds the best-effort error message taken from a body.
	maxMessageBytes = 512

	contentTypeJSON = "application/json"
	userAgentPrefix = "jev-mcp/"

	// statusOverloaded is the non-standard 529 "site overloaded" some providers
	// return; it has no net/http constant.
	statusOverloaded = 529

	logMsgFallback   = "jev fallback"
	logMsgAuthFailed = "jev auth failed"
)

// Client calls the Jev Decisions API. It tries its providers in order under one
// call budget, retries a provider on transient failures with jittered backoff,
// and falls back to the next provider when the first cannot answer. It holds no
// mutable state and is safe for concurrent use.
type Client struct {
	http             *http.Client
	providers        []Provider
	policy           retryPolicy
	budget           time.Duration
	maxResponseBytes int
	sleep            func(context.Context, time.Duration) error
	rand             func() float64
	now              func() time.Time
	log              *slog.Logger
	userAgent        string
}

// Option configures a [Client].
type Option func(*Client)

// Result is a successful evaluation plus the metadata of the call that produced
// it: which provider answered, how long the winning provider's call took, and
// how many HTTP attempts were made across retries and fallback.
type Result struct {
	Response

	// ProviderName is the provider that answered (after any fallback).
	ProviderName string
	// Latency is the wall-clock time of the winning provider's call, including
	// its own retries but not time spent on providers that failed before it.
	Latency time.Duration
	// Attempts is the total number of HTTP attempts made across every provider
	// tried, including retries and fallback; 1 is the normal case.
	Attempts int
}

// WithHTTPClient sets the underlying HTTP client. Its Timeout should stay unset:
// the context governs deadlines. A nil client is ignored.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithMaxRetries sets the per-provider retry count. A negative value is ignored.
func WithMaxRetries(n int) Option {
	return func(c *Client) {
		if n >= 0 {
			c.policy.MaxRetries = n
		}
	}
}

// WithBudget sets the overall call budget spanning all providers, retries, and
// fallback. A non-positive value is ignored.
func WithBudget(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.budget = d
		}
	}
}

// WithLogger sets the structured logger. A nil logger is ignored.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.log = l
		}
	}
}

// WithUserAgent overrides the User-Agent header. An empty value is ignored.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// New builds a client that tries providers in the given order. It returns
// [ErrNoProvider] when providers is empty.
func New(providers []Provider, opts ...Option) (*Client, error) {
	if len(providers) == 0 {
		return nil, ErrNoProvider
	}
	// Clone the slice and each provider's Headers map so a caller mutating its
	// own copy cannot race a concurrent Evaluate (setHeaders ranges Headers).
	providers = slices.Clone(providers)
	for i := range providers {
		providers[i].Headers = maps.Clone(providers[i].Headers)
	}
	c := &Client{
		http:             &http.Client{},
		providers:        providers,
		policy:           defaultRetryPolicy(),
		budget:           defaultCallBudget,
		maxResponseBytes: MaxResponseBytes,
		sleep:            sleepCtx,
		rand:             rand.Float64,
		now:              time.Now,
		log:              slog.New(slog.DiscardHandler),
		userAgent:        userAgentPrefix + jevmcp.Version,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Evaluate validates req and runs it against the providers in order under the
// call budget. It returns the first successful [Result]. When a provider fails
// with a fallback-eligible error (rate limit, overload, server, transport, or
// unauthorized) the next provider is tried; a rejected request or a cancelled
// context stops immediately. When every provider fails the returned error joins
// each provider's error, so errors.Is matches any of them.
func (c *Client) Evaluate(ctx context.Context, req Request) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := Validate(req); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.budget)
	defer cancel()

	errs := make([]error, 0, len(c.providers))
	total := 0
	for i := range c.providers {
		p := &c.providers[i]
		res, attempts, err := c.callProvider(ctx, p, req)
		total += attempts
		if err == nil {
			res.Attempts = total
			return res, nil
		}
		errs = append(errs, err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if errors.Is(err, ErrUnauthorized) {
			c.log.Warn(logMsgAuthFailed, slog.String("provider", p.Name))
		}
		if i == len(c.providers)-1 || !shouldFallback(err) {
			break
		}
		c.log.Info(logMsgFallback, slog.String("from", p.Name), slog.String("error", err.Error()))
	}
	return nil, errors.Join(errs...)
}

// callProvider runs one provider's retry loop and returns the result, the
// number of HTTP attempts it made (whether it succeeded or not), and the final
// error. A cancelled context returns the context error so Evaluate stops.
func (c *Client) callProvider(ctx context.Context, p *Provider, req Request) (*Result, int, error) {
	endpoint, err := p.Endpoint()
	if err != nil {
		return nil, 0, &APIError{Provider: p.Name, Sentinel: ErrTransport, Message: err.Error()}
	}
	call := req
	if p.ModelID != nil {
		call.Model = p.ModelID(req.Model)
	}
	body, err := json.Marshal(call)
	if err != nil {
		return nil, 0, fmt.Errorf("jev: marshal request: %w", err)
	}

	start := c.now()
	var (
		lastErr    error
		retryAfter time.Duration
	)
	maxAttempts := c.policy.MaxRetries + 1
	for attempt := range maxAttempts {
		if attempt > 0 {
			if err := c.sleep(ctx, c.policy.delay(attempt-1, retryAfter, c.rand)); err != nil {
				return nil, attempt, err
			}
		}
		resp, err := c.doRequest(ctx, endpoint, p, body)
		if err == nil {
			res := &Result{Response: *resp, ProviderName: p.Name, Latency: c.now().Sub(start)}
			return res, attempt + 1, nil
		}
		lastErr = err
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, attempt + 1, ctxErr
		}
		if !isRetryable(err) {
			return nil, attempt + 1, err
		}
		retryAfter = apiErrorRetryAfter(err)
	}
	return nil, maxAttempts, lastErr
}

// doRequest performs one HTTP attempt bounded by perRequestTimeout, and maps the
// outcome to a *Response or an error. A cancellation of the parent (budget)
// context surfaces as the context error; every other failure is an *APIError.
func (c *Client) doRequest(ctx context.Context, endpoint string, p *Provider, body []byte) (*Response, error) {
	reqCtx, cancel := context.WithTimeout(ctx, perRequestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &APIError{Provider: p.Name, Sentinel: ErrTransport, Message: err.Error()}
	}
	c.setHeaders(httpReq, p)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &APIError{Provider: p.Name, Sentinel: ErrTransport, Message: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.maxResponseBytes)+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &APIError{
			Provider:  p.Name,
			Status:    resp.StatusCode,
			Sentinel:  ErrTransport,
			Message:   err.Error(),
			RequestID: headerRequestID(resp.Header),
		}
	}
	if len(data) > c.maxResponseBytes {
		// A non-200 body over the cap must still classify by status, or a
		// retryable/fallback-eligible error (429, 503/529, 5xx) would be masked
		// as ErrResponseTooLarge and skip retry and fallback. The diagnostic body
		// is bounded here and truncated again by extractMessage.
		if resp.StatusCode != http.StatusOK {
			return nil, c.newAPIError(p, resp, data[:c.maxResponseBytes])
		}
		return nil, &APIError{
			Provider:  p.Name,
			Status:    resp.StatusCode,
			Sentinel:  ErrResponseTooLarge,
			RequestID: headerRequestID(resp.Header),
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.newAPIError(p, resp, data)
	}
	return decodeResponse(p, resp, data)
}

// setHeaders applies the bearer, content negotiation, user agent, and any
// provider-specific extra headers.
func (c *Client) setHeaders(req *http.Request, p *Provider) {
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("Accept", contentTypeJSON)
	req.Header.Set("User-Agent", c.userAgent)
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
}

// newAPIError builds the APIError for a non-200 response.
func (c *Client) newAPIError(p *Provider, resp *http.Response, body []byte) error {
	return &APIError{
		Provider:   p.Name,
		Status:     resp.StatusCode,
		Sentinel:   statusSentinel(resp.StatusCode),
		Message:    extractMessage(body),
		RequestID:  requestID(resp, body),
		RetryAfter: parseRetryAfter(resp.Header, c.now()),
	}
}

// decodeResponse decodes a 200 body into a Response, turning a decode failure
// into an ErrMalformedResponse APIError.
func decodeResponse(p *Provider, resp *http.Response, data []byte) (*Response, error) {
	var out Response
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &APIError{
			Provider:  p.Name,
			Status:    resp.StatusCode,
			Sentinel:  ErrMalformedResponse,
			Message:   err.Error(),
			RequestID: requestID(resp, data),
		}
	}
	return &out, nil
}

// statusSentinel maps an HTTP status to the sentinel that classifies it.
func statusSentinel(code int) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return ErrInvalidRequest
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusServiceUnavailable, statusOverloaded:
		return ErrOverloaded
	case http.StatusRequestTimeout:
		return ErrTransport
	default:
		if code >= http.StatusInternalServerError {
			return ErrServer
		}
		return ErrInvalidRequest
	}
}

// isRetryable reports whether the same provider should be retried after err.
func isRetryable(err error) bool {
	return errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrOverloaded) ||
		errors.Is(err, ErrServer) ||
		errors.Is(err, ErrTransport)
}

// shouldFallback reports whether the next provider should be tried after err. A
// wrong key on the primary (ErrUnauthorized) should not make the secondary
// useless, so it is fallback-eligible even though it is not retryable in place.
func shouldFallback(err error) bool {
	return isRetryable(err) || errors.Is(err, ErrUnauthorized)
}

// apiErrorRetryAfter returns the Retry-After an APIError carried, or 0.
func apiErrorRetryAfter(err error) time.Duration {
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		return apiErr.RetryAfter
	}
	return 0
}

// extractMessage pulls a human message from an error body from the "error",
// "detail", and "message" fields in turn, each either a string or an object
// with a message, skipping one that is empty once cleaned, then falls back to
// the raw body.
// TypeSafe reports errors as {"detail":{"error_type":..,"message":..}}
// (MEASURED against api.typesafe.ai on 2026-09-23 for a 400 and a 401); the
// error_type is kept as a prefix. Every result goes through [CleanText].
func extractMessage(body []byte) string {
	var env struct {
		Error   json.RawMessage `json:"error"`
		Detail  json.RawMessage `json:"detail"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &env); err == nil {
		for _, raw := range []json.RawMessage{env.Error, env.Detail, env.Message} {
			if m := fieldMessage(raw); m != "" {
				return m
			}
		}
	}
	return cleanBytes(body)
}

// fieldMessage reads an "error", "detail", or "message" field that may be an
// object with a message or a bare string, and returns it cleaned. An object's
// error_type, when it is a string that is not blank once cleaned, is prefixed
// ("api_usage_error: Unknown model") so the provider's classification stays
// visible. Any other shape, or a message that is empty once cleaned, yields "".
func fieldMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		Message   string          `json:"message"`
		ErrorType json.RawMessage `json:"error_type"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		msg := CleanText(obj.Message)
		if msg == "" {
			return ""
		}
		var errType string
		if json.Unmarshal(obj.ErrorType, &errType) == nil {
			if t := CleanText(errType); t != "" {
				return strings.TrimSpace(truncateMessage([]byte(t + ": " + msg)))
			}
		}
		return msg
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return CleanText(s)
	}
	return ""
}

// CleanText makes provider-supplied text safe to print: at most 512 bytes,
// valid UTF-8, and free of characters that can move the cursor or reorder the
// line on a terminal. It is exported for callers that print other
// provider-supplied values, such as a response's model or id.
func CleanText(s string) string {
	return cleanBytes([]byte(s))
}

// cleanBytes implements [CleanText]. Leading whitespace and control
// characters are dropped first, so padding cannot push the text out of the
// cap. Then the text is cut to maxMessageBytes, each run of invalid UTF-8
// becomes one U+FFFD, and every control (Cc), bidirectional control (the
// overrides, embeddings, isolates, and marks that reorder a line), line
// separator (Zl), and paragraph separator (Zp) character becomes a space.
// Other format characters, such as the zero-width joiners that Persian, Indic
// scripts, and emoji sequences depend on, are kept. Replacing invalid bytes with the three-byte
// U+FFFD can grow the text past the cap, so it is cut again, and the result is
// trimmed. The input is cut before it is sanitized, so the work after the
// leading trim is bounded by the cap, not by the input size.
func cleanBytes(b []byte) string {
	b = bytes.TrimLeftFunc(b, isUnsafeOrSpace)
	s := strings.ToValidUTF8(truncateMessage(b), "\uFFFD")
	s = strings.Map(func(r rune) rune {
		if isUnsafe(r) {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(truncateMessage([]byte(s)))
}

// isUnsafe reports whether r is a character [CleanText] replaces.
func isUnsafe(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Bidi_Control, unicode.Zl, unicode.Zp)
}

// isUnsafeOrSpace reports whether r is dropped from the start of a text.
func isUnsafeOrSpace(r rune) bool {
	return unicode.IsSpace(r) || isUnsafe(r)
}

// truncateMessage returns the first maxMessageBytes of body as a string. It
// converts only the needed prefix, not the whole (potentially large) body. It
// backs off at most utf8.UTFMax-1 bytes to the start of a rune, so valid UTF-8
// is never cut inside a rune; when no rune starts within that distance the
// bytes there are invalid anyway and the cut stays at maxMessageBytes.
func truncateMessage(body []byte) string {
	if len(body) <= maxMessageBytes {
		return string(body)
	}
	for end := maxMessageBytes; end > maxMessageBytes-utf8.UTFMax; end-- {
		if utf8.RuneStart(body[end]) {
			return string(body[:end])
		}
	}
	return string(body[:maxMessageBytes])
}

// requestID prefers a request id from the response headers and falls back to a
// top-level "id" in the body (OpenRouter reports its id there).
func requestID(resp *http.Response, body []byte) string {
	if id := headerRequestID(resp.Header); id != "" {
		return id
	}
	return bodyRequestID(body)
}

// headerRequestID reads the TypeSafe or OpenRouter request-id header, cleaned
// by [CleanText]: net/http rejects ASCII control bytes in a header value but
// passes bytes from 0x80 up, which may encode C1 controls or invalid UTF-8.
func headerRequestID(h http.Header) string {
	if id := h.Get("X-Typesafe-Request-Id"); id != "" {
		return CleanText(id)
	}
	return CleanText(h.Get("X-Request-ID"))
}

// bodyRequestID reads a top-level "id" from a JSON body, if present, cleaned
// by [CleanText].
func bodyRequestID(body []byte) string {
	var env struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &env); err == nil {
		return CleanText(env.ID)
	}
	return ""
}

// sleepCtx waits for d or until ctx is done, returning ctx.Err() if the context
// ends first (or is already done for a non-positive d).
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
