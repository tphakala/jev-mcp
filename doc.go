// Package jevmcp holds what the jev-mcp binary shares across the tree: the
// Version constant. The MCP server, the Jev HTTP client, and configuration
// live under internal/; this root package stays limited to Version so the
// module can be imported for it alone.
package jevmcp
