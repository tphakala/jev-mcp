package config

import (
	"fmt"

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
// Each key is passed through [NormalizeAPIKey] before it is used, so a key
// pasted with a trailing newline still works. A key that is set but blank, or
// that holds a control character, is an error ([ErrBlankAPIKey] or
// [ErrInvalidAPIKey], naming the variable) whenever that provider would be
// used: it is a credential problem, reported before any request rather than
// as a transport failure on every call. In auto mode a blank key still counts
// as set, so it is reported instead of silently selecting the other provider.
//
// Select builds the [jev.Provider] values through the provider constructors, so
// base URLs and model-id normalisation stay in one place. It reads no
// environment and makes no network call.
func Select(cfg *Config) ([]jev.Provider, error) {
	typeSafe := func() (jev.Provider, error) {
		key, err := apiKey(EnvTypeSafeKey, cfg.TypeSafeKey)
		return provider.TypeSafe(cfg.TypeSafeBaseURL, key), err
	}
	openRouter := func() (jev.Provider, error) {
		key, err := apiKey(EnvOpenRouterKey, cfg.OpenRouterKey)
		return provider.OpenRouter(cfg.OpenRouterBaseURL, key), err
	}

	switch cfg.Provider {
	case ProviderTypeSafe:
		if cfg.TypeSafeKey == "" {
			return nil, ErrProviderKeyMissing
		}
		return build(typeSafe)

	case ProviderOpenRouter:
		if cfg.OpenRouterKey == "" {
			return nil, ErrProviderKeyMissing
		}
		return build(openRouter)

	case ProviderAuto:
		switch {
		case cfg.TypeSafeKey != "" && cfg.OpenRouterKey != "":
			if cfg.Fallback {
				return build(typeSafe, openRouter)
			}
			return build(typeSafe)
		case cfg.TypeSafeKey != "":
			return build(typeSafe)
		case cfg.OpenRouterKey != "":
			return build(openRouter)
		default:
			return nil, ErrNoAPIKey
		}

	default:
		// A mode outside the three constants should have been rejected by Resolve;
		// treat it as invalid rather than silently picking a provider.
		return nil, ErrInvalidProvider
	}
}

// apiKey normalizes the key read from the variable name, naming the variable
// in the error.
func apiKey(name, raw string) (string, error) {
	key, err := NormalizeAPIKey(raw)
	if err != nil {
		return "", fmt.Errorf("%w (%s)", err, name)
	}
	return key, nil
}

// build calls each constructor in order and returns the providers, or the
// first error.
func build(ctors ...func() (jev.Provider, error)) ([]jev.Provider, error) {
	out := make([]jev.Provider, 0, len(ctors))
	for _, ctor := range ctors {
		p, err := ctor()
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
