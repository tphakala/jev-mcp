package jev

import (
	"math"
	"net/http"
	"testing"
	"time"
)

func TestRetryPolicyExponential(t *testing.T) {
	t.Parallel()

	p := retryPolicy{MaxRetries: 5, BaseDelay: 500 * time.Millisecond, MaxDelay: 5 * time.Second, Jitter: 0}
	cases := map[int]time.Duration{
		0: 500 * time.Millisecond,
		1: time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		4: 5 * time.Second, // 8s capped at 5s
		5: 5 * time.Second,
	}
	for attempt, want := range cases {
		// rand nil disables jitter, so delay equals the raw exponential value.
		if got := p.delay(attempt, 0, nil); got != want {
			t.Errorf("delay(attempt=%d) = %v, want %v", attempt, got, want)
		}
	}
}

func TestRetryPolicyRetryAfterWins(t *testing.T) {
	t.Parallel()

	p := defaultRetryPolicy()
	if got := p.delay(0, 2*time.Second, nil); got != 2*time.Second {
		t.Errorf("delay with Retry-After 2s = %v, want 2s", got)
	}
	// A hostile Retry-After is capped at retryAfterCapFactor*MaxDelay.
	wantCap := p.MaxDelay * retryAfterCapFactor
	if got := p.delay(0, time.Hour, nil); got != wantCap {
		t.Errorf("delay with a huge Retry-After = %v, want the cap %v", got, wantCap)
	}
}

func TestRetryPolicyJitterBounds(t *testing.T) {
	t.Parallel()

	p := retryPolicy{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: 10 * time.Second, Jitter: 0.25}
	// rand=0 gives the low end (1 - jitter); rand=1 gives the high end (1 + jitter).
	if got := p.delay(0, 0, func() float64 { return 0 }); got != 750*time.Millisecond {
		t.Errorf("low-jitter delay = %v, want 750ms", got)
	}
	if got := p.delay(0, 0, func() float64 { return 1 }); got != 1250*time.Millisecond {
		t.Errorf("high-jitter delay = %v, want 1250ms", got)
	}
	// A jitter fraction above 1 with rand=0 drives the factor negative; the
	// delay is clamped to 0 rather than returned as a negative duration.
	neg := retryPolicy{BaseDelay: time.Second, MaxDelay: 10 * time.Second, Jitter: 2}
	if got := neg.delay(0, 0, func() float64 { return 0 }); got != 0 {
		t.Errorf("negative-jitter delay = %v, want it clamped to 0", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Unix(1000, 0)
	future := now.Add(5 * time.Second).UTC().Format(http.TimeFormat)
	past := now.Add(-5 * time.Second).UTC().Format(http.TimeFormat)

	tests := []struct {
		name    string
		headers map[string]string
		want    time.Duration
	}{
		{"milliseconds", map[string]string{retryAfterMsHeader: "250"}, 250 * time.Millisecond},
		{"ms wins over seconds", map[string]string{retryAfterMsHeader: "100", retryAfterHeader: "5"}, 100 * time.Millisecond},
		{"ms zero falls through", map[string]string{retryAfterMsHeader: "0", retryAfterHeader: "2"}, 2 * time.Second},
		{"seconds", map[string]string{retryAfterHeader: "2"}, 2 * time.Second},
		// A huge value must clamp to the max duration, not overflow to negative.
		{"seconds overflow clamped", map[string]string{retryAfterHeader: "99999999999"}, time.Duration(math.MaxInt64)},
		{"seconds zero", map[string]string{retryAfterHeader: "0"}, 0},
		{"seconds negative", map[string]string{retryAfterHeader: "-3"}, 0},
		{"http-date future", map[string]string{retryAfterHeader: future}, 5 * time.Second},
		{"http-date past", map[string]string{retryAfterHeader: past}, 0},
		{"garbage", map[string]string{retryAfterHeader: "soon"}, 0},
		{"empty", map[string]string{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			if got := parseRetryAfter(h, now); got != tt.want {
				t.Errorf("parseRetryAfter(%v) = %v, want %v", tt.headers, got, tt.want)
			}
		})
	}
}
