package jev

import (
	"net/http"
	"strconv"
	"time"
)

const (
	defaultMaxRetries = 2
	defaultBaseDelay  = 500 * time.Millisecond
	defaultMaxDelay   = 5 * time.Second
	defaultJitter     = 0.25

	// backoffFactor is the exponential base: each retry waits twice as long.
	backoffFactor = 2

	// retryAfterCapFactor bounds a single server-sent Retry-After wait, as a
	// multiple of MaxDelay, so one hostile header cannot request an absurdly long
	// sleep. Total call time is bounded separately by the ctx budget, which the
	// backoff sleep respects.
	retryAfterCapFactor = 4

	// jitterSpan turns the fractional Jitter into a symmetric +/- range around 1.
	jitterSpan = 2.0

	// Header names carrying a server's requested wait. Retry-After-Ms is an
	// integer count of milliseconds; Retry-After is integer seconds or an
	// HTTP-date.
	retryAfterMsHeader = "Retry-After-Ms"
	retryAfterHeader   = "Retry-After"
)

// retryPolicy configures the per-provider retry loop.
type retryPolicy struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Jitter     float64
}

// defaultRetryPolicy is the client's default retry policy: two retries, 500ms
// base doubling to a 5s cap, with 25% jitter.
func defaultRetryPolicy() retryPolicy {
	return retryPolicy{
		MaxRetries: defaultMaxRetries,
		BaseDelay:  defaultBaseDelay,
		MaxDelay:   defaultMaxDelay,
		Jitter:     defaultJitter,
	}
}

// delay returns how long to wait before the retry numbered attempt (attempt 0
// is the first retry). A positive retryAfter from the server takes precedence,
// capped at retryAfterCapFactor*MaxDelay. Otherwise the delay is an exponential
// backoff BaseDelay*backoffFactor^attempt capped at MaxDelay, then scaled by
// +/-Jitter using rand, a value in [0,1).
func (p retryPolicy) delay(attempt int, retryAfter time.Duration, rand func() float64) time.Duration {
	if retryAfter > 0 {
		if capped := p.MaxDelay * retryAfterCapFactor; retryAfter > capped {
			return capped
		}
		return retryAfter
	}
	d := p.exponential(attempt)
	if p.Jitter <= 0 || rand == nil {
		return d
	}
	factor := 1 + p.Jitter*(rand()*jitterSpan-1)
	jittered := time.Duration(float64(d) * factor)
	if jittered < 0 {
		return 0
	}
	return jittered
}

// exponential returns the un-jittered backoff for attempt, capped at MaxDelay
// and guarded against overflow.
func (p retryPolicy) exponential(attempt int) time.Duration {
	d := p.BaseDelay
	for range attempt {
		d *= backoffFactor
		if d <= 0 || d >= p.MaxDelay {
			return p.MaxDelay
		}
	}
	return d
}

// parseRetryAfter reads a server's requested wait from the Retry-After-Ms or
// Retry-After header. A valid, positive Retry-After-Ms (integer milliseconds)
// takes precedence; otherwise Retry-After is read as integer seconds or an
// HTTP-date measured against now. It returns 0 when neither header yields a
// positive duration.
func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	if ms := h.Get(retryAfterMsHeader); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	v := h.Get(retryAfterHeader)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
