package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tphakala/jev-mcp/internal/config"
)

// getenvFrom returns a getenv function backed by m, so tests stay parallel
// without t.Setenv (which forbids t.Parallel). A missing key yields "", exactly
// as os.Getenv does for an unset variable.
func getenvFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// wantConfig is the expected subset of a resolved Config. Comparisons live in
// assertConfig so the table rows stay declarative.
type wantConfig struct {
	provider  config.ProviderMode
	tsKey     string
	orKey     string
	httpToken string
	tsBase    string
	orBase    string
	model     string
	timeout   time.Duration
	retries   int
	fallback  bool
}

// wantDefaults is the expectation for an empty environment: every setting at its
// compiled default and no credentials.
func wantDefaults() wantConfig {
	return wantConfig{
		provider: config.ProviderAuto,
		model:    config.DefaultModel,
		timeout:  config.DefaultTimeout,
		retries:  config.DefaultMaxRetries,
		fallback: config.DefaultFallback,
	}
}

func assertConfig(t *testing.T, got *config.Config, w *wantConfig) {
	t.Helper()
	if got.Provider != w.provider {
		t.Errorf("Provider = %q, want %q", got.Provider, w.provider)
	}
	if got.TypeSafeKey != w.tsKey {
		t.Errorf("TypeSafeKey = %q, want %q", got.TypeSafeKey, w.tsKey)
	}
	if got.OpenRouterKey != w.orKey {
		t.Errorf("OpenRouterKey = %q, want %q", got.OpenRouterKey, w.orKey)
	}
	if got.HTTPToken != w.httpToken {
		t.Errorf("HTTPToken = %q, want %q", got.HTTPToken, w.httpToken)
	}
	if got.TypeSafeBaseURL != w.tsBase {
		t.Errorf("TypeSafeBaseURL = %q, want %q", got.TypeSafeBaseURL, w.tsBase)
	}
	if got.OpenRouterBaseURL != w.orBase {
		t.Errorf("OpenRouterBaseURL = %q, want %q", got.OpenRouterBaseURL, w.orBase)
	}
	if got.DefaultModel != w.model {
		t.Errorf("DefaultModel = %q, want %q", got.DefaultModel, w.model)
	}
	if got.Timeout != w.timeout {
		t.Errorf("Timeout = %v, want %v", got.Timeout, w.timeout)
	}
	if got.MaxRetries != w.retries {
		t.Errorf("MaxRetries = %d, want %d", got.MaxRetries, w.retries)
	}
	if got.Fallback != w.fallback {
		t.Errorf("Fallback = %t, want %t", got.Fallback, w.fallback)
	}
}

func TestResolveValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		env    map[string]string
		mutate func(*wantConfig) // applied to wantDefaults(); nil means pure defaults
	}{
		{
			name: "empty environment is all defaults",
			env:  map[string]string{},
		},
		{
			name:   "typesafe key only",
			env:    map[string]string{config.EnvTypeSafeKey: "ts-key"},
			mutate: func(w *wantConfig) { w.tsKey = "ts-key" },
		},
		{
			name:   "openrouter key only",
			env:    map[string]string{config.EnvOpenRouterKey: "or-key"},
			mutate: func(w *wantConfig) { w.orKey = "or-key" },
		},
		{
			name: "both keys",
			env: map[string]string{
				config.EnvTypeSafeKey:   "ts-key",
				config.EnvOpenRouterKey: "or-key",
			},
			mutate: func(w *wantConfig) { w.tsKey, w.orKey = "ts-key", "or-key" },
		},
		{
			name: "explicit typesafe with key",
			env: map[string]string{
				config.EnvProvider:    "typesafe",
				config.EnvTypeSafeKey: "ts-key",
			},
			mutate: func(w *wantConfig) { w.provider, w.tsKey = config.ProviderTypeSafe, "ts-key" },
		},
		{
			name:   "explicit typesafe without key still resolves",
			env:    map[string]string{config.EnvProvider: "typesafe"},
			mutate: func(w *wantConfig) { w.provider = config.ProviderTypeSafe },
		},
		{
			name: "explicit openrouter with key",
			env: map[string]string{
				config.EnvProvider:      "openrouter",
				config.EnvOpenRouterKey: "or-key",
			},
			mutate: func(w *wantConfig) { w.provider, w.orKey = config.ProviderOpenRouter, "or-key" },
		},
		{
			name: "typesafe base url override wins over sdk name",
			env: map[string]string{
				config.EnvTypeSafeKey:        "ts-key",
				config.EnvTypeSafeBaseURL:    "https://jev.example",
				config.EnvTypeSafeSDKBaseURL: "https://sdk.example",
			},
			mutate: func(w *wantConfig) { w.tsKey, w.tsBase = "ts-key", "https://jev.example" },
		},
		{
			name: "typesafe base url falls back to sdk name",
			env: map[string]string{
				config.EnvTypeSafeKey:        "ts-key",
				config.EnvTypeSafeSDKBaseURL: "https://sdk.example",
			},
			mutate: func(w *wantConfig) { w.tsKey, w.tsBase = "ts-key", "https://sdk.example" },
		},
		{
			name: "openrouter base url override",
			env: map[string]string{
				config.EnvOpenRouterKey:     "or-key",
				config.EnvOpenRouterBaseURL: "https://or.example/api",
			},
			mutate: func(w *wantConfig) { w.orKey, w.orBase = "or-key", "https://or.example/api" },
		},
		{
			name: "default model override wins over sdk name",
			env: map[string]string{
				config.EnvTypeSafeKey:     "ts-key",
				config.EnvDefaultModel:    "jev-1.13",
				config.EnvSDKDefaultModel: "jev-sdk",
			},
			mutate: func(w *wantConfig) { w.tsKey, w.model = "ts-key", "jev-1.13" },
		},
		{
			name: "default model falls back to sdk name",
			env: map[string]string{
				config.EnvTypeSafeKey:     "ts-key",
				config.EnvSDKDefaultModel: "jev-sdk",
			},
			mutate: func(w *wantConfig) { w.tsKey, w.model = "ts-key", "jev-sdk" },
		},
		{
			name: "timeout parses",
			env: map[string]string{
				config.EnvTypeSafeKey: "ts-key",
				config.EnvTimeout:     "45s",
			},
			mutate: func(w *wantConfig) { w.tsKey, w.timeout = "ts-key", 45*time.Second },
		},
		{
			name: "max retries parses",
			env: map[string]string{
				config.EnvTypeSafeKey: "ts-key",
				config.EnvMaxRetries:  "5",
			},
			mutate: func(w *wantConfig) { w.tsKey, w.retries = "ts-key", 5 },
		},
		{
			name: "fallback parses false",
			env: map[string]string{
				config.EnvTypeSafeKey: "ts-key",
				config.EnvFallback:    "false",
			},
			mutate: func(w *wantConfig) { w.tsKey, w.fallback = "ts-key", false },
		},
		{
			name: "http token is stored",
			env: map[string]string{
				config.EnvTypeSafeKey: "ts-key",
				config.EnvHTTPToken:   "http-bearer",
			},
			mutate: func(w *wantConfig) { w.tsKey, w.httpToken = "ts-key", "http-bearer" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			want := wantDefaults()
			if tt.mutate != nil {
				tt.mutate(&want)
			}
			cfg, err := config.Resolve(getenvFrom(tt.env))
			if err != nil {
				t.Fatalf("unexpected err = %v", err)
			}
			assertConfig(t, &cfg, &want)
		})
	}
}

func TestResolveErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     map[string]string
		wantErr error
	}{
		{
			name:    "invalid provider",
			env:     map[string]string{config.EnvProvider: "azure", config.EnvTypeSafeKey: "ts-key"},
			wantErr: config.ErrInvalidProvider,
		},
		{
			name:    "timeout unparsable",
			env:     map[string]string{config.EnvTypeSafeKey: "ts-key", config.EnvTimeout: "later"},
			wantErr: config.ErrInvalidTimeout,
		},
		{
			name:    "timeout non-positive",
			env:     map[string]string{config.EnvTypeSafeKey: "ts-key", config.EnvTimeout: "0s"},
			wantErr: config.ErrInvalidTimeout,
		},
		{
			// Values are used verbatim: a whitespace-padded duration is a parse
			// error, not silently trimmed.
			name:    "whitespace-padded timeout",
			env:     map[string]string{config.EnvTypeSafeKey: "ts-key", config.EnvTimeout: " 45s "},
			wantErr: config.ErrInvalidTimeout,
		},
		{
			name:    "max retries unparsable",
			env:     map[string]string{config.EnvTypeSafeKey: "ts-key", config.EnvMaxRetries: "many"},
			wantErr: config.ErrInvalidMaxRetries,
		},
		{
			name:    "max retries negative",
			env:     map[string]string{config.EnvTypeSafeKey: "ts-key", config.EnvMaxRetries: "-1"},
			wantErr: config.ErrInvalidMaxRetries,
		},
		{
			name:    "whitespace-padded retries",
			env:     map[string]string{config.EnvTypeSafeKey: "ts-key", config.EnvMaxRetries: " 3 "},
			wantErr: config.ErrInvalidMaxRetries,
		},
		{
			name:    "fallback unparsable",
			env:     map[string]string{config.EnvTypeSafeKey: "ts-key", config.EnvFallback: "sometimes"},
			wantErr: config.ErrInvalidFallback,
		},
		{
			// A parse error surfaces even with no key set: Resolve does not check
			// keys, so nothing masks the timeout error.
			name:    "parse error with no key set",
			env:     map[string]string{config.EnvTimeout: "nope"},
			wantErr: config.ErrInvalidTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := config.Resolve(getenvFrom(tt.env))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want errors.Is %v", err, tt.wantErr)
			}
			// Resolve always returns a populated Config, even on error, so a
			// diagnostic caller can still render the settings. All resolvers run
			// (they are firstNonNil arguments), so every source key is present even
			// when one of them errored.
			if len(cfg.Sources) != 7 {
				t.Errorf("Sources has %d keys on the error path, want 7 (every resolver must run): %v",
					len(cfg.Sources), cfg.Sources)
			}
		})
	}
}

func TestResolveSourcesCoverAllNonSecretSettings(t *testing.T) {
	t.Parallel()

	cfg, err := config.Resolve(getenvFrom(map[string]string{config.EnvTypeSafeKey: "ts-key"}))
	if err != nil {
		t.Fatalf("unexpected err = %v", err)
	}
	want := []string{
		config.SourceProvider, config.SourceTypeSafeBaseURL, config.SourceOpenRouterBaseURL,
		config.SourceDefaultModel, config.SourceTimeout, config.SourceMaxRetries, config.SourceFallback,
	}
	for _, key := range want {
		if _, ok := cfg.Sources[key]; !ok {
			t.Errorf("Sources missing key %q", key)
		}
	}
	if len(cfg.Sources) != len(want) {
		t.Errorf("Sources has %d keys, want %d: %v", len(cfg.Sources), len(want), cfg.Sources)
	}
}

func TestResolveSourcePrecedence(t *testing.T) {
	t.Parallel()

	cfg, err := config.Resolve(getenvFrom(map[string]string{
		config.EnvTypeSafeKey:        "ts-key",
		config.EnvTypeSafeSDKBaseURL: "https://sdk.example", // only the SDK name is set
		config.EnvDefaultModel:       "jev-1.13",            // JEV_MCP name is set
	}))
	if err != nil {
		t.Fatalf("unexpected err = %v", err)
	}
	if got := cfg.Sources[config.SourceTypeSafeBaseURL]; got != config.EnvTypeSafeSDKBaseURL {
		t.Errorf("typesafe_base_url source = %q, want %q", got, config.EnvTypeSafeSDKBaseURL)
	}
	if got := cfg.Sources[config.SourceDefaultModel]; got != config.EnvDefaultModel {
		t.Errorf("default_model source = %q, want %q", got, config.EnvDefaultModel)
	}
	if got := cfg.Sources[config.SourceTimeout]; got != config.SourceDefault {
		t.Errorf("timeout source = %q, want %q", got, config.SourceDefault)
	}
}

