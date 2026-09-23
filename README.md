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

Requires Go 1.27 or later. Binaries for linux, darwin and windows are attached to every [release](https://github.com/tphakala/jev-mcp/releases/latest), and a container image for linux/amd64 and linux/arm64 is published to `ghcr.io/tphakala/jev-mcp` for each release tag.

## Quick start

Get an API key from TypeSafe (or use an OpenRouter key), then check the setup:

```
export TYPESAFE_API_KEY=...
jev-mcp doctor -probe
```

`doctor` prints the resolved settings and where each came from, and `-probe` makes one small live call per configured provider. Credentials are never printed.

Add the server to Claude Code:

```
claude mcp add jev -e TYPESAFE_API_KEY=your-key -- jev-mcp
```

Any MCP client that launches stdio servers works the same way; in the usual `mcpServers` JSON form:

```json
{
  "mcpServers": {
    "jev": {
      "command": "jev-mcp",
      "env": { "TYPESAFE_API_KEY": "your-key" }
    }
  }
}
```

With the container image instead of a local binary, keep stdin open with `-i` (images are tagged with the release version):

```
docker run -i --rm -e TYPESAFE_API_KEY ghcr.io/tphakala/jev-mcp:0.1.0
```

## Usage

```
jev-mcp                        # serve MCP over stdio (the default)
jev-mcp -http 127.0.0.1:8765   # serve Streamable HTTP on a loopback address
jev-mcp doctor [-probe]        # check the configuration and providers
jev-mcp -version
```

Serve flags:

| Flag | Meaning |
| --- | --- |
| `-http addr` | Serve Streamable HTTP on `addr` instead of stdio. The host must be loopback (`127.0.0.1`, `::1`, or a name that resolves only to loopback); anything else is refused. |
| `-http-token token` | Require `Authorization: Bearer <token>` in HTTP mode. Overrides `JEV_MCP_HTTP_TOKEN`; an explicit empty value forces unauthenticated. Trimmed and checked like the variable. |
| `-log-level level` | `debug`, `info` (default), `warn`, or `error`. |
| `-log-json` | Write logs as JSON instead of text. |

Logs go to stderr; in stdio mode stdout carries only the MCP stream.

In HTTP mode point the client at the listen address, for example `http://127.0.0.1:8765/`. Cross-origin browser POST requests are rejected. Because the bind is loopback only, a container can serve HTTP only with `--network host`; stdio is the supported container mode.

## Configuration

Everything is read from the environment. `TYPESAFE_API_KEY`, `TYPESAFE_BASE_URL`, and `TYPESAFE_DEFAULT_MODEL` match the official TypeSafe SDK, so an existing setup keeps working.

| Variable | Default | Meaning |
| --- | --- | --- |
| `TYPESAFE_API_KEY` | | TypeSafe API key. |
| `OPENROUTER_API_KEY` | | OpenRouter API key. |
| `JEV_MCP_PROVIDER` | `auto` | `auto`, `typesafe`, or `openrouter`. `auto` uses TypeSafe when its key is set, else OpenRouter; with both keys set, TypeSafe is primary and OpenRouter the fallback. |
| `JEV_MCP_FALLBACK` | `true` | Whether `auto` mode falls back to the second provider. |
| `JEV_MCP_DEFAULT_MODEL` | `jev-latest` | Model used when a call does not name one. `TYPESAFE_DEFAULT_MODEL` is read when this is unset. |
| `JEV_MCP_TIMEOUT` | `30s` | Budget for one tool call across all retries and the fallback, as a Go duration. |
| `JEV_MCP_MAX_RETRIES` | `2` | Retries per provider after the first attempt. |
| `JEV_MCP_TYPESAFE_BASE_URL` | `https://api.typesafe.ai` | TypeSafe base URL. `TYPESAFE_BASE_URL` is read when this is unset. |
| `JEV_MCP_OPENROUTER_BASE_URL` | `https://openrouter.ai/api` | OpenRouter base URL. |
| `JEV_MCP_HTTP_TOKEN` | | Bearer token for HTTP mode. Leading and trailing spaces, tabs, and line breaks are trimmed; HTTP mode refuses to start when nothing else is left. Stdio mode ignores it. |

At least one API key is required to serve. Rate limits (429), overload (503, 529), other server errors, request timeouts (408), and transport failures are retried with jittered exponential backoff, honouring `Retry-After` and `Retry-After-Ms` up to a 20 second wait. In `auto` mode with both keys, those failures and a rejected key move on to the fallback provider; a request the provider rejects as invalid does not, since it would fail there too.

## The `jev_evaluate` tool

One tool, `jev_evaluate`, decides one or more questions over a shared state in a single call.

| Input | Meaning |
| --- | --- |
| `state` | What to decide over: a string, or a JSON object or array. Text only, at most 1 MiB encoded. |
| `questions` | 1 to 64 questions, each `{name, type, instructions, criteria}`. Names must be unique, at most 128 bytes, and free of control characters. `instructions` is the question to decide: usually a string, or a non-empty object or array. |
| `model` | Optional Jev model id, such as `jev-latest` or `jev-1.13.0`. |
| `detail` | `summary` (default) or `full`; controls the text result only. |

Question types:

- `choice` picks one option. `criteria` is an object of 2 to 255 option names to descriptions; a description may be a string, an object, or `null`.
- `score` places the state on an ordered scale. `criteria` is an array of 2 to 10 level descriptions, lowest first; a level may be a string or an object.
- `noul` answers a yes/no proposition. `criteria` is optional: an object with `true` and `false` descriptions. The answer is the probability that the proposition is true.

Example arguments:

```json
{
  "state": {"ticket": "My card was charged twice for the same order, please refund one", "customer_tier": "gold"},
  "questions": [
    {"name": "route", "type": "choice", "instructions": "Which team should handle this ticket?",
     "criteria": {"billing": "payments and refunds", "shipping": null, "tech": "app bugs"}},
    {"name": "urgency", "type": "score", "instructions": "How urgent is this ticket?",
     "criteria": ["low", "medium", "high"]},
    {"name": "refund", "type": "noul", "instructions": "The customer is asking for a refund."}
  ]
}
```

The default text result is a compact summary, answers in question order:

```json
{"answers":[{"name":"route","choice":"billing","confidence":1},{"name":"urgency","score":1.27,"confidence":0.52},{"name":"refund","noul":0.98}]}
```

The structured result always carries the full output: per answer the `type` and the decision, for a choice or score also `confidence` and `probabilities`, and for a score the `legend`; an answer this server cannot read comes back verbatim under `raw`. Each call also reports `provider`, `model`, token `usage` (with `cost` when the provider reports it), `latency_ms`, and `attempts`. With `detail` set to `full`, the text result carries the same full output, for clients that show the model only the text.

State and instructions are sent to the provider's API, so do not put secrets in them.

## Development

```
task check   # build, vet (amd64/arm64), tidy check, lint, ruleguard probe, race tests
task tools   # install the pinned golangci-lint and govulncheck
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow and [SECURITY.md](SECURITY.md) for reporting vulnerabilities.

## License

Apache License 2.0, see [LICENSE](LICENSE).
