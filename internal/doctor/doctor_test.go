package doctor_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tphakala/jev-mcp/internal/config"
	"github.com/tphakala/jev-mcp/internal/doctor"
	"github.com/tphakala/jev-mcp/internal/jev"
)

func getenvFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// fakeEvaluator records the request it was given and returns a canned result or
// error, so the probe path runs without a network call. The probes run
// concurrently, so the recorded fields are guarded; read them through wasCalled
// and request once Run has returned.
type fakeEvaluator struct {
	res *jev.Result
	err error

	mu     sync.Mutex
	called bool
	gotReq jev.Request
}

func (f *fakeEvaluator) Evaluate(_ context.Context, req jev.Request) (*jev.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	f.gotReq = req
	return f.res, f.err
}

func (f *fakeEvaluator) wasCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.called
}

func (f *fakeEvaluator) request() jev.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotReq
}

func factoryReturning(ev *fakeEvaluator) doctor.ClientFactory {
	return func(_ jev.Provider) (doctor.Evaluator, error) { return ev, nil }
}

func factoryError(err error) doctor.ClientFactory {
	return func(_ jev.Provider) (doctor.Evaluator, error) { return nil, err }
}

func TestRunHappyPath(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	code := doctor.Run(t.Context(), &out,
		getenvFrom(map[string]string{config.EnvTypeSafeKey: "ts-key"}), false, nil)

	if code != 0 {
		t.Errorf("exit code = %d, want 0\n%s", code, out.String())
	}
	got := out.String()
	for _, want := range []string{"[PASS] config", "[PASS] providers", "typesafe_key=set", "openrouter_key=unset"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "] probe ") {
		t.Errorf("probe ran without -probe\n%s", got)
	}
}

func TestRunMissingKeyFailsProviders(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	code := doctor.Run(t.Context(), &out, getenvFrom(map[string]string{}), false, nil)

	if code != 1 {
		t.Errorf("exit code = %d, want 1\n%s", code, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "[PASS] config") {
		t.Errorf("config should still parse\n%s", got)
	}
	if !strings.Contains(got, "[FAIL] providers") {
		t.Errorf("providers should fail on no key\n%s", got)
	}
}

func TestRunParseErrorFailsConfig(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	code := doctor.Run(t.Context(), &out,
		getenvFrom(map[string]string{config.EnvTypeSafeKey: "ts-key", config.EnvTimeout: "nope"}), false, nil)

	if code != 1 {
		t.Errorf("exit code = %d, want 1\n%s", code, out.String())
	}
	if got := out.String(); !strings.Contains(got, "[FAIL] config") {
		t.Errorf("config should fail on bad timeout\n%s", got)
	}
}

func TestRunKeyFormatWarnsOnWhitespaceAndCoversHTTPToken(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	// One credential per format problem, each one the normalizers keep: TypeSafe
	// key a trailing non-breaking space, OpenRouter key a C1 control character
	// (its UTF-8 bytes are legal in a header), HTTP token an embedded quote.
	code := doctor.Run(t.Context(), &out, getenvFrom(map[string]string{
		config.EnvTypeSafeKey:   "ts-key\u00a0",
		config.EnvOpenRouterKey: "or\u0085key",
		config.EnvHTTPToken:     `tok"en`,
	}), false, nil)

	// A WARN does not fail the run.
	if code != 0 {
		t.Errorf("exit code = %d, want 0\n%s", code, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "[WARN] credential format") {
		t.Errorf("expected a credential-format warning\n%s", got)
	}
	// The warning names each affected credential's env var, and never the value.
	for _, name := range []string{config.EnvTypeSafeKey, config.EnvOpenRouterKey, config.EnvHTTPToken} {
		if !strings.Contains(got, name) {
			t.Errorf("warning should name %s\n%s", name, got)
		}
	}
	for _, want := range []string{"surrounding whitespace that is not trimmed", "a control character", "an embedded quote"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning should describe %q\n%s", want, got)
		}
	}
}

// TestRunHTTPTokenNormalized checks that doctor judges the HTTP token as serve
// uses it, after config.NormalizeHTTPToken: a trailing newline is not a format
// problem because serve trims it, and a blank token is one warning that names
// the variable, not a failed run, since stdio mode does not use it.
func TestRunHTTPTokenNormalized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		token    string
		want     []string
		dontWant []string
	}{
		{
			name:     "trailing newline is trimmed",
			token:    "tok\n",
			want:     []string{"[PASS] credential format", "[PASS] http token: set"},
			dontWant: []string{"[WARN]"},
		},
		{
			name:     "blank token warns",
			token:    " \t",
			want:     []string{"[WARN] http token: " + config.EnvHTTPToken + " is set but only whitespace", "[PASS] credential format", "http_token=blank"},
			dontWant: []string{"[FAIL]", "[PASS] http token", "http_token=set"},
		},
		{
			name:     "control character warns",
			token:    "to\x00k",
			want:     []string{"[WARN] http token: " + config.EnvHTTPToken + " has a control character", "would refuse to start", "[PASS] credential format", "http_token=invalid"},
			dontWant: []string{"[FAIL]", "[PASS] http token", "http_token=set"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out strings.Builder
			code := doctor.Run(t.Context(), &out, getenvFrom(map[string]string{
				config.EnvTypeSafeKey: "ts-key",
				config.EnvHTTPToken:   tt.token,
			}), false, nil)
			got := out.String()
			if code != 0 {
				t.Errorf("exit code = %d, want 0\n%s", code, got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("output lacks %q\n%s", w, got)
				}
			}
			for _, w := range tt.dontWant {
				if strings.Contains(got, w) {
					t.Errorf("output contains %q\n%s", w, got)
				}
			}
		})
	}
}

