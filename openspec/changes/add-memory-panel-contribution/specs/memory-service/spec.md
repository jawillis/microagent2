## ADDED Requirements

### Requirement: memory-service registers with panel descriptor
memory-service SHALL publish a registration message on `stream:registry:announce` at startup, including a `dashboard_panel` descriptor declaring a two-section panel: a microagent2-specific config form and an iframe embedding Hindsight's Control Plane. memory-service SHALL also maintain a periodic heartbeat per the agent-registration contract.

#### Scenario: Registration publishes descriptor
- **WHEN** memory-service starts and connects to Valkey
- **THEN** it SHALL publish a `RegisterPayload` to `stream:registry:announce` with `agent_id: "memory-service"`, `capabilities: ["memory"]`, a non-zero `heartbeat_interval_ms`, and a valid `dashboard_panel` descriptor (see the descriptor schema in the dashboard-panel-registry capability)

#### Scenario: Heartbeat sustains registration
- **WHEN** memory-service is running normally
- **THEN** it SHALL publish a heartbeat to `channel:heartbeat:memory-service` at the declared interval

#### Scenario: Graceful shutdown
- **WHEN** memory-service receives SIGINT/SIGTERM
- **THEN** it SHALL publish a deregistration message before exiting, causing its panel to disappear from the dashboard aggregation

### Requirement: Panel descriptor shape for memory-service
The `dashboard_panel` descriptor SHALL declare: `title: "Memory"`, `order: 200`, and two sections in order: a `form` section for microagent2-specific config, followed by an `iframe` section for Hindsight's Control Plane.

#### Scenario: Form section contents
- **WHEN** memory-service constructs its panel descriptor
- **THEN** the form section SHALL have `config_key: "memory"`, `title: "Memory Configuration"`, and fields:
  - `recall_limit` (integer, min 1, default 5)
  - `prewarm_limit` (integer, min 0, default 3)
  - `recall_default_types` (enum, values `["observation","world_experience","all"]`, default `"observation"`)
  - `default_provenance` (enum, values `["explicit","implicit","inferred","researched"]`, default `"explicit"`)
  - `tag_taxonomy` (string, default `"identity,preferences,technical,home,ephemera"`, description noting comma-separated)
  - `memory_bank_id` (string, readonly, shows the currently-configured bank)

#### Scenario: Iframe section contents
- **WHEN** memory-service constructs its panel descriptor
- **THEN** the iframe section SHALL have `title: "Hindsight Control Plane"`, `url: ${MEMORY_SERVICE_CP_URL}` (env-resolved at descriptor construction time), and `height: "800px"`

### Requirement: Environment-driven CP URL
memory-service SHALL read `MEMORY_SERVICE_CP_URL` at startup. This URL is the external-to-browser Hindsight Control Plane URL that the iframe section uses.

#### Scenario: CP URL from environment
- **WHEN** memory-service starts
- **THEN** it SHALL read `MEMORY_SERVICE_CP_URL` (default `http://localhost:9999`) and bake that value into the iframe section's `url`

#### Scenario: Changing CP URL requires restart
- **WHEN** the operator changes `MEMORY_SERVICE_CP_URL` env and restarts memory-service
- **THEN** the new value SHALL appear in the panel descriptor on next registration

### Requirement: Recall types default from config
memory-service SHALL read `recall_default_types` from `config:memory` at request time and use it to determine the default `types` list sent to Hindsight when a `/recall` request omits `types`.

#### Scenario: observation default
- **WHEN** `config:memory.recall_default_types` is `"observation"` and a `/recall` request omits `types`
- **THEN** memory-service SHALL pass `types: ["observation"]` to Hindsight's recall endpoint

#### Scenario: world_experience default
- **WHEN** `config:memory.recall_default_types` is `"world_experience"` and a `/recall` request omits `types`
- **THEN** memory-service SHALL pass `types: ["world","experience"]` to Hindsight's recall endpoint

#### Scenario: all default
- **WHEN** `config:memory.recall_default_types` is `"all"` and a `/recall` request omits `types`
- **THEN** memory-service SHALL pass `types: ["observation","world","experience"]` to Hindsight's recall endpoint

#### Scenario: Config missing falls back to observation
- **WHEN** `config:memory.recall_default_types` is unset in Valkey
- **THEN** memory-service SHALL pass `types: ["observation"]` (same as the current hardcoded default)

#### Scenario: Caller-provided types override
- **WHEN** a `/recall` request includes a non-empty `types` field
- **THEN** the config default SHALL NOT apply and the request's types SHALL pass through

### Requirement: Default provenance from config
memory-service SHALL read `default_provenance` from `config:memory` at request time and use it as the `metadata.provenance` default for `/retain` requests that omit it.

#### Scenario: Explicit default
- **WHEN** `config:memory.default_provenance` is `"explicit"` and a `/retain` request's metadata omits `provenance`
- **THEN** memory-service SHALL set `metadata.provenance = "explicit"` before forwarding to Hindsight

#### Scenario: Caller-provided provenance preserved
- **WHEN** a `/retain` request includes `metadata.provenance`
- **THEN** the config default SHALL NOT apply and the request's value SHALL pass through (subject to existing enum validation)

#### Scenario: Invalid config value rejected at read
- **WHEN** `config:memory.default_provenance` is set to a value not in the valid provenance enum
- **THEN** memory-service SHALL fall back to `"explicit"`, log at WARN with `msg: "memory_default_provenance_invalid"` and the offending value

### Requirement: Deprecated config keys tolerated but unused
memory-service SHALL ignore `vault`, `max_hops`, `store_confidence`, and `recall_threshold` if present in `config:memory`. Their presence SHALL NOT cause errors, but they SHALL have no behavioral effect.

#### Scenario: Deprecated keys present but inert
- **WHEN** `config:memory` contains any of `vault`, `max_hops`, `store_confidence`, `recall_threshold`
- **THEN** memory-service SHALL read and discard these keys; no Hindsight call or internal decision SHALL reference them
