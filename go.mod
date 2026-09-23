module github.com/tphakala/jev-mcp

go 1.27.0

require (
	github.com/google/jsonschema-go v0.4.3
	github.com/modelcontextprotocol/go-sdk v1.8.0
	// Ruleguard DSL backs the custom gocritic ruleguard matchers in rules/*.go.
	// Those files carry the `ruleguard` build tag, so the normal toolchain never
	// compiles them; `go mod tidy` still keeps the requirement because tidy
	// considers every build tag.
	github.com/quasilyte/go-ruleguard/dsl v0.3.23
)

require (
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)