func TestRunDefaultModelWarn(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	code := doctor.Run(t.Context(), &out, getenvFrom(map[string]string{
		config.EnvTypeSafeKey:  "ts-key",
		config.EnvDefaultModel: "gpt-4",
	}), false, nil)

	if code != 0 {
		t.Errorf("exit code = %d, want 0\n%s", code, out.String())
	}
	if got := out.String(); !strings.Contains(got, "[WARN] default model") {
		t.Errorf("expected a default-model warning\n%s", got)
	}
}

func TestRunProbeSuccess(t *testing.T) {
	t.Parallel()

	res := &jev.Result{ProviderName: jev.ProviderTypeSafe, Latency: 200 * time.Millisecond, Attempts: 1}
	res.Model = "jev-1.13.0"
	res.ID = "resp-77" // OpenRouter-style response id; rendered only when present
	res.Usage = jev.Usage{InputTokens: 10, OutputTokens: 3}
	ev := &fakeEvaluator{res: res}

	var out strings.Builder
	code := doctor.Run(t.Context(), &out,
		getenvFrom(map[string]string{config.EnvTypeSafeKey: "ts-key"}), true, factoryReturning(ev))

	if code != 0 {
		t.Errorf("exit code = %d, want 0\n%s", code, out.String())
	}
	if !ev.wasCalled() {
		t.Error("probe did not call the client")
	}
	// The probe request must be valid, or a live probe would fail before the
	// network. This pins the request shape to jev.Validate.
	if err := jev.Validate(ev.request()); err != nil {
		t.Errorf("probe request is not valid: %v", err)
	}
	got := out.String()
	for _, want := range []string{"[PASS] probe typesafe", "model=jev-1.13.0", "tokens=10/3", "id=resp-77"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n%s", want, got)
		}
	}
}

