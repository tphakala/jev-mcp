package config_test

import (
	"errors"
	"testing"

	"github.com/tphakala/jev-mcp/internal/config"
	"github.com/tphakala/jev-mcp/internal/jev"
	"github.com/tphakala/jev-mcp/internal/provider"
)

// names returns the provider Name of each entry, in order, so a test can assert
// the fallback ordering without depending on the other Provider fields.
func names(ps []jev.Provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func TestSelect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       config.Config
		wantErr   error
		wantOrder []string
	}{
		{
			name:      "explicit typesafe",
			cfg:       config.Config{Provider: config.ProviderTypeSafe, TypeSafeKey: "ts"},
			wantOrder: []string{jev.ProviderTypeSafe},
		},
		{
			name:    "explicit typesafe without key",
			cfg:     config.Config{Provider: config.ProviderTypeSafe},
			wantErr: config.ErrProviderKeyMissing,
		},
		{
			name:      "explicit openrouter",
			cfg:       config.Config{Provider: config.ProviderOpenRouter, OpenRouterKey: "or"},
			wantOrder: []string{jev.ProviderOpenRouter},
		},
		{
			name:    "explicit openrouter without key",
			cfg:     config.Config{Provider: config.ProviderOpenRouter},
			wantErr: config.ErrProviderKeyMissing,
		},
		{
			name: "explicit typesafe ignores an openrouter key",
			cfg: config.Config{
				Provider:      config.ProviderTypeSafe,
				OpenRouterKey: "or",
			},
			wantErr: config.ErrProviderKeyMissing,
		},
		{
			name: "auto both keys with fallback",
			cfg: config.Config{
				Provider:      config.ProviderAuto,
				TypeSafeKey:   "ts",
				OpenRouterKey: "or",
				Fallback:      true,
			},
			wantOrder: []string{jev.ProviderTypeSafe, jev.ProviderOpenRouter},
		},
		{
			name: "auto both keys without fallback",
			cfg: config.Config{
				Provider:      config.ProviderAuto,
				TypeSafeKey:   "ts",
				OpenRouterKey: "or",
				Fallback:      false,
			},
			wantOrder: []string{jev.ProviderTypeSafe},
		},
		{
			name:      "auto typesafe only",
			cfg:       config.Config{Provider: config.ProviderAuto, TypeSafeKey: "ts", Fallback: true},
			wantOrder: []string{jev.ProviderTypeSafe},
		},
		{
			name:      "auto openrouter only",
			cfg:       config.Config{Provider: config.ProviderAuto, OpenRouterKey: "or", Fallback: true},
			wantOrder: []string{jev.ProviderOpenRouter},
		},
		{
			name:    "auto no key",
			cfg:     config.Config{Provider: config.ProviderAuto, Fallback: true},
			wantErr: config.ErrNoAPIKey,
		},
		{
			name:    "unknown mode",
			cfg:     config.Config{Provider: config.ProviderMode("azure"), TypeSafeKey: "ts"},
			wantErr: config.ErrInvalidProvider,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := config.Select(&tt.cfg)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want errors.Is %v", err, tt.wantErr)
				}
				if got != nil {
					t.Errorf("providers = %v, want nil on error", names(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err = %v", err)
			}
			gotOrder := names(got)
			if len(gotOrder) != len(tt.wantOrder) {
				t.Fatalf("order = %v, want %v", gotOrder, tt.wantOrder)
			}
			for i := range tt.wantOrder {
				if gotOrder[i] != tt.wantOrder[i] {
					t.Fatalf("order = %v, want %v", gotOrder, tt.wantOrder)
				}
			}
		})
	}
}

// TestSelectWiresKeysBaseURLsAndModelID confirms Select passes the configured
// key and base URL through to the constructor and that each provider's model-id
// normalisation is wired: TypeSafe strips a typesafe/ prefix, OpenRouter does
// not.
func TestSelectWiresKeysBaseURLsAndModelID(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Provider:          config.ProviderAuto,
		TypeSafeKey:       "ts-secret",
		OpenRouterKey:     "or-secret",
		TypeSafeBaseURL:   "https://ts.example",
		OpenRouterBaseURL: "https://or.example/api",
		Fallback:          true,
	}
	got, err := config.Select(&cfg)
	if err != nil {
		t.Fatalf("unexpected err = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 providers, got %d", len(got))
	}

	ts, or := got[0], got[1]
	if ts.APIKey != "ts-secret" {
		t.Errorf("typesafe APIKey = %q, want ts-secret", ts.APIKey)
	}
	if or.APIKey != "or-secret" {
		t.Errorf("openrouter APIKey = %q, want or-secret", or.APIKey)
	}
	if ts.BaseURL != "https://ts.example" {
		t.Errorf("typesafe BaseURL = %q, want the override", ts.BaseURL)
	}
	if or.BaseURL != "https://or.example/api" {
		t.Errorf("openrouter BaseURL = %q, want the override", or.BaseURL)
	}

	tsEndpoint, err := ts.Endpoint()
	if err != nil {
		t.Fatalf("typesafe Endpoint: %v", err)
	}
	if want := "https://ts.example" + jev.SystemOnePath; tsEndpoint != want {
		t.Errorf("typesafe Endpoint = %q, want %q", tsEndpoint, want)
	}

	// ModelID normalisation is wired per provider.
	if ts.ModelID == nil || or.ModelID == nil {
		t.Fatal("ModelID not wired on a provider")
	}
	if got := ts.ModelID("typesafe/jev-1.13"); got != "jev-1.13" {
		t.Errorf("typesafe ModelID(typesafe/jev-1.13) = %q, want jev-1.13", got)
	}
	// Use a prefixed input the TypeSafe normaliser WOULD change, so this pins the
	// OpenRouter passthrough rather than a value that is a fixed point of both.
	if got := or.ModelID("typesafe/jev-1.13"); got != "typesafe/jev-1.13" {
		t.Errorf("openrouter ModelID(typesafe/jev-1.13) = %q, want typesafe/jev-1.13 (passthrough)", got)
	}
}

// TestSelectDefaultBaseURLWhenUnset confirms an empty base URL in the config
// yields the provider default via the constructor.
func TestSelectDefaultBaseURLWhenUnset(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Provider: config.ProviderTypeSafe, TypeSafeKey: "ts"}
	got, err := config.Select(&cfg)
	if err != nil {
		t.Fatalf("unexpected err = %v", err)
	}
	if got[0].BaseURL != provider.DefaultTypeSafeBaseURL {
		t.Errorf("BaseURL = %q, want default %q", got[0].BaseURL, provider.DefaultTypeSafeBaseURL)
	}
}
