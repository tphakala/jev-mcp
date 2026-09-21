package provider_test

import (
	"testing"

	"github.com/tphakala/jev-mcp/internal/jev"
	"github.com/tphakala/jev-mcp/internal/provider"
)

func TestTypeSafeProvider(t *testing.T) {
	t.Parallel()

	p := provider.TypeSafe("", "key-ts")
	if p.Name != jev.ProviderTypeSafe {
		t.Errorf("Name = %q, want %q", p.Name, jev.ProviderTypeSafe)
	}
	if p.BaseURL != provider.DefaultTypeSafeBaseURL {
		t.Errorf("BaseURL = %q, want the default %q", p.BaseURL, provider.DefaultTypeSafeBaseURL)
	}
	if p.Path != jev.SystemOnePath {
		t.Errorf("Path = %q, want %q", p.Path, jev.SystemOnePath)
	}
	if p.APIKey != "key-ts" {
		t.Errorf("APIKey = %q, want key-ts", p.APIKey)
	}
	got, err := p.Endpoint()
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if want := "https://api.typesafe.ai/v1/systemone"; got != want {
		t.Errorf("Endpoint = %q, want %q", got, want)
	}
}

func TestOpenRouterProvider(t *testing.T) {
	t.Parallel()

	p := provider.OpenRouter("", "key-or")
	if p.Name != jev.ProviderOpenRouter {
		t.Errorf("Name = %q, want %q", p.Name, jev.ProviderOpenRouter)
	}
	if p.BaseURL != provider.DefaultOpenRouterBaseURL {
		t.Errorf("BaseURL = %q, want the default %q", p.BaseURL, provider.DefaultOpenRouterBaseURL)
	}
	got, err := p.Endpoint()
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if want := "https://openrouter.ai/api/v1/systemone"; got != want {
		t.Errorf("Endpoint = %q, want %q", got, want)
	}
}

func TestCustomBaseURL(t *testing.T) {
	t.Parallel()

	p := provider.TypeSafe("https://proxy.internal.test", "k")
	if p.BaseURL != "https://proxy.internal.test" {
		t.Errorf("BaseURL = %q, want the override", p.BaseURL)
	}
}

func TestModelIDNormalisation(t *testing.T) {
	t.Parallel()

	ts := provider.TypeSafe("", "")
	tsCases := map[string]string{
		"typesafe/jev-1.13":    "jev-1.13",   // strip the plain prefix
		"~typesafe/jev-latest": "jev-latest", // strip the tilde prefix
		"jev-1.13.0":           "jev-1.13.0", // already bare, unchanged
		"jev-latest":           "jev-latest",
	}
	for in, want := range tsCases {
		if got := ts.ModelID(in); got != want {
			t.Errorf("typesafe ModelID(%q) = %q, want %q", in, got, want)
		}
	}

	or := provider.OpenRouter("", "")
	orCases := map[string]string{
		"jev-1.13":          "jev-1.13", // bare id passes through (OpenRouter maps it)
		"jev-latest":        "jev-latest",
		"typesafe/jev-1.13": "typesafe/jev-1.13", // a prefixed id is left as given
	}
	for in, want := range orCases {
		if got := or.ModelID(in); got != want {
			t.Errorf("openrouter ModelID(%q) = %q, want %q", in, got, want)
		}
	}
}
