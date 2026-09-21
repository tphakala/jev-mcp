package jev

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// countingServer serves status/body and counts how many requests it received.
func countingServer(t *testing.T, status int, body string) (srvURL string, calls *atomic.Int32) {
	t.Helper()
	var c atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		c.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &c
}

// twoProviderClient wires a primary and secondary provider with time removed and
// no in-place retries, so each provider is tried exactly once.
func twoProviderClient(t *testing.T, primaryURL, secondaryURL string) *Client {
	t.Helper()
	c, err := New([]Provider{
		{Name: ProviderTypeSafe, BaseURL: primaryURL, Path: SystemOnePath, APIKey: "k1"},
		{Name: ProviderOpenRouter, BaseURL: secondaryURL, Path: SystemOnePath, APIKey: "k2"},
	}, WithMaxRetries(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	configureDeterministic(c)
	return c
}

func TestFallbackOnOverload(t *testing.T) {
	t.Parallel()

	primary := serveStatus(t, statusOverloaded, "")
	secondary := serveStatus(t, http.StatusOK, okBody)
	c := twoProviderClient(t, primary, secondary)

	res, err := c.Evaluate(t.Context(), sampleRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.ProviderName != ProviderOpenRouter {
		t.Errorf("ProviderName = %q, want the fallback openrouter", res.ProviderName)
	}
	if res.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2 (one per provider)", res.Attempts)
	}
}

func TestFallbackOnUnauthorized(t *testing.T) {
	t.Parallel()

	primary := serveStatus(t, http.StatusUnauthorized, "")
	secondary := serveStatus(t, http.StatusOK, okBody)
	c := twoProviderClient(t, primary, secondary)

	res, err := c.Evaluate(t.Context(), sampleRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.ProviderName != ProviderOpenRouter {
		t.Errorf("ProviderName = %q, want openrouter after a primary 401", res.ProviderName)
	}
}

func TestNoFallbackOnInvalidRequest(t *testing.T) {
	t.Parallel()

	primary := serveStatus(t, http.StatusUnprocessableEntity, "")
	secondaryURL, secondaryCalls := countingServer(t, http.StatusOK, okBody)
	c := twoProviderClient(t, primary, secondaryURL)

	_, err := c.Evaluate(t.Context(), sampleRequest())
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if got := secondaryCalls.Load(); got != 0 {
		t.Errorf("secondary called %d times, want 0: a rejected request must not fall back", got)
	}
}

func TestFallbackExhaustedJoinsErrors(t *testing.T) {
	t.Parallel()

	primary := serveStatus(t, statusOverloaded, "")
	secondary := serveStatus(t, http.StatusInternalServerError, "")
	c := twoProviderClient(t, primary, secondary)

	_, err := c.Evaluate(t.Context(), sampleRequest())
	if !errors.Is(err, ErrOverloaded) {
		t.Errorf("error %v does not wrap the primary ErrOverloaded", err)
	}
	if !errors.Is(err, ErrServer) {
		t.Errorf("error %v does not wrap the secondary ErrServer", err)
	}
}
