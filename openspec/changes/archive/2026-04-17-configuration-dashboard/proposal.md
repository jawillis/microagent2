## Why

All runtime-tunable settings (system prompt, memory thresholds, slot count, retro policy) are hardcoded or only settable via env vars at deploy time. There is no way to inspect system health, view sessions, or trigger retrospection jobs without CLI access to Valkey. The operator needs a single interface to configure agent behavior, manage sessions, and monitor system status without redeploying containers.

## What Changes

- Add a configuration dashboard served from the gateway service (static HTML/CSS/JS + REST API)
- Introduce a config storage layer in Valkey (`config:*` keyspace) with env var fallback, so all services resolve runtime settings from a central store
- Implement hybrid session ID strategy: clients may provide a `session_id` in chat requests, otherwise the gateway auto-generates one and returns it via response header and body
- Add session management endpoints to list, view, and delete sessions
- Add manual retro job triggering per session (memory extraction, skill creation, curation) with per-session locking to prevent duplicate runs
- Add a system health endpoint that reports connectivity to Valkey, llama.cpp, and MuninnDB
- Extract all hardcoded tuning parameters into the config layer: memory recall settings, retro policy thresholds, broker slot/preempt settings, and chat behavior defaults

## Capabilities

### New Capabilities
- `configuration-store`: Centralized config read/write against Valkey with env var fallback. All services resolve tunable settings from `config:*` keys.
- `dashboard-ui`: Web-based dashboard served from the gateway. Five panels: Chat, Memory, Agents, Sessions, System. Static SPA with REST API backend.
- `session-management`: Hybrid session ID strategy (client-provided or auto-generated), session listing, history viewing, and session deletion.
- `retro-triggering`: Manual trigger of retrospection jobs (memory extraction, skill creation, curation) per session from the dashboard, with Valkey-based locking to prevent concurrent duplicate runs.
- `system-health`: Health check endpoint reporting connectivity status for Valkey, llama.cpp, and MuninnDB.

### Modified Capabilities
- `gateway-api`: Chat completions endpoint gains hybrid session ID support (optional `session_id` field in request, `X-Session-ID` response header, `session_id` in response body). New routes added for config, sessions, retro triggers, health, and dashboard static files.
- `context-management`: Memory recall settings (limit, threshold, max hops, pre-warm count, vault name, store confidence) become configurable via the config store instead of hardcoded values.
- `llm-broker`: Slot count, preempt timeout, and model name become configurable via the config store instead of hardcoded/env-only values.
- `retrospection`: Inactivity timeout, skill duplicate threshold, minimum history for skills, and curation categories become configurable. Jobs gain a locking mechanism and can be triggered externally via messaging.

## Impact

- **Gateway service**: Significant expansion — new HTTP routes, static file serving, config API, session API, health API
- **All services** (`cmd/*`): Startup logic changes to read config from Valkey with env var fallback
- **Internal packages**: `internal/config/` (new), modifications to `internal/gateway/`, `internal/context/`, `internal/broker/`, `internal/retro/`
- **Frontend**: New `web/` directory with dashboard SPA (HTML/CSS/JS, no framework)
- **API surface**: New REST endpoints under `/v1/config`, `/v1/sessions`, `/v1/retro`, `/v1/status`, and `GET /` for dashboard
- **Docker**: Gateway container needs to serve static files from `web/`
- **Dependencies**: No new Go dependencies expected (Valkey access already via go-redis)
