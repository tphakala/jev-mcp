// Package config resolves the jev-mcp process configuration from the
// environment into a single [Config] value. It reads the environment through an
// injected getenv function so callers and tests stay in control of the source,
// and it never performs network I/O. Turning a [Config] into an ordered list of
// providers is [Select]'s job; serving it belongs to cmd/jev-mcp, and
// diagnosing it to internal/doctor.
//
// Secrets (the API keys and the HTTP bearer token) are held on the [Config] but
// never recorded in [Config.Sources] and never logged, so the resolved
// configuration can be printed for diagnostics without leaking credentials. A
// value that fails to parse is redacted when it is over-long, so a credential
// pasted into the wrong variable is not echoed into a diagnostic that may reach
// a log.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Environment variable names. JEV_MCP_* are this project's own knobs;
// TYPESAFE_API_KEY, TYPESAFE_BASE_URL, and TYPESAFE_DEFAULT_MODEL match the
// official TypeSafe SDK so an existing setup keeps working. There is
// deliberately no OpenRouter path override: the path is always
// [jev.SystemOnePath], because a user-supplied path against the OpenRouter base
// invites a doubled "/api" segment.
const (
	EnvProvider           = "JEV_MCP_PROVIDER"
	EnvTypeSafeKey        = "TYPESAFE_API_KEY"
	EnvOpenRouterKey      = "OPENROUTER_API_KEY"
	EnvTypeSafeBaseURL    = "JEV_MCP_TYPESAFE_BASE_URL"
	EnvTypeSafeSDKBaseURL = "TYPESAFE_BASE_URL"
	EnvOpenRouterBaseURL  = "JEV_MCP_OPENROUTER_BASE_URL"
	EnvDefaultModel       = "JEV_MCP_DEFAULT_MODEL"
	EnvSDKDefaultModel    = "TYPESAFE_DEFAULT_MODEL"
	EnvTimeout            = "JEV_MCP_TIMEOUT"
	EnvMaxRetries         = "JEV_MCP_MAX_RETRIES"
	EnvFallback           = "JEV_MCP_FALLBACK"
	EnvHTTPToken          = "JEV_MCP_HTTP_TOKEN" //nolint:gosec // G101: an environment variable name, not a credential.
)

// Defaults applied when the corresponding environment variable is unset.
const (
	DefaultProvider   = ProviderAuto
	DefaultModel      = "jev-latest"
	DefaultTimeout    = 30 * time.Second
	DefaultMaxRetries = 2
	DefaultFallback   = true
)

// Source keys used in [Config.Sources], and the value used for a setting left at
// its default. They are exported so a consumer (for example the doctor package)
// references one definition rather than re-spelling the string, which turns a
// rename into a compile error instead of a silently blank source column.
const (
	SourceDefault           = "default"
	SourceProvider          = "provider"
	SourceTypeSafeBaseURL   = "typesafe_base_url"
	SourceOpenRouterBaseURL = "openrouter_base_url"
	SourceDefaultModel      = "default_model"
	SourceTimeout           = "timeout"
	SourceMaxRetries        = "max_retries"
	SourceFallback          = "fallback"
)

// maxEchoedValue bounds how much of an invalid setting value is echoed in a
// parse error. A legitimate duration, count, mode, or bool is short; a value
// longer than this is most likely a credential pasted into the wrong variable,
// so it is redacted rather than printed, because the error is rendered to the
// doctor report and can reach a terminal or a CI log.
const maxEchoedValue = 32

// ProviderMode selects which backend(s) the client will try.
type ProviderMode string

// The three provider modes. Auto derives the order from which keys are set.
const (
	// ProviderAuto picks TypeSafe when its key is set, else OpenRouter, and with
	// both keys uses TypeSafe as primary and OpenRouter as fallback.
	ProviderAuto ProviderMode = "auto"
	// ProviderTypeSafe forces the native TypeSafe API.
	ProviderTypeSafe ProviderMode = "typesafe"
	// ProviderOpenRouter forces OpenRouter.
	ProviderOpenRouter ProviderMode = "openrouter"
)

// Config is the resolved process configuration. The base URL fields hold the
// override only: an empty value means "use the provider's default base", which
// the provider constructors apply. TypeSafeKey, OpenRouterKey, and HTTPToken are
// secrets and are absent from Sources. HTTPToken is the raw environment value;
// pass it through [NormalizeHTTPToken] before using it.
type Config struct {
	Provider          ProviderMode
	TypeSafeKey       string
	OpenRouterKey     string
	TypeSafeBaseURL   string
	OpenRouterBaseURL string
	DefaultModel      string
	Timeout           time.Duration
	MaxRetries        int
	Fallback          bool
	HTTPToken         string
	// Sources maps each non-secret setting's stable key (for example
	// [SourceTimeout]) to the environment variable that supplied its effective
	// value, or [SourceDefault] when the default is in effect (including when an
	// override was set but failed to parse). Secrets are never included.
	Sources map[string]string
}