// TestResolveParseErrorAttributesDefault confirms that when an override fails to
// parse, the default value is in effect, so the source is recorded as default,
// not the env var that supplied the bad value.
func TestResolveParseErrorAttributesDefault(t *testing.T) {
	t.Parallel()

	cfg, err := config.Resolve(getenvFrom(map[string]string{
		config.EnvTypeSafeKey: "ts-key",
		config.EnvTimeout:     "nope",
	}))
	if !errors.Is(err, config.ErrInvalidTimeout) {
		t.Fatalf("err = %v, want ErrInvalidTimeout", err)
	}
	if cfg.Timeout != config.DefaultTimeout {
		t.Errorf("Timeout = %v, want the default %v to remain in effect", cfg.Timeout, config.DefaultTimeout)
	}
	if got := cfg.Sources[config.SourceTimeout]; got != config.SourceDefault {
		t.Errorf("timeout source = %q, want %q (default is in effect, not the bad env value)", got, config.SourceDefault)
	}
}

// TestResolveInvalidProviderIsRejectedBySelect confirms an invalid provider mode
// is preserved on the Config (not reset to auto), so a caller that ignores the
// Resolve error still cannot silently select a provider: Select rejects it too.
func TestResolveInvalidProviderIsRejectedBySelect(t *testing.T) {
	t.Parallel()

	cfg, err := config.Resolve(getenvFrom(map[string]string{
		config.EnvProvider:    "azure",
		config.EnvTypeSafeKey: "ts-key",
	}))
	if !errors.Is(err, config.ErrInvalidProvider) {
		t.Fatalf("Resolve err = %v, want ErrInvalidProvider", err)
	}
	if cfg.Provider != config.ProviderMode("azure") {
		t.Errorf("cfg.Provider = %q, want the invalid value preserved", cfg.Provider)
	}
	if _, selErr := config.Select(&cfg); !errors.Is(selErr, config.ErrInvalidProvider) {
		t.Errorf("Select err = %v, want ErrInvalidProvider (an invalid mode must not fall back to auto)", selErr)
	}
}

// TestResolveRedactsLongInvalidValue confirms a long invalid value (a credential
// pasted into a typed variable) is redacted from the parse error, while a short
// invalid value is still shown to help a genuine typo.
func TestResolveRedactsLongInvalidValue(t *testing.T) {
	t.Parallel()

	secret := "sk-ant-" + strings.Repeat("x", 60)
	_, err := config.Resolve(getenvFrom(map[string]string{config.EnvTimeout: secret}))
	if !errors.Is(err, config.ErrInvalidTimeout) {
		t.Fatalf("err = %v, want ErrInvalidTimeout", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("parse error echoed the long value verbatim: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "redacted") {
		t.Errorf("parse error should indicate redaction of a long value: %q", err.Error())
	}

	_, short := config.Resolve(getenvFrom(map[string]string{config.EnvTimeout: "later"}))
	if !strings.Contains(short.Error(), `"later"`) {
		t.Errorf("a short invalid value should be shown to aid a typo: %q", short.Error())
	}
}

func TestResolveNeverRecordsSecretsInSources(t *testing.T) {
	t.Parallel()

	// Distinctive values that avoid credential-shaped identifiers and words.
	const (
		tsVal   = "cred-ts-9f3a"
		orVal   = "cred-or-2b7c"
		httpVal = "cred-hx-11xz"
	)
	cfg, err := config.Resolve(getenvFrom(map[string]string{
		config.EnvTypeSafeKey:   tsVal,
		config.EnvOpenRouterKey: orVal,
		config.EnvHTTPToken:     httpVal,
	}))
	if err != nil {
		t.Fatalf("unexpected err = %v", err)
	}
	// The credentials must be present on the Config but absent from Sources, both
	// as keys and as values.
	if cfg.TypeSafeKey != tsVal || cfg.OpenRouterKey != orVal || cfg.HTTPToken != httpVal {
		t.Fatal("Resolve dropped a credential from the Config")
	}
	for k, v := range cfg.Sources {
		if v == tsVal || v == orVal || v == httpVal {
			t.Errorf("Sources[%q] leaks a credential value", k)
		}
		if k == config.EnvTypeSafeKey || k == config.EnvOpenRouterKey || k == config.EnvHTTPToken {
			t.Errorf("Sources contains a credential env name as a key: %q", k)
		}
	}
}
