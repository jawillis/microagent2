## ADDED Requirements

### Requirement: Logs panel as gateway built-in
The gateway SHALL register a built-in Logs panel descriptor declaring a single section of kind `logs`, with tail/history/services URLs pointing at the gateway's own log endpoints. The Logs panel SHALL be ordered `80` so it appears after System but with other built-ins.

#### Scenario: Logs tab available
- **WHEN** the dashboard is loaded
- **THEN** a Logs tab SHALL appear in the navigation, contributed by the gateway

### Requirement: Logs section rendering
The dashboard JS SHALL render `logs` sections as a live log viewer with filter controls above a scrollable list.

#### Scenario: Filter controls shown
- **WHEN** a Logs section is rendered
- **THEN** the UI SHALL display:
  - a multi-select of services (populated from `services_url`; all selected by default unless `default_services` constrains the initial selection)
  - a level filter (radio or select; default from `default_level`)
  - a correlation-ID search box (free-text, exact or prefix match)
  - a free-text filter applied to `msg` and other string fields
  - an auto-scroll toggle (default on)

#### Scenario: Live tail subscribed on mount
- **WHEN** the Logs panel becomes the active tab
- **THEN** the dashboard SHALL open an `EventSource` to `tail_url` with the current filter state as query parameters; incoming events SHALL append to the list

#### Scenario: Tail reconnects on disconnect
- **WHEN** the EventSource connection closes unexpectedly
- **THEN** the browser's native reconnect behavior handles it (EventSource auto-reconnects); the UI SHALL indicate connection state

#### Scenario: In-DOM entry cap
- **WHEN** the number of entries in the list exceeds 500
- **THEN** the oldest entries SHALL be removed from the DOM (FIFO); the list remains performant

#### Scenario: Correlation-ID filter
- **WHEN** the operator enters a correlation_id in the filter box
- **THEN** the EventSource URL SHALL include `correlation_id=<value>`, the server SHALL filter on that field, and only matching entries SHALL appear

#### Scenario: History fetch on tab open
- **WHEN** the Logs tab opens
- **THEN** the dashboard MAY first fetch recent history via `history_url` to populate the view with context, then switch to live tail
