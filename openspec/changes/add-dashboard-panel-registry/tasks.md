## 1. Descriptor types and validation

- [ ] 1.1 Create `internal/dashboard` package with `PanelDescriptor`, `Section`, `FormSection`, `IframeSection`, `StatusSection`, `FieldSchema` Go types
- [ ] 1.2 Implement `ValidateDescriptor(d PanelDescriptor) error` covering: version check, required fields, section kind allowlist, field type allowlist, `values` required for enum, order clamping (< 100 → 100)
- [ ] 1.3 Implement JSON marshal/unmarshal with custom section-kind discriminator (so `{"kind":"form",...}` parses into the right struct)
- [ ] 1.4 Unit tests in `internal/dashboard` covering all validation branches (missing title, empty sections, unknown kind, unknown field type, invalid enum values, order clamp)

## 2. Registry payload extension

- [ ] 2.1 Add optional `DashboardPanel *dashboard.PanelDescriptor` field to `messaging.RegisterPayload` (JSON tag `dashboard_panel,omitempty`)
- [ ] 2.2 Round-trip serialization tests in `internal/messaging/payloads_test.go`: absent field, populated field with each section kind
- [ ] 2.3 Update `internal/registry/consumer.go` and `internal/registry/agent.go` to persist the descriptor on the in-memory agent record
- [ ] 2.4 Update `AgentInfo` (or equivalent struct) to carry the descriptor; preserve it across heartbeat cycles; drop it on deregister / death

## 3. Gateway aggregation endpoint

- [ ] 3.1 New `internal/gateway/dashboard.go` with an aggregator that reads from the registry, collects descriptors, applies ordering rules (explicit order first, then service ID), and returns a flat list
- [ ] 3.2 Gateway-side built-in descriptor registry: a small slice of `PanelDescriptor` hardcoded in the gateway for Chat + Sessions + System panels. Attach to the aggregator at startup. Use `order: 10, 80, 90` respectively.
- [ ] 3.3 `GET /v1/dashboard/panels` handler on the gateway mux. Returns `{"panels": [...]}`
- [ ] 3.4 Validate descriptors received from services at registration time; reject invalid ones with a WARN log and omit from aggregation
- [ ] 3.5 Handler tests with a fake registry containing a mix of valid and invalid descriptors

## 4. Built-in panel descriptors

- [ ] 4.1 Chat descriptor: one form section with `config_key: "chat"`, fields `system_prompt` (textarea), `model` (string), `request_timeout_s` (integer, min 1)
- [ ] 4.2 Sessions descriptor: one status section with `layout: "table"`, `url: "/v1/sessions"`; plus interactive controls (view / delete / trigger retro). Interactive controls may require a new section kind OR a custom handling path; design placeholder — treat Sessions as partially built-in this change if interactive controls don't fit the descriptor shape.
- [ ] 4.3 System descriptor: one status section with `layout: "key_value"` pointing at `/v1/status`; a second status section with `layout: "table"` for services health.
- [ ] 4.4 Memory descriptor: NOT included (removed from shell per spec; returns in follow-up `add-memory-panel-contribution`)
- [ ] 4.5 Agents descriptor: NOT included (removed from shell per spec; returns in follow-up `add-agents-panel-contributions`)

## 5. Dashboard shell refactor

- [ ] 5.1 HTML: reduce `index.html` to header + nav container (empty) + `<main>` with a single `<div id="panel-host">` that panels render into
- [ ] 5.2 JS: on DOMContentLoaded, fetch `GET /v1/dashboard/panels`, build tabs from the response, render each panel's sections into a container
- [ ] 5.3 Form rendering: for each `form` section, build a `<form>` with labeled inputs per field schema, a Save button, and a status span. Save submits via existing `api("PUT", "/v1/config", {section, values})` helper
- [ ] 5.4 Iframe rendering: emit `<iframe>` with `src`, `sandbox="allow-scripts allow-same-origin allow-forms"`, and CSS height from `height` or default
- [ ] 5.5 Status rendering: GET the declared URL, render `key_value` as `<dl>` or `table` as a table matching the response shape
- [ ] 5.6 Remove existing panel-specific JS (memory form, broker form, retro form, MCP table setup) from `app.js` — all of it becomes descriptor-driven or moves to follow-ups
- [ ] 5.7 Tab-click navigation using the existing `.panel` / `.active` CSS classes; panel activation toggles `active` class
- [ ] 5.8 Empty-state render when panels list is empty

## 6. Sessions / MCP / retro trigger handling

- [ ] 6.1 Decide descriptor shape for Sessions (list + actions) — if status+table isn't enough, extend with an `actions` section kind, OR render Sessions as a gateway-built-in with some custom JS that the shell calls by `panel_id === "sessions"`. Favor the former for consistency; document the shape in design if different from spec.
- [ ] 6.2 MCP CRUD: currently on the Agents panel. In this change, MCP CRUD is NOT in scope (Agents panel moves to a follow-up). Add a small Note panel to the built-ins list describing where MCP / memory / broker config has temporarily moved (PUT /v1/config, PUT /v1/mcp/servers), OR leave gap and rely on the follow-up change landing quickly. Prefer the follow-up; document the gap in the change proposal (already documented).

## 7. Service-side registration updates (minimal — no new panels added here)

- [ ] 7.1 Existing services (main-agent, retro-agent, memory-service, context-manager, llm-proxy) do NOT register a panel in this change. Their `RegisterPayload` construction code is audited to confirm the new field is absent (backward-compatible)
- [ ] 7.2 No code changes required to those services for this change to land

## 8. Observability

- [ ] 8.1 Gateway logs at INFO on each descriptor validation: `dashboard_panel_validated` with service ID and panel title on success; `dashboard_panel_invalid` at WARN with service ID + reason on failure
- [ ] 8.2 Aggregation endpoint logs at DEBUG per call with count and service IDs

## 9. Validation

- [ ] 9.1 `go build ./...` green
- [ ] 9.2 `go test ./...` green; new `internal/dashboard` tests green; new gateway-side aggregation tests green
- [ ] 9.3 `openspec validate add-dashboard-panel-registry --strict` green
- [ ] 9.4 Manual: bring up docker compose; browse to `/`; verify Chat and System panels render from descriptor; verify Save on Chat still PUTs `/v1/config`; verify Memory and Agents tabs are absent (documented gap); verify no JS errors in browser console
