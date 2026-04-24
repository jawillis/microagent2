## ADDED Requirements

### Requirement: MCP servers CRUD endpoints
The gateway SHALL expose four endpoints for managing the MCP server list: `GET /v1/mcp/servers`, `PUT /v1/mcp/servers`, `POST /v1/mcp/servers`, `DELETE /v1/mcp/servers/:name`. All endpoints operate on the `config:mcp:servers` Valkey key.

#### Scenario: Read server list
- **WHEN** a GET request is made to `/v1/mcp/servers`
- **THEN** the gateway SHALL return `{"servers": [...]}` with the current stored list (may be empty)

#### Scenario: Replace server list
- **WHEN** a PUT request is made to `/v1/mcp/servers` with body `{"servers": [...]}`
- **THEN** the gateway SHALL validate every entry (name format, uniqueness, required fields), and on success write the full list to Valkey; on validation failure SHALL return 400 with an error describing the first invalid entry

#### Scenario: Append single server
- **WHEN** a POST request is made to `/v1/mcp/servers` with a single server object body
- **THEN** the gateway SHALL append it to the stored list; if a server with that name already exists, SHALL return 409 without mutating the list

#### Scenario: Delete by name
- **WHEN** a DELETE request is made to `/v1/mcp/servers/:name` and that name exists
- **THEN** the gateway SHALL remove that entry from the stored list and return 204

#### Scenario: Delete missing name
- **WHEN** a DELETE request is made to `/v1/mcp/servers/:name` and no entry matches
- **THEN** the gateway SHALL return 404

### Requirement: /v1/status surfaces MCP server health
The gateway's `/v1/status` handler SHALL include an `mcp_servers` field populated from the `health:main-agent:mcp` Valkey key. Each entry SHALL contain `name`, `enabled`, `connected`, `tool_count`, and `last_error` fields.

#### Scenario: Status includes MCP health
- **WHEN** a GET request is made to `/v1/status` and `health:main-agent:mcp` is populated
- **THEN** the response SHALL include a top-level `mcp_servers` field whose value is the parsed JSON array from that key

#### Scenario: Status when MCP health absent
- **WHEN** `health:main-agent:mcp` is missing or empty (main-agent not yet reporting)
- **THEN** the response SHALL include `mcp_servers` as an empty array, not omit the field
