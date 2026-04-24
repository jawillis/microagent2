## 1. Action section kind in dashboard-panel-registry

- [ ] 1.1 Add `ActionSection` struct to `internal/dashboard` with `Title` and `Actions []Action`
- [ ] 1.2 Add `Action` struct with `Label`, `URL`, `Method`, `Body`, `Params`, `Confirm`, `StatusKey`
- [ ] 1.3 Add `ActionParam` struct with `Name`, `Type`, `Required`, `Label`, `Description`, `Default`
- [ ] 1.4 Extend discriminator unmarshal + validator for `kind: "action"`
- [ ] 1.5 Unit tests for action descriptor validation (missing actions, unknown method, unknown param type)

## 2. Broker slot snapshot messaging + endpoint

- [ ] 2.1 Add `SlotSnapshotRequest` and `SlotSnapshotResponse` payload types in `internal/messaging/payloads.go`
- [ ] 2.2 Add message types `TypeSlotSnapshotRequest` and `TypeSlotSnapshotResponse` in `internal/messaging/message.go`
- [ ] 2.3 In `internal/broker/broker.go`, consume a new stream `stream:broker:slot-snapshot-requests`; on message, reply with the current `SlotTable.Snapshot()` contents
- [ ] 2.4 Gateway handler `GET /v1/broker/slots` publishes a SlotSnapshotRequest, waits on its reply stream, returns `{"slots": [...]}` or 503 on timeout
- [ ] 2.5 Integration test: start broker + gateway with a fake slot state, GET `/v1/broker/slots`, verify shape
- [ ] 2.6 Test timeout path (broker unreachable → 503)

## 3. Broker config migration to Valkey

- [ ] 3.1 Extend `internal/config/config.go` `BrokerConfig` with `AgentSlotCount`, `HindsightSlotCount`, `ProvisionalTimeoutMS`, `SlotSnapshotIntervalS`
- [ ] 3.2 `DefaultBrokerConfig` sets existing + new defaults
- [ ] 3.3 `ResolveBroker` reads all fields from `config:broker`, falls back to env (`AGENT_SLOT_COUNT`, `HINDSIGHT_SLOT_COUNT`, `PREEMPT_TIMEOUT_MS`, `PROVISIONAL_TIMEOUT_MS`, `SLOT_SNAPSHOT_INTERVAL_S`), then defaults
- [ ] 3.4 `cmd/llm-broker/main.go` reads via `ResolveBroker` (replacing direct env reads for these values); keeps SLOT_COUNT validation
- [ ] 3.5 Broker re-resolves preempt/provisional/snapshot timeouts at request time (or via short-cache); slot-count values remain startup-only
- [ ] 3.6 Tests covering Valkey-wins, env-fallback, defaults

## 4. Broker registration + heartbeat + panel descriptor

- [ ] 4.1 Add `registry.NewAgentRegistrar` invocation in `cmd/llm-broker/main.go` with `agent_id: "llm-broker"`, `capabilities: ["llm-broker"]`
- [ ] 4.2 Heartbeat goroutine
- [ ] 4.3 `internal/broker/panel.go` builds the panel descriptor (form + status sections)
- [ ] 4.4 Attach descriptor to RegisterPayload, register, deregister on shutdown
- [ ] 4.5 Unit test for descriptor shape
- [ ] 4.6 Integration test: bring up broker, confirm `/v1/dashboard/panels` includes the Broker descriptor

## 5. llm-proxy config migration + registration

- [ ] 5.1 Add `LLMProxyConfig` + `ResolveLLMProxy` in `internal/config/config.go`; Valkey → env (`LLM_PROXY_SLOT_TIMEOUT_MS`, `LLM_PROXY_REQUEST_TIMEOUT_MS`) → defaults
- [ ] 5.2 `cmd/llm-proxy/main.go` reads via ResolveLLMProxy; re-reads per request
- [ ] 5.3 Registration + heartbeat in `cmd/llm-proxy/main.go`
- [ ] 5.4 `internal/llmproxy/panel.go` builds descriptor with form section + readonly identity
- [ ] 5.5 Register with descriptor, deregister on shutdown
- [ ] 5.6 Unit test for descriptor shape

## 6. Retro panel descriptor

- [ ] 6.1 Extend `RetroConfig` with `CurationRecallLimit` (if not already present) and `MentalModelRefreshS`
- [ ] 6.2 `internal/retro/panel.go` builds the descriptor with form + action sections
- [ ] 6.3 Action section declares three trigger actions with session_id param
- [ ] 6.4 In `cmd/retro-agent/main.go`, attach descriptor to the existing RegisterPayload
- [ ] 6.5 Unit test for descriptor shape + action params

## 7. MCP panel descriptor

- [ ] 7.1 `internal/agent/mcp_panel.go` (or similar) builds the MCP panel descriptor: status table + add/remove actions + invoke_timeout form
- [ ] 7.2 In `cmd/main-agent/main.go`, attach descriptor to the existing RegisterPayload
- [ ] 7.3 Verify existing `/v1/mcp/servers` endpoints accept the body shapes declared by the action params
- [ ] 7.4 Unit test for descriptor shape

## 8. Dashboard action renderer

- [ ] 8.1 Add renderer in `app.js` for `kind: "action"` — title + button per action + param inputs
- [ ] 8.2 Build request body from params and static `body`; substitute `{name}` tokens in URL template
- [ ] 8.3 Handle `method` override; default POST
- [ ] 8.4 Confirm dialog on `confirm` field
- [ ] 8.5 Render success using `status_key`; error on non-2xx
- [ ] 8.6 Refresh adjacent status section after successful action (MCP add → refresh server table)
- [ ] 8.7 Manual QA across all four new action panels

## 9. Docker-compose + env

- [ ] 9.1 No new docker-compose services; existing env vars continue to bootstrap until Valkey overrides exist
- [ ] 9.2 Document new config sections in `.env.example`

## 10. Validation

- [ ] 10.1 `go build ./...` green
- [ ] 10.2 `go test ./...` green
- [ ] 10.3 `openspec validate add-agents-panel-contributions --strict` green
- [ ] 10.4 Manual: dashboard shows Broker + LLM Proxy + Retro + MCP tabs; broker slot table reflects live state; editing preempt_timeout_ms takes effect without restart; triggering a retro job from the Retro panel works; MCP add/remove works
