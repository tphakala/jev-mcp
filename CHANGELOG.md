# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Initial project scaffolding.
- `jev_evaluate` MCP tool: decide choice, score and noul questions over a shared state with Jev, through TypeSafe or OpenRouter, with retry and provider fallback.
- Serving over stdio (the default) or loopback Streamable HTTP (`-http`), with optional bearer auth (`-http-token` or `JEV_MCP_HTTP_TOKEN`) and `-log-level` / `-log-json`.
- `jev-mcp doctor [-probe]` preflight check of the configuration and providers.
- README: configuration reference, the `jev_evaluate` input and output, and MCP client setup for Claude Code, a generic `mcpServers` entry, and the container image.
- Provider error messages are read from TypeSafe's `{"detail":{"message"}}` error body, as well as `error` and `message`, and are length-capped.
- The HTTP bearer token (`JEV_MCP_HTTP_TOKEN` or `-http-token`) is trimmed of surrounding spaces, tabs, and line breaks, so a token copied with a trailing newline still authenticates; HTTP mode refuses to start with a token that is only those characters.

[Unreleased]: https://github.com/tphakala/jev-mcp/compare/v0.1.0...HEAD
