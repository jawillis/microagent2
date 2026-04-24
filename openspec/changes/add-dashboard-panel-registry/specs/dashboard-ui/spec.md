## MODIFIED Requirements

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

## REMOVED Requirements

### Requirement: Chat panel
**Reason**: Replaced by descriptor-driven rendering. The gateway now registers a built-in Chat panel descriptor; the dashboard renders the form from that descriptor using the schema-driven mechanism. User-visible behavior is unchanged.

**Migration**: The form fields (system prompt, model, request timeout) are declared in the gateway's built-in descriptor at startup. Save continues to PUT `/v1/config` with section `chat`. No action required by operators.

### Requirement: Memory panel
**Reason**: The memory panel's current muninn-era fields (recall_threshold, max_hops, vault, store_confidence) no longer have meaning under Hindsight. Rather than update the hardcoded panel, this panel is removed from the dashboard shell and will be re-introduced as a descriptor-driven contribution from memory-service in the follow-up `add-memory-panel-contribution` change. Until that change lands, the Memory tab does not appear in the dashboard.

**Migration**: Operators lose the memory configuration form temporarily. During the gap between this change and `add-memory-panel-contribution`, Hindsight-side configuration is editable via Hindsight's Control Plane at its exposed port, and microagent2-specific memory config is editable via `PUT /v1/config` section `memory` directly (same as before). The dashboard re-gains a Memory panel when memory-service contributes its descriptor.

### Requirement: Agents panel
**Reason**: Similar to Memory — the Agents panel's broker settings and retro policy forms are tightly coupled to specific services. The panel is removed from the dashboard shell and will be re-introduced as contributions from llm-broker (slot management + agent registry), retro-agent (retro policy), and llm-proxy (timeouts) in a follow-up `add-agents-panel-contributions` change.

**Migration**: During the gap, operators can continue editing broker settings via `PUT /v1/config` section `broker`, retro settings via section `retro`, and the MCP endpoints remain unchanged. The Agents tab returns once the follow-up change lands.

## ADDED Requirements

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
