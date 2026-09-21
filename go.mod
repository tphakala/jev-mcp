module github.com/tphakala/jev-mcp

go 1.27.0

// Ruleguard DSL backs the custom gocritic ruleguard matchers in rules/*.go.
// Those files carry the `ruleguard` build tag, so the normal toolchain never
// compiles them; `go mod tidy` still keeps the requirement because tidy
// considers every build tag.
require github.com/quasilyte/go-ruleguard/dsl v0.3.23
