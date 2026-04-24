## ADDED Requirements

### Requirement: MCP server health visibility
The system-health capability SHALL include MCP server connection state. Main-agent SHALL write a JSON snapshot to `health:main-agent:mcp`; the gateway's `/v1/status` endpoint SHALL surface it under an `mcp_servers` field. Each entry SHALL contain `name`, `enabled`, `connected`, `tool_count`, and `last_error`.

#### Scenario: Main-agent publishes health
- **WHEN** main-agent's MCP manager completes a lifecycle event (spawn, initialize, tools registration, disconnect) or a 30-second heartbeat elapses
- **THEN** main-agent SHALL SET `health:main-agent:mcp` to the current JSON-encoded snapshot

#### Scenario: Gateway surfaces health
- **WHEN** `/v1/status` is requested
- **THEN** the response SHALL read `health:main-agent:mcp` and include its parsed contents as the `mcp_servers` field, or an empty array if the key is absent
