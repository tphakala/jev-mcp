package config

import (
	"github.com/tphakala/jev-mcp/internal/jev"
	"github.com/tphakala/jev-mcp/internal/provider"
)

// Select turns a resolved [Config] into the ordered list of providers the
// client will try. The order is the fallback order: the first provider is the
// primary and any later one is tried only when the primary cannot answer.
//
// It is where a missing API key becomes an error, since key presence decides
// which providers can be built. In auto mode the order follows which keys are
// set: with both keys, TypeSafe is primary and OpenRouter is the fallback
// (dropped when [Config.Fallback] is false); with one key, only that provider
// is used; with neither, [ErrNoAPIKey]. An explicit mode requires that
// provider's own key and yields [ErrProviderKeyMissing] when it is absent.
//
// Select builds the [jev.Provider] values through the provider constructors, so
// base URLs and model-id normalisation stay in one place. It reads no
// environment and makes no network call.
func Select(cfg *Config) ([]jev.Provider, error) {
	typeSafe := func() jev.Provider { return provider.TypeSafe(cfg.TypeSafeBaseURL, cfg.TypeSafeKey) }
	openRouter := func() jev.Provider { return provider.OpenRouter(cfg.OpenRouterBaseURL, cfg.OpenRouterKey) }

	switch cfg.Provider {
	case ProviderTypeSafe:
		if cfg.TypeSafeKey == "" {
			return nil, ErrProviderKeyMissing
		}
		return []jev.Provider{typeSafe()}, nil

	case ProviderOpenRouter:
		if cfg.OpenRouterKey == "" {
			return nil, ErrProviderKeyMissing
		}
		return []jev.Provider{openRouter()}, nil

	case ProviderAuto:
		switch {
		case cfg.TypeSafeKey != "" && cfg.OpenRouterKey != "":
			if cfg.Fallback {
				return []jev.Provider{typeSafe(), openRouter()}, nil
			}
			return []jev.Provider{typeSafe()}, nil
		case cfg.TypeSafeKey != "":
			return []jev.Provider{typeSafe()}, nil
		case cfg.OpenRouterKey != "":
			return []jev.Provider{openRouter()}, nil
		default:
			return nil, ErrNoAPIKey
		}

	default:
		// A mode outside the three constants should have been rejected by Resolve;
		// treat it as invalid rather than silently picking a provider.
		return nil, ErrInvalidProvider
	}
}
