## ADDED Requirements

### Requirement: Optional dashboard panel descriptor on registration
The `messaging.RegisterPayload` SHALL include an optional `dashboard_panel` field carrying a panel descriptor (see `dashboard-panel-registry` capability). Services that do not expose a dashboard panel SHALL omit the field; its absence SHALL NOT affect any existing registration behavior.

#### Scenario: Service without a panel
- **WHEN** a service registers without a `dashboard_panel` field
- **THEN** registration SHALL succeed exactly as before this change, and no panel SHALL appear in the dashboard aggregation for this service

#### Scenario: Service with a panel
- **WHEN** a service registers with a valid `dashboard_panel` descriptor
- **THEN** the registry consumer SHALL store the descriptor on the agent's registry entry, and `GET /v1/dashboard/panels` SHALL include it while the service is alive

#### Scenario: Panel descriptor survives heartbeat cycles
- **WHEN** a service with a panel heartbeats normally
- **THEN** the descriptor SHALL remain available through the registry until the service deregisters or heartbeats time out

#### Scenario: Panel descriptor removed on deregistration
- **WHEN** a service publishes a deregistration message
- **THEN** its panel descriptor SHALL be removed from the registry and subsequent `GET /v1/dashboard/panels` responses SHALL NOT include it
