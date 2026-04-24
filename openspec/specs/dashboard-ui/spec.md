### Requirement: Dashboard served from gateway
The gateway SHALL serve a static HTML/CSS/JS dashboard at `GET /`. The dashboard files SHALL be embedded in the gateway binary using Go's `embed.FS`.

#### Scenario: Dashboard loads
- **WHEN** a GET request is made to `/`
- **THEN** the gateway SHALL serve the dashboard HTML page with associated CSS and JS assets

### Requirement: Dashboard has five panels
The dashboard SHALL compose its panels from `GET /v1/dashboard/panels`. Initial built-in panels (Chat, Sessions, System) SHALL be registered by the gateway itself so they continue to appear alongside service-contributed panels. The Memory and Agents panels SHALL be contributed by memory-service and llm-broker respectively (not built-in to the gateway), as part of the follow-up capability proposals.

#### Scenario: Panel composition on load
- **WHEN** the dashboard HTML is loaded
- **THEN** the dashboard JS SHALL fetch `GET /v1/dashboard/panels` and render a tab + panel container for each returned descriptor, in the order returned by the endpoint

#### Scenario: Panel navigation
- **WHEN** the user clicks a panel tab
- **THEN** the dashboard SHALL display the selected panel's content without a full page reload

#### Scenario: Zero panels
- **WHEN** the endpoint returns zero panels (e.g. during a brief window where no service has registered yet)
- **THEN** the dashboard SHALL render an empty state message rather than a blank page

### Requirement: Sessions panel
The Sessions panel SHALL display a table of active sessions with session ID, turn count, and last active time. Each session row SHALL have View and Delete action buttons. View SHALL display the session's chat history. Delete SHALL call `DELETE /v1/sessions/:id` and remove the row.

The Sessions panel SHALL also display retro action buttons per session: Run Memory Extraction, Run Skill Creation, and Run Curation. These SHALL POST to `/v1/retro/:session/trigger`.

#### Scenario: View session history
- **WHEN** the user clicks View on a session row
- **THEN** the dashboard SHALL fetch and display the chat history for that session

#### Scenario: Delete session
- **WHEN** the user clicks Delete on a session row
- **THEN** the dashboard SHALL send a DELETE request and remove the row from the table

#### Scenario: Trigger retro job
- **WHEN** the user clicks a retro action button for a session
- **THEN** the dashboard SHALL POST to the retro trigger endpoint and display the result

### Requirement: System panel
The System panel SHALL display health check results for Valkey, llama.cpp, and MuninnDB (connected/disconnected status). It SHALL also display read-only infrastructure settings (gateway port, Valkey address, LLM server address, MuninnDB address) as reported by the status endpoint.

#### Scenario: All services healthy
- **WHEN** all external services are reachable
- **THEN** the System panel SHALL show green connected indicators for each service

#### Scenario: Service unreachable
- **WHEN** a service (e.g., MuninnDB) is unreachable
- **THEN** the System panel SHALL show a red disconnected indicator for that service

### Requirement: Chat transcript renders tool-call events as collapsed status blocks
The dashboard's Chat panel transcript SHALL render each tool-call event received via the streaming `response.tool_call` SSE event kind as a collapsed status block, visually distinct from user and assistant text turns, showing the tool name and a disclosure affordance to expand and inspect the arguments. Tool-result messages (assistant turns referencing `tool_call_id` results) SHALL render in the same collapsed style.

#### Scenario: Tool call rendered as collapsed block
- **WHEN** the transcript receives a `response.tool_call` SSE event for tool `list_skills` with arguments `{}`
- **THEN** the transcript SHALL append a collapsed status block with a label identifying the tool name (for example `🔧 list_skills`), an expand/collapse affordance, and SHALL NOT append any text content to the surrounding assistant turn for that event

#### Scenario: Expanded block shows full arguments
- **WHEN** the user clicks or activates the disclosure affordance on a tool-call status block
- **THEN** the block SHALL expand to show the full `function.arguments` JSON, formatted for readability

#### Scenario: Tool-call blocks do not pollute text turns
- **WHEN** an assistant turn includes both streamed text tokens and a tool-call event
- **THEN** the rendered assistant turn SHALL show the text content in the normal transcript style, and the tool-call block SHALL render as a separate collapsed element adjacent to the assistant turn, with no tool-call JSON appearing inside the text content

### Requirement: Tool-call blocks are keyboard-accessible
The collapsed tool-call status blocks SHALL be keyboard-operable with native disclosure semantics, so that expand/collapse works without pointer input.

#### Scenario: Keyboard activation
- **WHEN** the user focuses a collapsed tool-call block and presses Enter or Space
- **THEN** the block SHALL toggle between collapsed and expanded states

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

### Requirement: Dashboard renders descriptor-driven panels
The dashboard JS SHALL render each panel from its descriptor by interpreting the `sections` array. Section rendering follows the rules in the `dashboard-panel-registry` capability (form / iframe / status).

#### Scenario: Form section rendered
- **WHEN** a panel contains a section with `kind: "form"` and a `fields` object
- **THEN** the dashboard SHALL render a form with one input per field according to the field's `type` and constraints, a save button, and MAY include a status indicator for save success/failure

#### Scenario: Form save round-trip
- **WHEN** the user modifies form values and clicks save
- **THEN** the dashboard SHALL PUT `/v1/config` with `{"section": <config_key>, "values": <form_data>}`, display success on HTTP 200, and display the error message on non-2xx

#### Scenario: Iframe section rendered
- **WHEN** a panel contains a section with `kind: "iframe"` and a `url`
- **THEN** the dashboard SHALL render an `<iframe src="<url>" sandbox="allow-scripts allow-same-origin allow-forms">` at the declared `height` (or 600px default)

#### Scenario: Status section rendered
- **WHEN** a panel contains a section with `kind: "status"`, a `url`, and a `layout`
- **THEN** the dashboard SHALL GET the URL and render the response as key-value pairs (if `layout: "key_value"`) or as a table (if `layout: "table"`)

### Requirement: Dashboard re-fetches panels on refresh
A full page refresh SHALL cause the dashboard to re-fetch `GET /v1/dashboard/panels` and recompose the UI. Runtime panel hot-reload is not in scope.

#### Scenario: Operator refreshes after new service deploys
- **WHEN** a new service with a panel descriptor starts up, and the operator refreshes the dashboard
- **THEN** the new panel SHALL appear in the tab list
