## ADDED Requirements

### Requirement: MCP servers subsection on Agents panel
The Agents panel SHALL include a new subsection for managing MCP servers. It SHALL display a table of servers (name, enabled, command, connected, tool count, last error), an "Add server" button that opens a form (name, command, args, env, enabled), per-row Edit and Delete buttons, and a persistent banner at the top of the subsection that reads "Restart main-agent to apply MCP config changes" whenever the stored config differs from the live state reported on `/v1/status`.

#### Scenario: MCP table populated
- **WHEN** the Agents panel loads and `/v1/mcp/servers` returns a non-empty list
- **THEN** the subsection SHALL render one row per server with stored fields plus live state (connected, tool count, last error) joined from `/v1/status.mcp_servers` by name

#### Scenario: Add server
- **WHEN** the user fills the Add form and submits
- **THEN** the dashboard SHALL POST to `/v1/mcp/servers` with the entered object and refresh the table on success

#### Scenario: Edit server
- **WHEN** the user clicks Edit on a row
- **THEN** the form SHALL open populated with that server's stored fields; submitting SHALL PUT the full list with the edited entry replaced, and refresh the table

#### Scenario: Delete server
- **WHEN** the user clicks Delete on a row and confirms
- **THEN** the dashboard SHALL DELETE `/v1/mcp/servers/:name` and remove the row from the table on 204

#### Scenario: Restart banner visibility
- **WHEN** the list of server names, commands, args, env, or enabled flags in `/v1/mcp/servers` differs from the corresponding live-state fields in `/v1/status.mcp_servers`
- **THEN** the banner SHALL be visible; otherwise the banner SHALL be hidden

#### Scenario: Banner absent on empty config
- **WHEN** both `/v1/mcp/servers` and `/v1/status.mcp_servers` are empty
- **THEN** the banner SHALL NOT be visible and no drift state SHALL be reported
