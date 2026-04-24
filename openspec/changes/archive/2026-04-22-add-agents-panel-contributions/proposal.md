## Why

The `add-dashboard-panel-registry` change removed the hardcoded Agents panel from the dashboard shell. The Agents panel covered four distinct concerns — broker slot config, registered agents table, MCP CRUD, retro policy — each owned by a different service. Under the new descriptor-driven model, each service contributes what it owns. Putting all four under one "Agents" tab is convenient for operators but hides which service produces what.

Three services get new panel descriptors:

- **llm-broker** — slot budget by class (agent / hindsight), preempt timeout, provisional timeout, snapshot interval, registered-agents readonly view, live slot table readonly view
- **llm-proxy** — slot timeout, request timeout, identity (readonly)
- **retro-agent** — inactivity timeout, skill duplicate threshold, minimum history turns, curation recall limit, mental-model refresh cadence (when that lands), trigger retro manually (action)

These are distinct enough to warrant separate panels rather than cramming them into one. Naming: `Broker`, `LLM Proxy`, `Retro`. The old `Agents` label was muddy anyway — it covered "services and their config" which is what the dashboard now is as a whole.

MCP CRUD is special — it's agent-local state (main-agent's list of MCP servers). It gets its own panel contributed by main-agent.

## What Changes

- **llm-broker** registers a panel descriptor with two sections:
  - `form` for broker slot budgets + timeouts, `config_key: "broker"`
  - `status` showing the live slot table (via a new gateway endpoint `GET /v1/broker/slots`) and the registered-agents list (`GET /v1/status`'s agents array surfaced as a separate URL `GET /v1/agents`)
- **llm-proxy** registers a panel descriptor with one form section for its three env-driven knobs; the form saves to `config:llm_proxy` (new Valkey config section), and llm-proxy is refactored to read from the config store with env fallback
- **retro-agent** registers a panel descriptor with a form section for retro policy (`config_key: "retro"`) + an `action` section kind (new) for manually triggering memory-extraction / skill-creation / curation jobs
- **main-agent** registers a panel descriptor for MCP server management: a `table` status section listing current MCP servers and their health, plus an `action` section for add/remove
- New `action` section kind in `dashboard-panel-registry` schema for non-config operations (trigger retro job, restart MCP server, etc.)
- llm-broker gains `GET /v1/broker/slots` (on the gateway, proxied via messaging) returning the live slot snapshot with slot ID, class, state, agent, priority, age
- Broker config refactor: reads `agent_slot_count`, `hindsight_slot_count`, `preempt_timeout_ms`, `provisional_timeout_ms`, `slot_snapshot_interval_s` from Valkey config `config:broker` (with env fallback for bootstrap)
- llm-proxy config refactor: `slot_timeout_ms`, `request_timeout_ms` read from `config:llm_proxy` (with env fallback)
- Retro config: `mental_model_refresh_s` added to the retro config panel (cadence for future mental-model-refresh work; stored but not yet consumed, documented)

## Capabilities

### New Capabilities

- None — all work extends existing capabilities.

### Modified Capabilities

- `dashboard-panel-registry`: adds `action` as a fourth section kind (button that POSTs to a declared URL with optional confirmation prompt and body)
- `llm-broker`: registers a dashboard panel, exposes a new `GET /v1/broker/slots` endpoint via the gateway, moves its slot budget + timeout config from env-only to Valkey-backed (env fallback)
- `llm-proxy`: registers a dashboard panel, moves its timeouts from env-only to Valkey-backed
- `retrospection`: registers a dashboard panel for retro-agent, adds manual-trigger action endpoints
- `mcp-integration`: registers a dashboard panel for main-agent's MCP server management (replacing the current hardcoded MCP UI in the old Agents panel)
- `dashboard-ui`: the panels Broker, LLM Proxy, Retro, and MCP appear in the dashboard after this change, contributed by their owning services

## Impact

- Four services gain registration-time panel descriptor construction: llm-broker, llm-proxy, retro-agent, main-agent (retro-agent and main-agent already register; llm-broker and llm-proxy need to start registering)
- llm-broker and llm-proxy gain heartbeat goroutines (neither registers today)
- llm-broker gains a new HTTP hop: the gateway exposes `GET /v1/broker/slots`, but the data lives in the broker. Option A: gateway proxies via a new messaging request/response round-trip. Option B: broker exposes its own tiny HTTP port and the gateway fetches. Design doc picks.
- New `action` section kind in `internal/dashboard` descriptor types + validator + dashboard JS renderer
- Broker config reads shift from `envInt(...)` to `config.ResolveBroker(ctx, cfgStore)` with env var still respected at bootstrap if Valkey is empty
- llm-proxy gains a `config.ResolveLLMProxy` function and a `config:llm_proxy` Valkey section
- `.env.example` updated with notes that broker + llm-proxy settings are now runtime-tunable; env vars remain valid as bootstrap values
- Dashboard JS gains an `action` kind renderer (button + optional confirm dialog + response-status indicator)
- Test surface: four new panel-descriptor construction unit tests; action endpoint tests; broker slot snapshot endpoint test; config-reading tests for llm-broker and llm-proxy covering the new runtime-tunable paths
- Not in scope: MCP CRUD rewrite on main-agent's side — main-agent's existing MCP endpoints stay as they are; we only change where the dashboard calls them from (panel descriptor instead of hardcoded HTML)
- Not in scope: real-time slot-table updates in the dashboard (status section fetches on tab open and on manual refresh; SSE-for-slots would be a follow-up if operators want it)
