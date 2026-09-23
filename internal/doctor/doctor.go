// Package doctor runs read-only preflight checks for jev-mcp and prints a
// human-readable report. It resolves the configuration, shows every non-secret
// setting with its source, lists the providers that would be tried, warns about
// common paste errors in credentials, and, when asked, makes one live decision
// call per provider to prove reachability. It never writes state and never
// prints a secret value.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/tphakala/jev-mcp/internal/config"
	"github.com/tphakala/jev-mcp/internal/jev"
	"github.com/tphakala/jev-mcp/internal/provider"
)

// probeBudget is the whole-call budget for a single -probe attempt. It is fixed
// and short, and the probe uses zero retries, so a preflight against a down
// provider fails quickly and deterministically instead of inheriting a large
// configured timeout and retry count. It sits just above the client's constant
// 10s per-request timeout so one attempt is never cut short.
const probeBudget = 15 * time.Second

// defaultModelPattern is the shape a Jev model id normally takes. A default
// model that does not match is only a warning, since a new id could appear.
var defaultModelPattern = regexp.MustCompile(`^(~?typesafe/)?jev-`)

// Evaluator is the narrow interface the probe needs, satisfied by *jev.Client,
// so a test can drive doctor without a network call.
type Evaluator interface {
	Evaluate(ctx context.Context, req jev.Request) (*jev.Result, error)
}

// ClientFactory builds an Evaluator for one provider. Run uses it only for
// -probe; a nil factory means the real per-provider client.
type ClientFactory func(p jev.Provider) (Evaluator, error)

// status is a check outcome. FAIL sets a non-zero exit; WARN does not.
type status string

const (
	statusPass status = "PASS"
	statusWarn status = "WARN"
	statusFail status = "FAIL"
)

// check is one reported line.
type check struct {
	status status
	name   string
	detail string
}

// Run performs the preflight checks and writes the report to out, reading the
// environment through getenv (pass os.Getenv in production). When probe is true
// it makes one live decision call per configured provider. newClient is
// injected for tests; pass nil for the real client. Run returns the process
// exit code: 1 if any check failed, 0 otherwise (a warning does not fail). A
// usage error (a bad flag) is the caller's concern, not Run's.
func Run(ctx context.Context, out io.Writer, getenv func(string) string, probe bool, newClient ClientFactory) int {
	cfg, cfgErr := config.Resolve(getenv)

	writeSettings(out, &cfg)

	var checks []check
	if cfgErr != nil {
		checks = append(checks, check{statusFail, "config", cfgErr.Error()})
	} else {
		checks = append(checks, check{statusPass, "config", "all settings parsed"})
	}

	providers, selErr := config.Select(&cfg)
	if selErr != nil {
		checks = append(checks, check{statusFail, "providers", selErr.Error()})
	} else {
		checks = append(checks, check{statusPass, "providers", describeProviders(providers, cfg.DefaultModel)})
	}

	checks = append(checks,
		credentialFormatCheck(&cfg),
		defaultModelCheck(cfg.DefaultModel),
		httpTokenCheck(&cfg),
	)

	if probe && cfgErr == nil && selErr == nil {
		if newClient == nil {
			newClient = defaultClientFactory()
		}
		for i := range providers {
			checks = append(checks, probeCheck(ctx, &providers[i], cfg.DefaultModel, newClient))
		}
	}

	return report(out, checks)
}

