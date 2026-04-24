## 1. Descriptor types and validation

- [x] 1.1 Create `internal/dashboard` package with `PanelDescriptor`, `Section`, `FormSection`, `IframeSection`, `StatusSection`, `FieldSchema` Go types
- [x] 1.2 Implement `ValidateDescriptor(d PanelDescriptor) error` covering: version check, required fields, section kind allowlist, field type allowlist, `values` required for enum, order clamping (< 100 → 100)
- [x] 1.3 Implement JSON marshal/unmarshal with custom section-kind discriminator (so `{"kind":"form",...}` parses into the right struct)
- [x] 1.4 Unit tests in `internal/dashboard` covering all validation branches (missing title, empty sections, unknown kind, unknown field type, invalid enum values, order clamp)

## 2. Registry payload extension

- [x] 2.1 Add optional `DashboardPanel *dashboard.PanelDescriptor` field to `messaging.RegisterPayload` (JSON tag `dashboard_panel,omitempty`)
- [x] 2.2 Round-trip serialization tests in `internal/messaging/payloads_test.go`: absent field, populated field with each section kind
- [x] 2.3 Update `internal/registry/consumer.go` and `internal/registry/agent.go` to persist the descriptor on the in-memory agent record
- [x] 2.4 Update `AgentInfo` (or equivalent struct) to carry the descriptor; preserve it across heartbeat cycles; drop it on deregister / death

## 3. Gateway aggregation endpoint

- [x] 3.1 New `internal/gateway/dashboard.go` with an aggregator that reads from the registry, collects descriptors, applies ordering rules (explicit order first, then service ID), and returns a flat list
- [x] 3.2 Gateway-side built-in descriptor registry: a small slice of `PanelDescriptor` hardcoded in the gateway for Chat + Sessions + System panels. Attach to the aggregator at startup. Use `order: 10, 80, 90` respectively.
- [x] 3.3 `GET /v1/dashboard/panels` handler on the gateway mux. Returns `{"panels": [...]}`
- [x] 3.4 Validate descriptors received from services at registration time; reject invalid ones with a WARN log and omit from aggregation
- [x] 3.5 Handler tests with a fake registry containing a mix of valid and invalid descriptors

## 4. Built-in panel descriptors

- [x] 4.1 Chat descriptor: one form section with `config_key: "chat"`, fields `system_prompt` (textarea), `model` (string), `request_timeout_s` (integer, min 1)
- [x] 4.2 Sessions descriptor: one status section with `layout: "table"`, `url: "/v1/sessions"` — interactive controls (view detail / delete / trigger retro) are deferred to the `action` section kind follow-up (`add-agents-panel-contributions`); for now Sessions is a list view only
- [x] 4.3 System descriptor: one status section with `layout: "key_value"` pointing at `/v1/status` (the key-value renderer handles the nested `services`/`agents`/`system` sub-objects uniformly)
- [x] 4.4 Memory descriptor: NOT included (removed from shell per spec; returns in follow-up `add-memory-panel-contribution`)
- [x] 4.5 Agents descriptor: NOT included (removed from shell per spec; returns in follow-up `add-agents-panel-contributions`)

## 5. Dashboard shell refactor

- [x] 5.1 HTML: reduce `index.html` to header + nav container (empty) + `<main>` with a single `<div id="panel-host">` that panels render into
- [x] 5.2 JS: on DOMContentLoaded, fetch `GET /v1/dashboard/panels`, build tabs from the response, render each panel's sections into a container
- [x] 5.3 Form rendering: for each `form` section, build a `<form>` with labeled inputs per field schema, a Save button, and a status span. Save submits via existing `api("PUT", "/v1/config", {section, values})` helper
- [x] 5.4 Iframe rendering: emit `<iframe>` with `src`, `sandbox="allow-scripts allow-same-origin allow-forms"`, and CSS height from `height` or default
- [x] 5.5 Status rendering: GET the declared URL, render `key_value` as `<dl>` or `table` as a table matching the response shape
- [x] 5.6 Remove existing panel-specific JS (memory form, broker form, retro form, MCP table setup) from `app.js` — all of it becomes descriptor-driven or moves to follow-ups
- [x] 5.7 Tab-click navigation using the existing `.panel` / `.active` CSS classes; panel activation toggles `active` class
- [x] 5.8 Empty-state render when panels list is empty

## 6. Sessions / MCP / retro trigger handling

- [x] 6.1 Decide descriptor shape for Sessions (list + actions) — defer action/delete/trigger to the `action` section kind landing with `add-agents-panel-contributions`. For this change, Sessions is a simple table view only. Documented in tasks 4.2 and the migration notes in dashboard-ui spec.
- [x] 6.2 MCP CRUD: not in scope this change. Documented in the spec's REMOVED Requirements migration note — MCP editing via `PUT /v1/mcp/servers` directly until `add-agents-panel-contributions` lands.

## 7. Service-side registration updates (minimal — no new panels added here)

- [x] 7.1 Existing services do NOT register a panel in this change. Their `RegisterPayload` construction is backward-compatible because `DashboardPanel` is `*dashboard.PanelDescriptor` with `omitempty` — nil encodes to nothing.
- [x] 7.2 No code changes to main-agent / retro-agent / memory-service / context-manager / llm-proxy required; their existing registrations continue to work.

## 8. Observability

- [x] 8.1 Gateway logs at INFO on each descriptor validation: `dashboard_panel_validated` with service ID and panel title on success; `dashboard_panel_invalid` at WARN with service ID + reason on failure (in `internal/registry/consumer.go`)
- [x] 8.2 Aggregation endpoint logs at DEBUG per call with panel count

## 9. Validation

- [x] 9.1 `go build ./...` green
- [x] 9.2 `go test ./...` green (including 20 new dashboard-package tests and new gateway aggregator tests)
- [x] 9.3 `openspec validate add-dashboard-panel-registry --strict` green
- [x] 9.4 Manual: live `/v1/dashboard/panels` returns Chat/Sessions/System descriptors ordered 10/80/90; all three services have `service_id="gateway"`; all descriptors pass validation
