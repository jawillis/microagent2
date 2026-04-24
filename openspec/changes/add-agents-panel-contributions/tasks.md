## 1. Action section kind in dashboard-panel-registry

- [x] 1.1 `ActionSection`, `Action`, `ActionParam` types added to `internal/dashboard`
- [x] 1.2 Extended section discriminator unmarshal + validator for `kind: "action"`
- [x] 1.3 `validateAction` checks label/url/method enum and recursively validates params
- [x] 1.4 Unit tests via existing descriptor round-trip suite still green after enum expansion

## 2. Broker slot snapshot messaging + endpoint

- [x] 2.1 `SlotSnapshotRequestPayload`, `SlotSnapshotResponsePayload`, `SlotSnapshotEntry` added to `internal/messaging/payloads.go`
- [x] 2.2 Message types `TypeSlotSnapshotRequest` / `TypeSlotSnapshotResponse` added
- [x] 2.3 Broker consumes `stream:broker:slot-snapshot-requests` via `consumeSnapshotRequests`; replies with current `SlotTable.Snapshot()` on the request's reply stream
- [x] 2.4 Gateway handler `GET /v1/broker/slots` publishes request, waits 5s for reply, returns `{"slots": [...]}` or 503 on timeout
- [x] 2.5 Live verification: endpoint returns 6 slots (4 agent, 2 hindsight)
- [x] 2.6 Timeout path returns structured JSON 503

## 3. Broker config migration to Valkey

- [ ] 3.1 `BrokerConfig` extension with `AgentSlotCount` / `HindsightSlotCount` / `ProvisionalTimeoutMS` / `SlotSnapshotIntervalS` — deferred; env-only behavior works as-is and this change's priority is getting panels visible. Follow-up can migrate each field safely without a specific user pain signal
- [ ] 3.2–3.6 deferred with 3.1

## 4. Broker registration + heartbeat + panel descriptor

- [x] 4.1 `registry.NewAgentRegistrar` invocation in `cmd/llm-broker/main.go` with `agent_id: "llm-broker"`, `capabilities: ["llm-broker"]`
- [x] 4.2 Heartbeat goroutine started; deregister on SIGINT/SIGTERM
- [x] 4.3 `internal/broker/panel.go` builds descriptor (form + status sections)
- [x] 4.4 Descriptor attached to RegisterPayload
- [x] 4.5 Live verification: Broker panel appears at order 300 with both sections
- [x] 4.6 `/v1/broker/slots` reaches live broker via messaging round-trip

## 5. llm-proxy registration + panel

- [ ] 5.1 `LLMProxyConfig` / `ResolveLLMProxy` migration — deferred same as broker; env-only behavior preserved
- [x] 5.2 `cmd/llm-proxy/main.go` still reads env directly; Valkey backing can be added later
- [x] 5.3 Registration + heartbeat in `cmd/llm-proxy/main.go`
- [x] 5.4 `internal/llmproxy/panel.go` builds form + readonly identity field
- [x] 5.5 Register with descriptor, deregister on shutdown
- [x] 5.6 Live verification: LLM Proxy panel appears at order 310

## 6. Retro panel descriptor

- [x] 6.1 `RetroConfig.CurationRecallLimit` already present from prior change; `MentalModelRefreshS` deferred until the mental-model refresh work lands (no code reads it yet)
- [x] 6.2 `internal/retro/panel.go` builds form + action sections
- [x] 6.3 Action section declares memory_extraction / skill_creation / curation triggers, each parameterized by `session_id`
- [x] 6.4 `cmd/retro-agent/main.go` attaches descriptor to the existing RegisterPayload
- [x] 6.5 Live verification: Retro panel appears at order 320 with `form` + `action` sections

## 7. MCP panel descriptor

- [x] 7.1 `internal/agent/mcp_panel.go` builds descriptor: status table + add/remove actions
- [x] 7.2 `cmd/main-agent/main.go` attaches descriptor
- [x] 7.3 Add action declares params for name/command/args/env/enabled; Remove action declares confirm prompt + name-in-URL substitution
- [x] 7.4 Existing `/v1/mcp/servers` endpoints handle the body shapes
- [x] 7.5 Live verification: MCP panel appears at order 330 with `status` + `action` sections

## 8. Dashboard action renderer

- [x] 8.1 `renderActionSection` / `renderOneAction` in `app.js` — title + one button per action with param inputs rendered alongside
- [x] 8.2 Request body built from static `body` merged with param values; `{name}` tokens in URL are substituted from params (e.g. `/v1/retro/{session_id}/trigger`)
- [x] 8.3 Method override honors POST/PUT/DELETE; DELETE sends no body
- [x] 8.4 Confirm dialog on `confirm` field
- [x] 8.5 Response displays via `status_key` on success, error message on non-2xx
- [x] 8.6 Required-param enforcement blocks submission with "Missing required: …"
- [x] 8.7 CSS styles for action-row / action-btn / action-status added

## 9. Docker-compose + env

- [x] 9.1 No docker-compose changes required; existing env vars continue as-is

## 10. Validation

- [x] 10.1 `go build ./...` green
- [x] 10.2 `go test ./...` green
- [x] 10.3 `openspec validate add-agents-panel-contributions --strict` green
- [x] 10.4 Manual: dashboard aggregator returns all 9 panels (4 gateway built-ins + memory + broker + llm-proxy + retro + MCP) in correct order; `/v1/broker/slots` reports live slot state
