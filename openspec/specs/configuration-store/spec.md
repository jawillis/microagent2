## ADDED Requirements

### Requirement: Config store reads and writes
The system SHALL provide a config store that reads and writes configuration values to Valkey using JSON-encoded hash keys in the `config:*` keyspace. The store SHALL support four config sections: `chat`, `memory`, `broker`, and `retro`.

#### Scenario: Write a config section
- **WHEN** a config section is written with a map of key-value pairs
- **THEN** the store SHALL serialize the map as JSON and SET it to the corresponding `config:<section>` key in Valkey

#### Scenario: Read a config section
- **WHEN** a config section is read
- **THEN** the store SHALL GET the `config:<section>` key from Valkey and deserialize the JSON into a typed struct

#### Scenario: Config key does not exist in Valkey
- **WHEN** a config section is read and the `config:<section>` key does not exist
- **THEN** the store SHALL return an empty/zero struct without error, allowing callers to fall back to defaults

### Requirement: Config resolution with env var fallback
Each service SHALL resolve configuration by reading the Valkey config store first, then falling back to environment variables, then to hardcoded defaults. Valkey values take precedence over env vars.

#### Scenario: Valkey value present
- **WHEN** a service reads a config field and Valkey has a value for that field
- **THEN** the service SHALL use the Valkey value regardless of what the env var is set to

#### Scenario: Valkey value absent, env var present
- **WHEN** a service reads a config field and Valkey does not have a value but the env var is set
- **THEN** the service SHALL use the env var value

#### Scenario: Both absent
- **WHEN** a service reads a config field and neither Valkey nor env var has a value
- **THEN** the service SHALL use the hardcoded default

### Requirement: Chat config section
The `config:chat` section SHALL contain: `system_prompt` (string), `model` (string, default `"default"`), and `request_timeout_s` (integer, default `120`).

#### Scenario: Chat config fields
- **WHEN** the chat config is loaded
- **THEN** it SHALL contain system_prompt, model, and request_timeout_s with their respective types and defaults

### Requirement: Memory config section
The `config:memory` section SHALL contain: `recall_limit` (integer, default `5`), `recall_threshold` (float, default `0.5`), `max_hops` (integer, default `2`), `prewarm_limit` (integer, default `3`), `vault` (string, default `"default"`), and `store_confidence` (float, default `0.9`).

#### Scenario: Memory config fields
- **WHEN** the memory config is loaded
- **THEN** it SHALL contain recall_limit, recall_threshold, max_hops, prewarm_limit, vault, and store_confidence with their respective types and defaults

### Requirement: Broker config section
The `config:broker` section SHALL contain: `slot_count` (integer, default `4`) and `preempt_timeout_ms` (integer, default `5000`).

#### Scenario: Broker config fields
- **WHEN** the broker config is loaded
- **THEN** it SHALL contain slot_count and preempt_timeout_ms with their respective types and defaults

### Requirement: Retro config section
The `config:retro` section SHALL contain: `inactivity_timeout_s` (integer, default `300`), `skill_dup_threshold` (float, default `0.85`), `min_history_turns` (integer, default `4`), and `curation_categories` (string array, default `["preference","fact","context","skill"]`).

#### Scenario: Retro config fields
- **WHEN** the retro config is loaded
- **THEN** it SHALL contain inactivity_timeout_s, skill_dup_threshold, min_history_turns, and curation_categories with their respective types and defaults

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
