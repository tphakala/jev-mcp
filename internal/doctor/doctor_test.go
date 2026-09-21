package doctor_test

import (
	"context"
	"errors"
	"strings"
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
// error, so the probe path runs without a network call.
type fakeEvaluator struct {
	res    *jev.Result
	err    error
	called bool
	gotReq jev.Request
}

func (f *fakeEvaluator) Evaluate(_ context.Context, req jev.Request) (*jev.Result, error) {
	f.called = true
	f.gotReq = req
	return f.res, f.err
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
	// One credential per format problem: TypeSafe key trailing whitespace,
	// OpenRouter key a control character, HTTP token an embedded quote.
	code := doctor.Run(t.Context(), &out, getenvFrom(map[string]string{
		config.EnvTypeSafeKey:   "ts-key ",
		config.EnvOpenRouterKey: "or\x01key",
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
	for _, want := range []string{"surrounding whitespace", "a control character", "an embedded quote"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning should describe %q\n%s", want, got)
		}
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
	if !ev.called {
		t.Error("probe did not call the client")
	}
	// The probe request must be valid, or a live probe would fail before the
	// network. This pins the request shape to jev.Validate.
	if err := jev.Validate(ev.gotReq); err != nil {
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
	if ev.called {
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
	if ev.called {
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
		// A trailing space makes the TypeSafe key trip a format warning, so the
		// warning path also handles a secret value; it must still not echo it.
		tsSecret   = "SECRET-typesafe-9f3a "
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
	// Check the raw values and the trimmed TypeSafe key (the warning path could
	// leak either form).
	for _, secret := range []string{tsSecret, strings.TrimSpace(tsSecret), orSecret, httpSecret} {
		if strings.Contains(got, secret) {
			t.Errorf("report leaked a secret value %q\n%s", secret, got)
		}
	}
}
