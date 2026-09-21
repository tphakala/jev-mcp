# jev-mcp

[![CI](https://github.com/tphakala/jev-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/tphakala/jev-mcp/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/tphakala/jev-mcp.svg)](https://pkg.go.dev/github.com/tphakala/jev-mcp)
[![codecov](https://codecov.io/gh/tphakala/jev-mcp/branch/main/graph/badge.svg)](https://codecov.io/gh/tphakala/jev-mcp)
[![Go Version](https://img.shields.io/github/go-mod/go-version/tphakala/jev-mcp)](go.mod)
[![Latest release](https://img.shields.io/github/v/release/tphakala/jev-mcp?sort=semver&label=release)](https://github.com/tphakala/jev-mcp/releases/latest)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/tphakala/jev-mcp/badge)](https://scorecard.dev/viewer/?uri=github.com/tphakala/jev-mcp)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)

jev-mcp is an MCP server that gives an agent typed, probabilistic decisions from TypeSafe's Jev "System One" model, over the TypeSafe API or OpenRouter. Jev answers structured choice, score, and yes/no questions about program state in one fast pass, so an agent can route, classify, gate, or rank without a text model inventing free-form output.

## Install

```
go install github.com/tphakala/jev-mcp/cmd/jev-mcp@latest
```

Requires Go 1.27 or later. Binaries for linux, darwin and windows are attached to every [release](https://github.com/tphakala/jev-mcp/releases/latest); a container image is published to `ghcr.io/tphakala/jev-mcp`.

## Status

Pre-release and under active construction. This build implements only the `-version` command; the Jev HTTP client, the `jev_evaluate` MCP tool, and the stdio and HTTP serve modes are landing incrementally. Nothing here is stable yet.

## Usage

```
jev-mcp -version
```

Serve and doctor commands, the tool schema, and a Claude Code configuration snippet are documented here as they land.

## Development

```
task check   # build, vet (amd64/arm64), tidy check, lint, ruleguard probe, race tests
task tools   # install the pinned golangci-lint and govulncheck
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow and [SECURITY.md](SECURITY.md) for reporting vulnerabilities.

## License

Apache License 2.0, see [LICENSE](LICENSE).