// report prints each check and returns the exit code: 1 if any check failed.
func report(out io.Writer, checks []check) int {
	failed := false
	_, _ = fmt.Fprintln(out)
	for _, c := range checks {
		_, _ = fmt.Fprintf(out, "[%s] %s: %s\n", c.status, c.name, c.detail)
		if c.status == statusFail {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

// writeSettings prints the non-secret settings with their sources and the
// set/unset state of each credential. It never prints a secret value.
func writeSettings(out io.Writer, cfg *config.Config) {
	_, _ = fmt.Fprintln(out, "settings:")
	line := func(name, value, source string) {
		_, _ = fmt.Fprintf(out, "  %s=%s (%s)\n", name, value, source)
	}
	line("provider", string(cfg.Provider), cfg.Sources[config.SourceProvider])
	line("typesafe_base_url", effectiveBaseURL(cfg.TypeSafeBaseURL, provider.DefaultTypeSafeBaseURL), cfg.Sources[config.SourceTypeSafeBaseURL])
	line("openrouter_base_url", effectiveBaseURL(cfg.OpenRouterBaseURL, provider.DefaultOpenRouterBaseURL), cfg.Sources[config.SourceOpenRouterBaseURL])
	line("default_model", cfg.DefaultModel, cfg.Sources[config.SourceDefaultModel])
	line("timeout", cfg.Timeout.String(), cfg.Sources[config.SourceTimeout])
	line("max_retries", strconv.Itoa(cfg.MaxRetries), cfg.Sources[config.SourceMaxRetries])
	line("fallback", strconv.FormatBool(cfg.Fallback), cfg.Sources[config.SourceFallback])
	_, _ = fmt.Fprintf(out, "  typesafe_key=%s\n", setState(cfg.TypeSafeKey))
	_, _ = fmt.Fprintf(out, "  openrouter_key=%s\n", setState(cfg.OpenRouterKey))
	_, _ = fmt.Fprintf(out, "  http_token=%s\n", setState(cfg.HTTPToken))
}

// effectiveBaseURL renders the base URL in effect: the override when set, else
// the provider default. Showing the resolved URL is clearer than the word
// "default" beside a "(default)" source.
func effectiveBaseURL(override, def string) string {
	if override == "" {
		return def
	}
	return override
}

// setState reports a secret as set or unset without revealing it.
func setState(secret string) string {
	if secret == "" {
		return "unset"
	}
	return "set"
}

// describeProviders renders the ordered provider list with each endpoint and the
// example model id after per-provider normalisation.
func describeProviders(providers []jev.Provider, model string) string {
	parts := make([]string, 0, len(providers))
	for i := range providers {
		p := &providers[i]
		endpoint, err := p.Endpoint()
		if err != nil {
			endpoint = "invalid endpoint: " + err.Error()
		}
		shown := model
		if p.ModelID != nil {
			shown = p.ModelID(model)
		}
		parts = append(parts, fmt.Sprintf("%s -> %s (model %s)", p.Name, endpoint, shown))
	}
	return strings.Join(parts, "; ")
}

// namedSecret pairs a credential's display name with its value for the format
// check. The value is inspected but never printed.
type namedSecret struct {
	name  string
	value string
}

// credentialFormatCheck warns when a set credential has surrounding whitespace, an
// embedded quote, or a control character: the usual copy-paste damage. It
// covers the HTTP bearer token as well as the two API keys; the token is
// checked after [config.NormalizeHTTPToken], since serve trims it, and a blank
// token is left to [httpTokenCheck]. It never prints a value.
func credentialFormatCheck(cfg *config.Config) check {
	token, err := config.NormalizeHTTPToken(cfg.HTTPToken)
	if err != nil {
		token = ""
	}
	secrets := []namedSecret{
		{config.EnvTypeSafeKey, cfg.TypeSafeKey},
		{config.EnvOpenRouterKey, cfg.OpenRouterKey},
		{config.EnvHTTPToken, token},
	}
	var warnings []string
	for _, s := range secrets {
		if w := tokenFormatWarning(s.name, s.value); w != "" {
			warnings = append(warnings, w)
		}
	}
	if len(warnings) > 0 {
		return check{statusWarn, "credential format", strings.Join(warnings, "; ")}
	}
	return check{statusPass, "credential format", "no formatting problems in set credentials"}
}

// tokenFormatWarning returns a description of the format problems in value, or
// "" when value is unset or clean. It never includes the value.
func tokenFormatWarning(name, value string) string {
	if value == "" {
		return ""
	}
	var issues []string
	if strings.TrimSpace(value) != value {
		issues = append(issues, "surrounding whitespace")
	}
	if strings.ContainsAny(value, `"'`) {
		issues = append(issues, "an embedded quote")
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		issues = append(issues, "a control character")
	}
	if len(issues) == 0 {
		return ""
	}
	return fmt.Sprintf("%s has %s", name, strings.Join(issues, " and "))
}

// defaultModelCheck warns when the default model does not look like a Jev id.
func defaultModelCheck(model string) check {
	if defaultModelPattern.MatchString(model) {
		return check{statusPass, "default model", model}
	}
	return check{statusWarn, "default model", fmt.Sprintf("%q does not look like a Jev model id (expected e.g. jev-latest)", model)}
}

// httpTokenCheck reports whether the HTTP bearer token is set. It never fails,
// since stdio mode needs no token; a blank token, which HTTP serve mode refuses
// to start with, is a warning.
func httpTokenCheck(cfg *config.Config) check {
	const name = "http token"
	if _, err := config.NormalizeHTTPToken(cfg.HTTPToken); err != nil {
		return check{statusWarn, name, config.EnvHTTPToken + " is set but only whitespace; HTTP serve mode would refuse to start unless -http-token is given"}
	}
	if cfg.HTTPToken == "" {
		return check{statusPass, name, "unset (HTTP serve mode would be unauthenticated)"}
	}
	return check{statusPass, name, "set"}
}

// probeCheck makes one live noul call against p and reports the outcome. A
// success reports the echoed model, latency, token usage, and any response id;
// a failure reports the error, including a provider request id when present.
func probeCheck(ctx context.Context, p *jev.Provider, model string, newClient ClientFactory) check {
	name := "probe " + p.Name
	client, err := newClient(*p)
	if err != nil {
		return check{statusFail, name, "building client: " + err.Error()}
	}
	res, err := client.Evaluate(ctx, probeRequest(model))
	if err != nil {
		return check{statusFail, name, probeErrorDetail(err)}
	}
	detail := fmt.Sprintf("model=%s latency=%s tokens=%d/%d",
		res.Model, res.Latency.Round(time.Millisecond), res.Usage.InputTokens, res.Usage.OutputTokens)
	if res.ID != "" {
		detail += " id=" + res.ID
	}
	return check{statusPass, name, detail}
}

// probeRequest is the minimal, valid noul request used for reachability. It
// passes [jev.Validate]: a non-empty JSON state, one noul question, non-blank
// instructions, and no criteria (optional for noul).
func probeRequest(model string) jev.Request {
	return jev.Request{
		Model: model,
		State: json.RawMessage(`"jev-mcp doctor probe"`),
		Questions: map[string]jev.Question{
			"reachable": {
				Type:         jev.TypeNoul,
				Instructions: json.RawMessage(`"Is this API reachable? Answer yes."`),
			},
		},
	}
}

// probeErrorDetail renders a probe failure. A provider request id, when the
// error carried one, is already part of [jev.APIError]'s message, so returning
// the error string surfaces it without repeating it; that id is what provider
// support needs to trace a failure.
func probeErrorDetail(err error) string {
	return err.Error()
}

// defaultClientFactory builds a real per-provider client for the probe with a
// fixed short budget and no retries.
func defaultClientFactory() ClientFactory {
	return func(p jev.Provider) (Evaluator, error) {
		return jev.New([]jev.Provider{p}, jev.WithBudget(probeBudget), jev.WithMaxRetries(0))
	}
}
