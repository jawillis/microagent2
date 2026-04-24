## 1. Config Store Foundation

- [x] 1.1 Create `internal/config/` package with typed structs for ChatConfig, MemoryConfig, BrokerConfig, and RetroConfig including JSON tags and hardcoded defaults
- [x] 1.2 Implement `Store` that reads/writes config sections to Valkey `config:<section>` keys as JSON
- [x] 1.3 Implement `Resolve` functions that merge Valkey values over env vars over hardcoded defaults (three-tier resolution)
- [x] 1.4 Write tests for config resolution: Valkey present wins, Valkey absent falls back to env, both absent falls back to default

## 2. Service Startup Integration

- [x] 2.1 Update `cmd/gateway/main.go` to read ChatConfig via config store at startup and pass request_timeout_s and model to gateway server
- [x] 2.2 Update `cmd/context-manager/main.go` (or equivalent) to read MemoryConfig and ChatConfig via config store and pass to context manager/assembler/muninn client
- [x] 2.3 Update `cmd/llm-broker/main.go` (or equivalent) to read BrokerConfig and ChatConfig via config store and pass slot_count, preempt_timeout_ms, and model to broker
- [x] 2.4 Update `cmd/retro-agent/main.go` (or equivalent) to read RetroConfig via config store and pass inactivity_timeout_s, skill_dup_threshold, min_history_turns, and curation_categories to retro components

## 3. Replace Hardcoded Values

- [x] 3.1 Update `internal/context/manager.go` to accept recall_limit from config instead of hardcoded `5`
- [x] 3.2 Update `internal/context/muninn.go` to accept recall_threshold, max_hops, vault, prewarm_limit, and store_confidence from config instead of hardcoded values
- [x] 3.3 Update `internal/context/assembler.go` to accept system_prompt from config
- [x] 3.4 Update `internal/broker/broker.go` to accept model name from config instead of hardcoded `"default"`
- [x] 3.5 Update `internal/retro/trigger.go` to accept inactivity_timeout_s from config
- [x] 3.6 Update `internal/retro/job.go` to accept skill_dup_threshold, min_history_turns, and curation_categories from config instead of hardcoded values

## 4. Hybrid Session ID

- [x] 4.1 Update gateway chat completions request struct to include optional `session_id` field
- [x] 4.2 Replace `deriveSessionID` in `internal/gateway/server.go` with logic: use client-provided session_id if present, otherwise generate UUIDv4
- [x] 4.3 Add `X-Session-ID` response header and `session_id` field to both streaming and non-streaming response bodies
- [x] 4.4 Write tests for hybrid session ID: client-provided preserved, absent generates UUID, header set on streaming responses

## 5. Config API Endpoints

- [x] 5.1 Implement `GET /v1/config` handler that reads all four config sections and returns them as a JSON object with keys chat, memory, broker, retro
- [x] 5.2 Implement `PUT /v1/config` handler that accepts `{section, values}`, validates section name, and writes to config store
- [x] 5.3 Register config routes on the gateway router
- [x] 5.4 Write tests for config API: read all, update section, invalid section returns 400

## 6. Session Management API

- [x] 6.1 Implement `GET /v1/sessions` handler that scans Valkey for `session:*:history` keys and returns session_id, turn_count, last_active
- [x] 6.2 Implement `GET /v1/sessions/:id` handler that returns full chat history for a session, or 404 if not found
- [x] 6.3 Implement `DELETE /v1/sessions/:id` handler that removes session history from Valkey, or 404 if not found
- [x] 6.4 Register session routes on the gateway router
- [x] 6.5 Write tests for session API: list, view existing, view missing (404), delete existing, delete missing (404)

## 7. Retro Triggering API and Locking

- [x] 7.1 Implement retro lock helpers in `internal/retro/` or `internal/config/`: acquire lock via SET NX with 300s TTL, release lock via DEL
- [x] 7.2 Implement `POST /v1/retro/:session/trigger` handler: validate job_type, check session exists, acquire lock, publish to `stream:retro:triggers`, return 202/409/400/404
- [x] 7.3 Add lock acquisition to retro job execution (inactivity-triggered and externally-triggered jobs)
- [x] 7.4 Add lock cleanup (DEL) in retro job completion path (success and error), ignoring missing keys
- [x] 7.5 Implement stream consumer in retro agent for `stream:retro:triggers` messages
- [x] 7.6 Register retro trigger route on the gateway router
- [x] 7.7 Write tests for retro triggering: valid trigger returns 202, duplicate returns 409, invalid job_type returns 400, missing session returns 404, lock released on completion

## 8. System Health Endpoint

- [x] 8.1 Implement Valkey health check (PING, 3s timeout)
- [x] 8.2 Implement llama.cpp health check (HTTP GET /health, 5s timeout)
- [x] 8.3 Implement MuninnDB health check (HTTP GET /api/health, 5s timeout)
- [x] 8.4 Implement `GET /v1/status` handler that runs all health checks and includes system metadata (gateway port, service addresses) and registered agents from the agent registry
- [x] 8.5 Register status route on the gateway router
- [x] 8.6 Write tests for health endpoint: all healthy, partial failure, agent registry included

## 9. Dashboard Frontend

- [x] 9.1 Create `web/` directory with `index.html` containing tabbed navigation for five panels (Chat, Memory, Agents, Sessions, System)
- [x] 9.2 Create `web/style.css` with dashboard layout, form styling, table styling, status indicators
- [x] 9.3 Create `web/app.js` with panel switching logic, API fetch helpers, and form save/load functions
- [x] 9.4 Implement Chat panel: system prompt textarea, model input, timeout input, save button calling PUT /v1/config
- [x] 9.5 Implement Memory panel: recall_limit, recall_threshold, max_hops, prewarm_limit, vault, store_confidence fields with save button
- [x] 9.6 Implement Agents panel: broker settings (slot_count, preempt_timeout_ms) with save, agent registry table from /v1/status, retro policy settings with save
- [x] 9.7 Implement Sessions panel: session table from /v1/sessions, view history modal/panel, delete button, retro trigger buttons per session
- [x] 9.8 Implement System panel: health status indicators (green/red) from /v1/status, infrastructure info display (addresses, ports)

## 10. Dashboard Embedding and Route Setup

- [x] 10.1 Add `//go:embed web/*` directive in gateway package and serve static files at `GET /` using `http.FileServer` with `embed.FS`
- [x] 10.2 Ensure dashboard route does not conflict with `/v1/*` API routes (static files served for non-API paths)
- [x] 10.3 Verify the complete gateway builds and the dashboard loads in a browser with all panels functional
