package jev_test

import (
	"testing"

	"github.com/tphakala/jev-mcp/internal/jev"
)

func TestProviderEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base string
		path string
		want string
	}{
		{"typesafe", "https://api.typesafe.ai", jev.SystemOnePath, "https://api.typesafe.ai/v1/systemone"},
		{"openrouter", "https://openrouter.ai/api", jev.SystemOnePath, "https://openrouter.ai/api/v1/systemone"},
		{"trailing slash base", "https://host.test/", jev.SystemOnePath, "https://host.test/v1/systemone"},
		// A base ending in /api against a path beginning with /api must not double
		// the segment.
		{"api dedup", "https://host.test/api", "/api/alpha/decisions", "https://host.test/api/alpha/decisions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := jev.Provider{Name: "p", BaseURL: tt.base, Path: tt.path}
			got, err := p.Endpoint()
			if err != nil {
				t.Fatalf("Endpoint() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Endpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}