// Configuration errors. Each sentinel names the offending variable; the parse
// errors additionally append the offending value (redacted when over-long).
// ErrNoAPIKey and ErrProviderKeyMissing are returned by [Select], not [Resolve]:
// a missing key is a selection failure, not a parse failure, so [Resolve] still
// succeeds and a diagnostic caller can render the settings before the provider
// list fails.
var (
	// ErrNoAPIKey is returned by [Select] in auto mode when neither API key is
	// set.
	ErrNoAPIKey = errors.New("config: no API key: set TYPESAFE_API_KEY or OPENROUTER_API_KEY")
	// ErrProviderKeyMissing is returned by [Select] when an explicitly selected
	// provider has no API key.
	ErrProviderKeyMissing = errors.New("config: selected provider has no API key")
	// ErrInvalidProvider is returned for a JEV_MCP_PROVIDER outside the three
	// valid modes.
	ErrInvalidProvider = errors.New("config: invalid JEV_MCP_PROVIDER")
	// ErrInvalidTimeout is returned for an unparsable or non-positive
	// JEV_MCP_TIMEOUT.
	ErrInvalidTimeout = errors.New("config: invalid JEV_MCP_TIMEOUT")
	// ErrInvalidMaxRetries is returned for an unparsable or negative
	// JEV_MCP_MAX_RETRIES.
	ErrInvalidMaxRetries = errors.New("config: invalid JEV_MCP_MAX_RETRIES")
	// ErrInvalidFallback is returned for an unparsable JEV_MCP_FALLBACK.
	ErrInvalidFallback = errors.New("config: invalid JEV_MCP_FALLBACK")
	// ErrBlankHTTPToken is returned by [NormalizeHTTPToken], not [Resolve], for
	// an HTTP bearer token that is set but holds only spaces, tabs, and line
	// breaks. It never includes the value.
	ErrBlankHTTPToken = errors.New("config: HTTP bearer token is only whitespace")
)

// Resolve builds a Config from the environment read through getenv (pass
// os.Getenv in production). It always returns a fully populated Config, with
// defaults filled in for anything unset or unparsable, so a diagnostic caller
// can render the settings even on failure. The returned error is the first
// parse error found (an invalid provider, timeout, retry count, or fallback
// flag). Resolve does NOT check that an API key is present: that is a selection
// concern handled by [Select], so Resolve succeeds for a keyless environment
// and doctor can still print the settings. An unset variable is
// indistinguishable from one set to the empty string, and both mean "use the
// default"; values are used verbatim, so a whitespace-padded number or duration
// is a parse error rather than being silently trimmed.
func Resolve(getenv func(string) string) (Config, error) {
	cfg := Config{
		Provider:      DefaultProvider,
		TypeSafeKey:   getenv(EnvTypeSafeKey),
		OpenRouterKey: getenv(EnvOpenRouterKey),
		DefaultModel:  DefaultModel,
		Timeout:       DefaultTimeout,
		MaxRetries:    DefaultMaxRetries,
		Fallback:      DefaultFallback,
		HTTPToken:     getenv(EnvHTTPToken),
		Sources:       make(map[string]string),
	}

	resolveBaseURLs(&cfg, getenv)
	resolveModel(&cfg, getenv)

	// Every resolver runs (they are evaluated as arguments before firstNonNil is
	// called) so Sources is fully populated regardless of a parse error; the
	// first error wins.
	err := firstNonNil(
		resolveProvider(&cfg, getenv),
		resolveTimeout(&cfg, getenv),
		resolveMaxRetries(&cfg, getenv),
		resolveFallback(&cfg, getenv),
	)
	return cfg, err
}

