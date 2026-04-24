## ADDED Requirements

### Requirement: Memory panel contributed by memory-service
The dashboard's Memory panel SHALL appear whenever memory-service is registered and alive. Its content SHALL be composed from memory-service's panel descriptor — a form section for microagent2-specific config followed by an iframe embedding Hindsight's Control Plane.

#### Scenario: Memory tab visible when memory-service is alive
- **WHEN** memory-service has registered and is heartbeating normally
- **THEN** the dashboard SHALL display a "Memory" tab in its navigation, ordered per the descriptor's `order` (200)

#### Scenario: Memory tab absent when memory-service is down
- **WHEN** memory-service is not registered or has failed its heartbeat
- **THEN** the dashboard SHALL NOT display a Memory tab

#### Scenario: Form edits persisted to config:memory
- **WHEN** the operator edits fields in the Memory form and clicks Save
- **THEN** the dashboard SHALL PUT `/v1/config` with section `memory` and the form values, and memory-service's subsequent request handling SHALL use the new values

#### Scenario: Iframe shows Hindsight CP
- **WHEN** the Memory panel is selected
- **THEN** the iframe section SHALL load the URL declared in memory-service's descriptor (`MEMORY_SERVICE_CP_URL`) and render Hindsight's Control Plane inline
