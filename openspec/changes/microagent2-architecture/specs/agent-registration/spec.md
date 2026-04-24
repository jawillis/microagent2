## ADDED Requirements

### Requirement: Self-registration on startup
Each agent container SHALL announce itself on startup by publishing a registration message to `stream:registry:announce` containing its identity, priority, capabilities, trigger configuration, and preemptibility.

#### Scenario: Agent registration
- **WHEN** an agent container starts and connects to Valkey
- **THEN** it publishes a registration message containing at minimum: `agent_id`, `priority` (integer, 0 = highest), `preemptible` (boolean), `capabilities` (list of strings), `trigger` (object describing activation conditions), and `heartbeat_interval_ms`

#### Scenario: Duplicate registration
- **WHEN** an agent registers with an `agent_id` that already has an active registration (heartbeat still alive)
- **THEN** the system treats the new registration as a restart — the old registration is superseded and any held slots are released

### Requirement: Heartbeat contract
Registered agents SHALL publish periodic heartbeat messages on a dedicated channel to signal liveness.

#### Scenario: Regular heartbeat
- **WHEN** an agent is registered and running
- **THEN** it publishes a heartbeat to `channel:heartbeat:{agent_id}` at the interval declared in its registration message

#### Scenario: Graceful shutdown
- **WHEN** an agent is shutting down intentionally
- **THEN** it publishes a deregistration message to `stream:registry:announce` with `type: "deregister"` and releases any held slots before exiting

### Requirement: Capability declaration
Agents SHALL declare their capabilities during registration so other services can discover what operations an agent can perform.

#### Scenario: Capability-based discovery
- **WHEN** a service needs to find agents with a specific capability (e.g., `memory:write`)
- **THEN** it can read the registration stream and filter by the `capabilities` field to find matching agents

### Requirement: Trigger configuration declaration
Agents SHALL declare their trigger conditions during registration, specifying what events or conditions cause them to activate and request LLM resources.

#### Scenario: Inactivity-triggered agent
- **WHEN** a retrospective agent registers with trigger `{ type: "inactivity", timeout_seconds: 120 }`
- **THEN** the agent subscribes to session events and self-activates when the configured inactivity period elapses

#### Scenario: Event-triggered agent
- **WHEN** an agent registers with trigger `{ type: "event", event: "session_ended" }`
- **THEN** the agent subscribes to `channel:events` and self-activates when a matching event is published
