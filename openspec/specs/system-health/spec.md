## ADDED Requirements

### Requirement: Health status endpoint
The system SHALL provide an API endpoint `GET /v1/status` that returns the health status of all external dependencies and system metadata.

#### Scenario: All services healthy
- **WHEN** a GET request is made to `/v1/status` and all dependencies are reachable
- **THEN** the response SHALL be a JSON object with `valkey`, `llm_server`, and `muninndb` each reporting `"status": "connected"`, plus system metadata including gateway port, service addresses, and registered agent info

#### Scenario: Partial failure
- **WHEN** a GET request is made to `/v1/status` and one or more dependencies are unreachable
- **THEN** the unreachable services SHALL report `"status": "disconnected"` with an `"error"` field, and reachable services SHALL report `"status": "connected"`

### Requirement: Health checks use lightweight probes
The Valkey health check SHALL use a PING command. The llama.cpp health check SHALL use a GET to `/health` or a HEAD request. The MuninnDB health check SHALL use a GET to `/api/health` or equivalent lightweight endpoint.

#### Scenario: Valkey health check
- **WHEN** the health endpoint checks Valkey
- **THEN** it SHALL issue a PING command and report connected if PONG is returned within 3 seconds

#### Scenario: LLM server health check
- **WHEN** the health endpoint checks the llama.cpp server
- **THEN** it SHALL issue an HTTP request and report connected if a successful response is returned within 5 seconds

#### Scenario: MuninnDB health check
- **WHEN** the health endpoint checks MuninnDB
- **THEN** it SHALL issue an HTTP request and report connected if a successful response is returned within 5 seconds

### Requirement: Registered agents in status
The status endpoint SHALL include a list of registered agents with their agent ID, priority, preemptible flag, heartbeat interval, and alive status. This data SHALL be read from the agent registry.

#### Scenario: Agents registered
- **WHEN** agents are registered in the system
- **THEN** the status response SHALL include an `agents` array with one entry per registered agent containing id, priority, preemptible, heartbeat_interval_ms, and alive fields

## ADDED Requirements

### Requirement: MCP server health visibility
The system-health capability SHALL include MCP server connection state. Main-agent SHALL write a JSON snapshot to `health:main-agent:mcp`; the gateway's `/v1/status` endpoint SHALL surface it under an `mcp_servers` field. Each entry SHALL contain `name`, `enabled`, `connected`, `tool_count`, and `last_error`.

#### Scenario: Main-agent publishes health
- **WHEN** main-agent's MCP manager completes a lifecycle event (spawn, initialize, tools registration, disconnect) or a 30-second heartbeat elapses
- **THEN** main-agent SHALL SET `health:main-agent:mcp` to the current JSON-encoded snapshot

#### Scenario: Gateway surfaces health
- **WHEN** `/v1/status` is requested
- **THEN** the response SHALL read `health:main-agent:mcp` and include its parsed contents as the `mcp_servers` field, or an empty array if the key is absent
