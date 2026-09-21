package jev

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAPIErrorError(t *testing.T) {
	t.Parallel()

	e := &APIError{
		Provider:   ProviderTypeSafe,
		Status:     http.StatusTooManyRequests,
		Message:    "slow down",
		RequestID:  "rid-1",
		RetryAfter: time.Second,
		Sentinel:   ErrRateLimited,
	}
	got := e.Error()
	for _, want := range []string{ErrRateLimited.Error(), ProviderTypeSafe, "429", "rid-1", "slow down"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}
	// Unwrap keeps errors.Is matching the sentinel through the APIError.
	if !errors.Is(e, ErrRateLimited) {
		t.Error("errors.Is(APIError, ErrRateLimited) = false, want true")
	}
}

func TestAPIErrorErrorOmitsZeroStatus(t *testing.T) {
	t.Parallel()

	// A transport failure before any response has a zero Status, which must be
	// omitted rather than rendered as "status 0".
	e := &APIError{Provider: ProviderOpenRouter, Sentinel: ErrTransport}
	if got := e.Error(); strings.Contains(got, "status") {
		t.Errorf("Error() = %q, want no status for a zero-Status transport error", got)
	}
}

func TestAPIErrorErrorNilSentinel(t *testing.T) {
	t.Parallel()

	// A nil Sentinel must not panic: APIError is exported with an exported
	// Sentinel field, so a zero-value or partially-built value can be formatted.
	e := &APIError{Provider: ProviderTypeSafe, Status: http.StatusInternalServerError}
	got := e.Error() // must not panic
	if !strings.Contains(got, ProviderTypeSafe) || !strings.Contains(got, "500") {
		t.Errorf("Error() = %q, want it to still render provider and status", got)
	}
}