func TestRunProbeClientBuildError(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	code := doctor.Run(t.Context(), &out,
		getenvFrom(map[string]string{config.EnvTypeSafeKey: "ts-key"}), true,
		factoryError(errors.New("boom")))

	if code != 1 {
		t.Errorf("exit code = %d, want 1\n%s", code, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "[FAIL] probe typesafe") || !strings.Contains(got, "building client") {
		t.Errorf("a client-build failure should FAIL the probe with a building-client detail\n%s", got)
	}
}

func TestRunProbeFailureShowsRequestID(t *testing.T) {
	t.Parallel()

	ev := &fakeEvaluator{err: &jev.APIError{
		Provider:  jev.ProviderTypeSafe,
		Status:    401,
		Sentinel:  jev.ErrUnauthorized,
		RequestID: "req-abc123",
		Message:   "invalid key",
	}}

	var out strings.Builder
	code := doctor.Run(t.Context(), &out,
		getenvFrom(map[string]string{config.EnvTypeSafeKey: "ts-key"}), true, factoryReturning(ev))

	if code != 1 {
		t.Errorf("exit code = %d, want 1\n%s", code, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "[FAIL] probe typesafe") {
		t.Errorf("expected a probe failure\n%s", got)
	}
	// The request id must appear exactly once: APIError.Error already renders it,
	// so the detail must not append it a second time.
	if n := strings.Count(got, "req-abc123"); n != 1 {
		t.Errorf("request id should appear exactly once, got %d\n%s", n, got)
	}
}

func TestRunProbeSkippedWhenConfigParseFails(t *testing.T) {
	t.Parallel()

	ev := &fakeEvaluator{}
	var out strings.Builder
	// A valid key (so selection succeeds) but an unparsable timeout: the config
	// check fails, so the probe must not spend a live call on a broken config.
	code := doctor.Run(t.Context(), &out, getenvFrom(map[string]string{
		config.EnvTypeSafeKey: "ts-key",
		config.EnvTimeout:     "nope",
	}), true, factoryReturning(ev))

	if code != 1 {
		t.Errorf("exit code = %d, want 1\n%s", code, out.String())
	}
	if ev.wasCalled() {
		t.Error("probe ran despite a config parse failure")
	}
	if strings.Contains(out.String(), "] probe ") {
		t.Errorf("probe line present despite config parse failure\n%s", out.String())
	}
}

func TestRunProbeSkippedWhenProvidersFail(t *testing.T) {
	t.Parallel()

	ev := &fakeEvaluator{}
	var out strings.Builder
	// auto with no key: providers fails, so the probe must not run.
	code := doctor.Run(t.Context(), &out, getenvFrom(map[string]string{}), true, factoryReturning(ev))

	if code != 1 {
		t.Errorf("exit code = %d, want 1\n%s", code, out.String())
	}
	if ev.wasCalled() {
		t.Error("probe ran even though provider selection failed")
	}
	if strings.Contains(out.String(), "] probe ") {
		t.Errorf("probe line present despite selection failure\n%s", out.String())
	}
}

// TestRunNeverPrintsASecret is the security guard: no credential value may
// appear anywhere in the report, across settings, checks, and a probe.
func TestRunNeverPrintsASecret(t *testing.T) {
	t.Parallel()

	const (
		// A trailing non-breaking space makes the TypeSafe key trip a format
		// warning, so the warning path also handles a secret value; it must
		// still not echo it.
		tsSecret   = "SECRET-typesafe-9f3a\u00a0"
		orSecret   = "SECRET-openrouter-2b7c"
		httpSecret = "SECRET-httptoken-11xz"
	)
	res := &jev.Result{Latency: 10 * time.Millisecond}
	res.Model = "jev-1.13.0"
	ev := &fakeEvaluator{res: res}

	var out strings.Builder
	doctor.Run(t.Context(), &out, getenvFrom(map[string]string{
		config.EnvTypeSafeKey:   tsSecret,
		config.EnvOpenRouterKey: orSecret,
		config.EnvHTTPToken:     httpSecret,
	}), true, factoryReturning(ev))

	got := out.String()
	// Check the raw values and the bare TypeSafe key (the warning path could
	// leak either form).
	for _, secret := range []string{tsSecret, strings.TrimSuffix(tsSecret, "\u00a0"), orSecret, httpSecret} {
		if strings.Contains(got, secret) {
			t.Errorf("report leaked a secret value %q\n%s", secret, got)
		}
	}
}

// TestRunAPIKeyNormalized checks that doctor judges the API keys as Select uses
// them: surrounding line breaks are trimmed and not reported, while a blank or
// control-character key is reported, as a failed provider selection when the
// key is used and as a warning even when it is not.
func TestRunAPIKeyNormalized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      map[string]string
		wantCode int
		want     []string
		dontWant []string
	}{
		{
			name:     "trailing newline is trimmed",
			env:      map[string]string{config.EnvTypeSafeKey: "ts-key\r\n"},
			want:     []string{"[PASS] providers", "[PASS] credential format", "typesafe_key=set"},
			dontWant: []string{"[WARN]"},
		},
		{
			name:     "blank key fails selection",
			env:      map[string]string{config.EnvTypeSafeKey: " \n"},
			wantCode: 1,
			want: []string{
				"[FAIL] providers", config.EnvTypeSafeKey + ")", "typesafe_key=blank",
				"[WARN] credential format: " + config.EnvTypeSafeKey + " is set but only whitespace",
			},
		},
		{
			name:     "control character fails selection",
			env:      map[string]string{config.EnvOpenRouterKey: "or\x01key"},
			wantCode: 1,
			want: []string{
				"[FAIL] providers", "openrouter_key=invalid",
				"[WARN] credential format: " + config.EnvOpenRouterKey + " has a control character that cannot be sent in a header",
			},
		},
		{
			name: "unused bad key still warns",
			env: map[string]string{
				config.EnvTypeSafeKey:   "ts-key",
				config.EnvOpenRouterKey: "or\x01key",
				config.EnvFallback:      "false",
			},
			want:     []string{"[PASS] providers", "[WARN] credential format: " + config.EnvOpenRouterKey + " has a control character that cannot be sent in a header"},
			dontWant: []string{"[FAIL]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out strings.Builder
			code := doctor.Run(t.Context(), &out, getenvFrom(tt.env), false, nil)
			got := out.String()
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d\n%s", code, tt.wantCode, got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("output lacks %q\n%s", w, got)
				}
			}
			for _, w := range tt.dontWant {
				if strings.Contains(got, w) {
					t.Errorf("output contains %q\n%s", w, got)
				}
			}
		})
	}
}

