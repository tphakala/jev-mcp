// Package provider builds concrete [jev.Provider] values for the supported
// backends. It is the one place base URLs, paths, and model-id normalisation
// meet. It reads no environment and makes no network call; turning a resolved
// configuration into an ordered provider list is the caller's job.
package provider

import (
	"strings"

	"github.com/tphakala/jev-mcp/internal/jev"
)

// Default base URLs for the two providers.
const (
	// DefaultTypeSafeBaseURL is the native TypeSafe API base.
	DefaultTypeSafeBaseURL = "https://api.typesafe.ai"
	// DefaultOpenRouterBaseURL is the OpenRouter API base; the System One path
	// is joined onto it.
	DefaultOpenRouterBaseURL = "https://openrouter.ai/api"
)

// TypeSafe returns the native TypeSafe provider. An empty baseURL uses
// [DefaultTypeSafeBaseURL].
func TypeSafe(baseURL, apiKey string) jev.Provider {
	if baseURL == "" {
		baseURL = DefaultTypeSafeBaseURL
	}
	return jev.Provider{
		Name:    jev.ProviderTypeSafe,
		BaseURL: baseURL,
		Path:    jev.SystemOnePath,
		APIKey:  apiKey,
		ModelID: normaliseTypeSafeModel,
	}
}

// OpenRouter returns the OpenRouter provider. An empty baseURL uses
// [DefaultOpenRouterBaseURL].
func OpenRouter(baseURL, apiKey string) jev.Provider {
	if baseURL == "" {
		baseURL = DefaultOpenRouterBaseURL
	}
	return jev.Provider{
		Name:    jev.ProviderOpenRouter,
		BaseURL: baseURL,
		Path:    jev.SystemOnePath,
		APIKey:  apiKey,
		ModelID: normaliseOpenRouterModel,
	}
}

// normaliseTypeSafeModel strips an OpenRouter-style typesafe/ or ~typesafe/
// prefix, since the native API names models without it (jev-latest,
// jev-1.13.0). An id with no such prefix passes through unchanged.
func normaliseTypeSafeModel(id string) string {
	if s, ok := strings.CutPrefix(id, "~typesafe/"); ok {
		return s
	}
	if s, ok := strings.CutPrefix(id, "typesafe/"); ok {
		return s
	}
	return id
}

// normaliseOpenRouterModel passes the id through unchanged: OpenRouter maps a
// bare id server-side (jev-1.13 to typesafe/jev-1.13, jev-latest to
// ~typesafe/jev-latest) and a prefixed id passes through, so pre-mapping here
// would break future ids.
func normaliseOpenRouterModel(id string) string {
	return id
}
