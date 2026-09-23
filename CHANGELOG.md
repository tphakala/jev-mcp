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
- `TYPESAFE_API_KEY` and `OPENROUTER_API_KEY` are trimmed the same way. A key that is only spaces, tabs, and line breaks, or that holds an ASCII control character other than tab, stops the server at startup with an error naming the variable whenever that provider would be used, including as the fallback. Before, such a key failed each call (a 401 or a retried transport error) and fell back to the other provider when there was one.
- HTTP mode refuses a bearer token that holds an ASCII control character other than tab, and logs a warning when it runs without a token.
- Provider error messages keep TypeSafe's `error_type` when it is short (for example `api_usage_error: Unknown model`). Provider-supplied text in errors, and the text of transport errors, and in `doctor`'s output (messages, request ids, and the probed model and id) has control characters, Unicode bidirectional controls and line separators, and invalid UTF-8 replaced, and is capped at 512 bytes.
- `doctor` shows a credential as `blank` or `invalid` when it is set but unusable, reports a bad API key even when that provider is not selected, runs the `-probe` calls concurrently, and reports a probe cut short by Ctrl-C as interrupted rather than failed.

[Unreleased]: https://github.com/tphakala/jev-mcp/compare/v0.1.0...HEAD
