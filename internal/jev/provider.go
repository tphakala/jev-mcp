package jev

import (
	"fmt"
	"net/url"
	"strings"
)

// Provider names identify the two supported backends.
const (
	// ProviderTypeSafe is the native TypeSafe Decisions API.
	ProviderTypeSafe = "typesafe"
	// ProviderOpenRouter is the OpenRouter System One endpoint.
	ProviderOpenRouter = "openrouter"
)

// SystemOnePath is the Decisions endpoint path, identical on both providers.
const SystemOnePath = "/v1/systemone"

// Provider is one backend the client can call. Both backends share the wire
// codec and differ only in data: base URL, path, bearer key, model-id
// normalisation, and any extra headers.
type Provider struct {
	// Name is the short backend name, e.g. [ProviderTypeSafe].
	Name string
	// BaseURL is the scheme and host, without a trailing slash.
	BaseURL string
	// Path is the request path, normally [SystemOnePath].
	Path string
	// APIKey is the bearer token sent as Authorization.
	APIKey string
	// Headers are optional extra request headers.
	Headers map[string]string
	// ModelID normalises a caller-supplied model id for this backend. A nil
	// ModelID passes the id through unchanged.
	ModelID func(string) string
}

// Endpoint joins BaseURL and Path into the full request URL. It collapses a
// doubled "/api" segment (a base ending in /api against a path beginning with
// /api/) so a base override cannot produce https://host/api/api/....
func (p *Provider) Endpoint() (string, error) {
	base := strings.TrimRight(p.BaseURL, "/")
	path := p.Path
	if strings.HasSuffix(base, "/api") && strings.HasPrefix(path, "/api/") {
		path = strings.TrimPrefix(path, "/api")
	}
	u, err := url.JoinPath(base, path)
	if err != nil {
		return "", fmt.Errorf("jev: provider %q endpoint: %w", p.Name, err)
	}
	return u, nil
}