// firstNonNil returns the first non-nil error, or nil.
func firstNonNil(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// echoValue renders an invalid setting value for a parse error: quoted when
// short, redacted (length only) when longer than [maxEchoedValue], so a
// misplaced credential is not printed.
func echoValue(v string) string {
	if len(v) > maxEchoedValue {
		return fmt.Sprintf("<redacted, %d bytes>", len(v))
	}
	return strconv.Quote(v)
}

// resolveBaseURLs sets the two base URL overrides. TypeSafe honours the
// SDK-compatible name as a fallback before the default.
func resolveBaseURLs(cfg *Config, getenv func(string) string) {
	if v := getenv(EnvTypeSafeBaseURL); v != "" {
		cfg.TypeSafeBaseURL, cfg.Sources[SourceTypeSafeBaseURL] = v, EnvTypeSafeBaseURL
	} else if v := getenv(EnvTypeSafeSDKBaseURL); v != "" {
		cfg.TypeSafeBaseURL, cfg.Sources[SourceTypeSafeBaseURL] = v, EnvTypeSafeSDKBaseURL
	} else {
		cfg.Sources[SourceTypeSafeBaseURL] = SourceDefault
	}
	if v := getenv(EnvOpenRouterBaseURL); v != "" {
		cfg.OpenRouterBaseURL, cfg.Sources[SourceOpenRouterBaseURL] = v, EnvOpenRouterBaseURL
	} else {
		cfg.Sources[SourceOpenRouterBaseURL] = SourceDefault
	}
}

// resolveModel sets the default model. JEV_MCP_ wins over the SDK-compatible
// name over the compiled default.
func resolveModel(cfg *Config, getenv func(string) string) {
	if v := getenv(EnvDefaultModel); v != "" {
		cfg.DefaultModel, cfg.Sources[SourceDefaultModel] = v, EnvDefaultModel
	} else if v := getenv(EnvSDKDefaultModel); v != "" {
		cfg.DefaultModel, cfg.Sources[SourceDefaultModel] = v, EnvSDKDefaultModel
	} else {
		cfg.Sources[SourceDefaultModel] = SourceDefault
	}
}

// resolveProvider parses the provider mode, defaulting to auto. On an invalid
// mode it still records the raw value on cfg.Provider (not the default), so a
// caller that ignores the error, and [Select], see the invalid mode and reject
// it rather than silently falling back to auto.
func resolveProvider(cfg *Config, getenv func(string) string) error {
	v := getenv(EnvProvider)
	if v == "" {
		cfg.Sources[SourceProvider] = SourceDefault
		return nil
	}
	cfg.Sources[SourceProvider] = EnvProvider
	cfg.Provider = ProviderMode(v)
	switch cfg.Provider {
	case ProviderAuto, ProviderTypeSafe, ProviderOpenRouter:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrInvalidProvider, echoValue(v))
	}
}

// resolveTimeout parses the whole-call budget, rejecting a non-positive value.
// On failure the compiled default stays in effect, so the source is recorded as
// default, not the env var.
func resolveTimeout(cfg *Config, getenv func(string) string) error {
	v := getenv(EnvTimeout)
	if v == "" {
		cfg.Sources[SourceTimeout] = SourceDefault
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		cfg.Sources[SourceTimeout] = SourceDefault
		return fmt.Errorf("%w: %s", ErrInvalidTimeout, echoValue(v))
	}
	if d <= 0 {
		cfg.Sources[SourceTimeout] = SourceDefault
		return fmt.Errorf("%w: must be positive, got %s", ErrInvalidTimeout, echoValue(v))
	}
	cfg.Sources[SourceTimeout] = EnvTimeout
	cfg.Timeout = d
	return nil
}

// resolveMaxRetries parses the per-provider retry count, rejecting a negative
// value. On failure the default stays in effect and the source is default.
func resolveMaxRetries(cfg *Config, getenv func(string) string) error {
	v := getenv(EnvMaxRetries)
	if v == "" {
		cfg.Sources[SourceMaxRetries] = SourceDefault
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		cfg.Sources[SourceMaxRetries] = SourceDefault
		return fmt.Errorf("%w: %s", ErrInvalidMaxRetries, echoValue(v))
	}
	if n < 0 {
		cfg.Sources[SourceMaxRetries] = SourceDefault
		return fmt.Errorf("%w: must not be negative, got %s", ErrInvalidMaxRetries, echoValue(v))
	}
	cfg.Sources[SourceMaxRetries] = EnvMaxRetries
	cfg.MaxRetries = n
	return nil
}

// resolveFallback parses the fallback flag. On failure the default stays in
// effect and the source is default.
func resolveFallback(cfg *Config, getenv func(string) string) error {
	v := getenv(EnvFallback)
	if v == "" {
		cfg.Sources[SourceFallback] = SourceDefault
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		cfg.Sources[SourceFallback] = SourceDefault
		return fmt.Errorf("%w: %s", ErrInvalidFallback, echoValue(v))
	}
	cfg.Sources[SourceFallback] = EnvFallback
	cfg.Fallback = b
	return nil
}

// httpTokenTrim is what [NormalizeHTTPToken] strips from each end of a token:
// the space and tab that net/http strips from a received header value
// (net/textproto trim, Go 1.27.1), plus CR and LF, which cannot appear in a
// header value at all.
const httpTokenTrim = " \t\r\n"

// NormalizeHTTPToken prepares an HTTP bearer token for use by trimming
// [httpTokenTrim] from both ends, so a token pasted with a trailing space or
// newline still matches what a client can send. An empty value stays empty,
// meaning no authentication. A value that is non-empty but only trimmable
// characters returns [ErrBlankHTTPToken] rather than an empty token, so a
// configured token never turns into "no authentication".
func NormalizeHTTPToken(v string) (string, error) {
	token := strings.Trim(v, httpTokenTrim)
	if token == "" && v != "" {
		return "", ErrBlankHTTPToken
	}
	return token, nil
}
