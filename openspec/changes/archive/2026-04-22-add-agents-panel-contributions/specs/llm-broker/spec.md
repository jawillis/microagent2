## ADDED Requirements

### Requirement: llm-broker registers with panel descriptor
llm-broker SHALL publish a registration message on `stream:registry:announce` at startup and maintain a periodic heartbeat. The registration SHALL include a `dashboard_panel` descriptor with two sections: a form section for config, and a status section showing the live slot table.

#### Scenario: Registration + heartbeat
- **WHEN** llm-broker starts
- **THEN** it SHALL publish a `RegisterPayload` with `agent_id: "llm-broker"`, `capabilities: ["llm-broker"]`, `heartbeat_interval_ms` from env (default 3000), `preemptible: false`, and a valid `dashboard_panel` descriptor; it SHALL continue heartbeating on `channel:heartbeat:llm-broker`

#### Scenario: Panel descriptor sections
- **WHEN** the broker constructs its descriptor
- **THEN** the descriptor SHALL have `title: "Broker"`, `order: 300`, and sections:
  - form section with `config_key: "broker"` and fields for `agent_slot_count` (integer), `hindsight_slot_count` (integer), `preempt_timeout_ms` (integer), `provisional_timeout_ms` (integer), `slot_snapshot_interval_s` (integer). Fields that require restart SHALL have a description noting so.
  - status section with `layout: "table"` pointing at `/v1/broker/slots`

### Requirement: Runtime-tunable broker config with env fallback
llm-broker SHALL resolve its configuration via `config.ResolveBroker` which reads `config:broker` from Valkey first, then falls back to environment variables (`AGENT_SLOT_COUNT`, `HINDSIGHT_SLOT_COUNT`, `PREEMPT_TIMEOUT_MS`, `PROVISIONAL_TIMEOUT_MS`, `SLOT_SNAPSHOT_INTERVAL_S`), then hardcoded defaults. Timeout values SHALL be re-read at request time so changes from the dashboard take effect without restart. Slot-count values SHALL be read only at startup; changing them at runtime requires a broker restart.

#### Scenario: Valkey value wins over env
- **WHEN** `config:broker.preempt_timeout_ms` is set in Valkey and `PREEMPT_TIMEOUT_MS` is set in env
- **THEN** the broker SHALL use the Valkey value

#### Scenario: Env fallback on empty Valkey
- **WHEN** `config:broker.preempt_timeout_ms` is absent and `PREEMPT_TIMEOUT_MS` is in env
- **THEN** the broker SHALL use the env value

#### Scenario: Default on empty Valkey and env
- **WHEN** neither Valkey nor env has the value
- **THEN** the broker SHALL use the hardcoded default

#### Scenario: Preempt timeout hot-reloads
- **WHEN** the dashboard saves a new `preempt_timeout_ms` to `config:broker`
- **THEN** subsequent preemption events SHALL use the new value without a broker restart

#### Scenario: Slot-count change ignored at runtime
- **WHEN** the dashboard saves a new `agent_slot_count` or `hindsight_slot_count`
- **THEN** the value is persisted but the live SlotTable SHALL NOT rebuild; the operator is expected to restart the broker to apply slot-count changes

### Requirement: Gateway slot snapshot endpoint
The gateway SHALL expose `GET /v1/broker/slots` that returns a JSON object containing the broker's current slot table snapshot. The gateway SHALL fetch the snapshot from the broker via a messaging request/reply pair on a dedicated stream.

#### Scenario: Snapshot fetched and returned
- **WHEN** a client GETs `/v1/broker/slots`
- **THEN** the gateway SHALL publish a `SlotSnapshotRequest` message on `stream:broker:slot-snapshot-requests`, wait up to 5 seconds for a `SlotSnapshotResponse` on the reply stream, and return a JSON object `{"slots": [<entry>...]}` with one entry per slot containing `{slot, class, state, agent, priority, age_s}`

#### Scenario: Timeout on broker unreachable
- **WHEN** the broker does not reply within 5 seconds
- **THEN** the gateway SHALL return HTTP 503 with `{"error": "broker_unreachable"}`

### Requirement: Broker exposes snapshot via messaging
llm-broker SHALL consume `stream:broker:slot-snapshot-requests` and respond with the current slot table on the reply stream.

#### Scenario: Snapshot publish
- **WHEN** the broker receives a `SlotSnapshotRequest`
- **THEN** it SHALL publish a `SlotSnapshotResponse` to the request's reply stream containing the current snapshot within 1 second under normal load
