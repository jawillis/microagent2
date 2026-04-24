## ADDED Requirements

### Requirement: main-agent registers an MCP panel descriptor
main-agent SHALL include a `dashboard_panel` descriptor in its existing registration payload. The descriptor SHALL have three sections: a status table showing registered MCP servers and their health, an action section for add/remove operations, and a form section for the MCP invocation timeout.

#### Scenario: Panel descriptor sections
- **WHEN** main-agent constructs its descriptor
- **THEN** the descriptor SHALL have `title: "MCP"`, `order: 330`, and sections:
  - status section with `layout: "table"` pointing at `/v1/mcp/servers` showing: name, enabled, command, connected, tools, last_error
  - action section with two actions: "Add server" (POST `/v1/mcp/servers` with params for name, command, args, env, enabled) and "Remove server" (DELETE `/v1/mcp/servers/{name}` with params for name)
  - form section with `config_key: "mcp"` and one field: `invoke_timeout_s` (integer, min 1, default 30)

### Requirement: MCP CRUD via action parameters
The MCP add action SHALL declare `params` covering the fields required to register a new MCP server. The remove action SHALL require only the server name.

#### Scenario: Add action params
- **WHEN** the Add server action is rendered
- **THEN** the dashboard SHALL display inputs for: `name` (string, required), `command` (string, required), `args` (string, optional, space-separated), `env` (textarea, optional, KEY=VALUE per line), `enabled` (boolean, default true)

#### Scenario: Remove action params
- **WHEN** the Remove server action is rendered
- **THEN** the dashboard SHALL display a single input: `name` (string, required). Submission SHALL issue DELETE `/v1/mcp/servers/{name}` with the name substituted into the URL

#### Scenario: Action completion shows status
- **WHEN** an MCP action completes successfully
- **THEN** the panel SHALL refresh its status section data to reflect the new server list