// gatedEvaluator waits for a signal before answering, so a test can tell
// concurrent probes from sequential ones.
type gatedEvaluator struct {
	started chan<- struct{} // closed when this probe starts, when non-nil
	wait    <-chan struct{} // waited on before answering, when non-nil
	res     *jev.Result
}

func (g *gatedEvaluator) Evaluate(ctx context.Context, _ jev.Request) (*jev.Result, error) {
	if g.started != nil {
		close(g.started)
	}
	if g.wait != nil {
		select {
		case <-g.wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("the other probe never started: probes ran sequentially")
		}
	}
	return g.res, nil
}

// TestRunProbesConcurrentlyInOrder checks that the two probes overlap (the
// primary's probe waits for the fallback's to start, which a sequential loop
// never does) and are still reported in provider order.
func TestRunProbesConcurrentlyInOrder(t *testing.T) {
	t.Parallel()

	res := &jev.Result{Latency: time.Millisecond}
	res.Model = "jev-1.13.0"
	secondStarted := make(chan struct{})
	evs := map[string]doctor.Evaluator{
		jev.ProviderTypeSafe:   &gatedEvaluator{wait: secondStarted, res: res},
		jev.ProviderOpenRouter: &gatedEvaluator{started: secondStarted, res: res},
	}
	factory := func(p jev.Provider) (doctor.Evaluator, error) { return evs[p.Name], nil }

	var out strings.Builder
	code := doctor.Run(t.Context(), &out, getenvFrom(map[string]string{
		config.EnvTypeSafeKey:   "ts-key",
		config.EnvOpenRouterKey: "or-key",
	}), true, factory)

	got := out.String()
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, got)
	}
	ts := strings.Index(got, "[PASS] probe "+jev.ProviderTypeSafe)
	or := strings.Index(got, "[PASS] probe "+jev.ProviderOpenRouter)
	if ts < 0 || or < 0 || ts > or {
		t.Errorf("want both probes to pass, typesafe first\n%s", got)
	}
}

