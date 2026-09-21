package jev

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrValidation is returned by [Validate] for a request that the client
	// rejects before any network call. It is wrapped with the offending question
	// name (or a request-level detail) using %w, so callers match it with
	// errors.Is and still see the specifics in the message.
	ErrValidation = errors.New("jev: invalid request")

	// ErrMalformedResponse is returned when a response cannot be decoded, for
	// example an answer whose value is not a JSON object. Callers match it with
	// errors.Is. The client does not fall back to another provider on it: the
	// request was accepted, and the wire codec is identical across providers, so
	// a body the codec cannot decode is most likely a codec defect both providers
	// would hit rather than one provider misbehaving.
	ErrMalformedResponse = errors.New("jev: malformed response")

	// ErrUnauthorized is the sentinel for HTTP 401 and 403: the API key is
	// missing, wrong, or lacks access.
	ErrUnauthorized = errors.New("jev: unauthorized")

	// ErrInvalidRequest is the sentinel for a request the server rejects (400,
	// 413, 422). The same request will fail on every provider, so the client
	// does not fall back on it.
	ErrInvalidRequest = errors.New("jev: request rejected")

	// ErrRateLimited is the sentinel for HTTP 429.
	ErrRateLimited = errors.New("jev: rate limited")

	// ErrOverloaded is the sentinel for a provider signalling overload (529, 503).
	ErrOverloaded = errors.New("jev: service overloaded")

	// ErrServer is the sentinel for a server-side failure (5xx other than 503).
	ErrServer = errors.New("jev: server error")

	// ErrTransport is the sentinel for a failure below the HTTP status: a dial,
	// TLS, or read error, or a per-request timeout. HTTP 408 maps here too.
	ErrTransport = errors.New("jev: transport failure")

	// ErrResponseTooLarge is returned when a response body exceeds
	// [MaxResponseBytes].
	ErrResponseTooLarge = errors.New("jev: response exceeds size cap")

	// ErrNoProvider is returned by [New] when it is given no providers to call.
	ErrNoProvider = errors.New("jev: no provider configured")
)

// APIError is a non-success HTTP response from a provider. It wraps a sentinel
// (matched with errors.Is) and carries the provider name, status, a best-effort
// message from the body, the provider's request id, and any Retry-After the
// server asked for. A transport failure before any response has a zero Status.
type APIError struct {
	Provider   string
	Status     int
	Message    string
	RequestID  string
	RetryAfter time.Duration
	Sentinel   error
}

// Error renders the sentinel, the provider and status, the request id when the
// provider returned one, and the best-effort body message.
func (e *APIError) Error() string {
	var b strings.Builder
	if e.Sentinel != nil {
		b.WriteString(e.Sentinel.Error())
	}
	b.WriteString(" (provider ")
	b.WriteString(e.Provider)
	if e.Status > 0 {
		b.WriteString(", status ")
		b.WriteString(strconv.Itoa(e.Status))
	}
	if e.RequestID != "" {
		b.WriteString(", request ")
		b.WriteString(e.RequestID)
	}
	b.WriteByte(')')
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// Unwrap returns the sentinel so errors.Is matches the API error against it.
func (e *APIError) Unwrap() error { return e.Sentinel }
