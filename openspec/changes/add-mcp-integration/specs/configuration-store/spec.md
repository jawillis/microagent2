## ADDED Requirements

### Requirement: MCP servers config section
The configuration store SHALL support a new `config:mcp:servers` key that holds a JSON-encoded array of MCP server definitions. Each definition SHALL include `name` (string, required, matching `[a-zA-Z0-9_-]+`), `enabled` (bool), `command` (string, required), `args` (string array), and `env` (string-to-string map). The store SHALL validate each entry on read and skip invalid ones with a structured WARN log.

#### Scenario: Read MCP servers
- **WHEN** a caller invokes `ResolveMCPServers(ctx, store)`
- **THEN** the store SHALL GET `config:mcp:servers`, parse it as a JSON array, validate each entry, and return the valid entries as `[]MCPServerConfig`

#### Scenario: Empty or missing key
- **WHEN** `config:mcp:servers` is missing or holds an empty array
- **THEN** `ResolveMCPServers` SHALL return an empty slice without error

#### Scenario: Invalid entry skipped
- **WHEN** an array entry fails validation (missing required field or malformed name)
- **THEN** the store SHALL log at WARN with `msg: "mcp_server_config_invalid"` and fields `{entry_index, reason}`, and SHALL continue returning the remaining valid entries

#### Scenario: Write MCP servers
- **WHEN** a caller writes a full replacement list via the store's setter
- **THEN** the store SHALL validate each entry server-side, reject on any invalid entry with a typed error, and on success SET `config:mcp:servers` to the JSON-encoded array
