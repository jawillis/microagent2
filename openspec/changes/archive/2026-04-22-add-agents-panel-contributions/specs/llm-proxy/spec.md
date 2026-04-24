## ADDED Requirements

### Requirement: llm-proxy registers with panel descriptor
llm-proxy SHALL publish a registration message on `stream:registry:announce` at startup and maintain a periodic heartbeat. The registration SHALL include a `dashboard_panel` descriptor with a single form section for timeout configuration.

#### Scenario: Registration + heartbeat
- **WHEN** llm-proxy starts
- **THEN** it SHALL publish a `RegisterPayload` with `agent_id: "llm-proxy"`, `capabilities: ["llm-proxy"]`, `heartbeat_interval_ms` from env (default 3000), `preemptible: false`, and a valid `dashboard_panel` descriptor; it SHALL continue heartbeating on `channel:heartbeat:llm-proxy`

#### Scenario: Panel descriptor
- **WHEN** llm-proxy constructs its descriptor
- **THEN** the descriptor SHALL have `title: "LLM Proxy"`, `order: 310`, and a single form section with `config_key: "llm_proxy"` and fields:
  - `slot_timeout_ms` (integer, min 100, default 10000) — how long to wait for a hindsight-class slot before returning 503
  - `request_timeout_ms` (integer, min 1000, default 300000) — total upstream request deadline
  - `identity` (string, readonly) — the service identity used for slot ownership

### Requirement: Runtime-tunable llm-proxy config with env fallback
llm-proxy SHALL resolve its configuration via `config.ResolveLLMProxy` which reads `config:llm_proxy` from Valkey first, falls back to env (`LLM_PROXY_SLOT_TIMEOUT_MS`, `LLM_PROXY_REQUEST_TIMEOUT_MS`), then hardcoded defaults. Values SHALL be re-read per request so changes take effect without restart.

#### Scenario: Valkey wins over env
- **WHEN** `config:llm_proxy.slot_timeout_ms` is in Valkey and the env var is also set
- **THEN** the proxy SHALL use the Valkey value on the next request

#### Scenario: Env fallback
- **WHEN** Valkey lacks the key and the env var is set
- **THEN** the proxy SHALL use the env value

#### Scenario: Identity is readonly
- **WHEN** the operator attempts to edit `identity` via the form
- **THEN** the dashboard SHALL prevent submission (readonly field); the proxy SHALL NOT read identity from Valkey (identity remains env-only, `LLM_PROXY_IDENTITY`, default `llm-proxy`)
