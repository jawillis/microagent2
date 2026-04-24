## ADDED Requirements

### Requirement: Dashboard renders action sections
The dashboard JS SHALL render `action` sections with a title and one button per action, each with optional parameter inputs rendered adjacent. Clicking a button, after any declared confirmation, issues the action's HTTP request with the declared method, body, and parameters.

#### Scenario: Button click issues request
- **WHEN** the operator fills any required parameters and clicks an action button
- **THEN** the dashboard SHALL issue the declared HTTP request including parameter values in the body (or URL if the parameter appears as `{name}` in the URL template), display success text from `status_key` on 2xx, or error on non-2xx

#### Scenario: Confirmation required
- **WHEN** the action declares `confirm`
- **THEN** the dashboard SHALL show a browser confirm dialog with the declared message and fire the request only on affirmative response

#### Scenario: Required parameters disable button
- **WHEN** an action has `params` with any `required: true` that are empty
- **THEN** the button SHALL be disabled until all required parameters are populated

### Requirement: Broker, LLM Proxy, Retro, MCP panels appear when their services are alive
The panels contributed by llm-broker, llm-proxy, retro-agent, and main-agent SHALL appear in the dashboard when those services are registered and heartbeating. They SHALL disappear when the service drops from the registry.

#### Scenario: All tabs visible under full deploy
- **WHEN** all four services are alive
- **THEN** the dashboard SHALL render tabs titled "Broker", "LLM Proxy", "Retro", and "MCP" in the order defined by their descriptors' `order` values

#### Scenario: Partial availability
- **WHEN** a subset of services are alive (e.g. llm-proxy is restarting)
- **THEN** only the panels for alive services SHALL be visible; the missing tab returns on the next dashboard fetch once the service comes back

### Requirement: Broker slot-table status section
The Broker panel's status section SHALL render the slot table fetched from `/v1/broker/slots` as a table with columns: slot, class, state, agent, priority, age_s.

#### Scenario: Table renders with live data
- **WHEN** the Broker tab becomes active
- **THEN** the dashboard SHALL GET `/v1/broker/slots`, parse the `slots` array, and render a row per entry

#### Scenario: 503 shows error state
- **WHEN** the gateway returns 503 from `/v1/broker/slots`
- **THEN** the dashboard SHALL render an inline error message in the status area rather than an empty table