// cancellingEvaluator cancels the run's context mid-probe, as Ctrl-C does.
type cancellingEvaluator struct{ cancel context.CancelFunc }

func (c *cancellingEvaluator) Evaluate(ctx context.Context, _ jev.Request) (*jev.Result, error) {
	c.cancel()
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestRunProbeInterrupted checks that an interrupted probe reads as SKIP rather
// than as a provider failure, and that the run still exits non-zero.
func TestRunProbeInterrupted(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ev := &cancellingEvaluator{cancel: cancel}

	var out strings.Builder
	code := doctor.Run(ctx, &out, getenvFrom(map[string]string{config.EnvTypeSafeKey: "ts-key"}), true,
		func(jev.Provider) (doctor.Evaluator, error) { return ev, nil })

	got := out.String()
	if code != 1 {
		t.Errorf("exit code = %d, want 1 for an interrupted run\n%s", code, got)
	}
	if !strings.Contains(got, "[SKIP] probe "+jev.ProviderTypeSafe+": interrupted") {
		t.Errorf("want an interrupted SKIP line\n%s", got)
	}
	if strings.Contains(got, "[FAIL]") {
		t.Errorf("an interruption must not read as a failure\n%s", got)
	}
}

// TestRunProbeRealClient drives the default client factory (a nil factory)
// against a local server standing in for TypeSafe, so the real client
// construction and the probe request are exercised without reaching a real
// provider.
func TestRunProbeRealClient(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ts-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"reachable":{"type":"noul","noul":0.9}},"usage":{"input_tokens":9,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	var out strings.Builder
	code := doctor.Run(t.Context(), &out, getenvFrom(map[string]string{
		config.EnvTypeSafeKey:     "ts-key\n",
		config.EnvTypeSafeBaseURL: srv.URL,
	}), true, nil)

	got := out.String()
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, got)
	}
	if !strings.Contains(got, "[PASS] probe "+jev.ProviderTypeSafe+": model=jev-1.13.0") {
		t.Errorf("want a passing probe through the real client\n%s", got)
	}
}

// TestRunInterruptedWithoutProbeExplainsTheExit checks that a run cancelled
// with -probe off still prints why it exits 1, instead of only PASS lines.
func TestRunInterruptedWithoutProbeExplainsTheExit(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out strings.Builder
	code := doctor.Run(ctx, &out, getenvFrom(map[string]string{config.EnvTypeSafeKey: "ts-key"}), false, nil)

	got := out.String()
	if code != 1 {
		t.Errorf("exit code = %d, want 1\n%s", code, got)
	}
	if !strings.Contains(got, "[SKIP] run: interrupted") {
		t.Errorf("want a SKIP run line explaining the exit\n%s", got)
	}
	if strings.Contains(got, "[FAIL]") {
		t.Errorf("an interruption must not read as a failure\n%s", got)
	}
}

// TestRunProbeCleansProviderValues checks that the model and id a provider
// returns cannot carry a control sequence onto the PASS line.
func TestRunProbeCleansProviderValues(t *testing.T) {
	t.Parallel()

	res := &jev.Result{Latency: time.Millisecond}
	res.Model = "jev\x1b[31m-1"
	res.ID = "id\x1b[2J"
	ev := &fakeEvaluator{res: res}

	var out strings.Builder
	doctor.Run(t.Context(), &out, getenvFrom(map[string]string{config.EnvTypeSafeKey: "ts-key"}), true, factoryReturning(ev))

	got := out.String()
	if strings.Contains(got, "\x1b") {
		t.Errorf("an escape reached the report: %q", got)
	}
	if !strings.Contains(got, "model=jev [31m-1") || !strings.Contains(got, "id=id [2J") {
		t.Errorf("want the model and id shown with the escape replaced\n%s", got)
	}
}
