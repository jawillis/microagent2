## Why

The dashboard's five panels are hardcoded against a specific set of services and their specific configuration fields. As services have evolved (memory-service, llm-proxy, Hindsight, slot classes, provenance conventions), the dashboard hasn't kept up: the memory panel still shows muninn-era fields that no longer exist, new services have no visibility, and adding visibility means hand-editing HTML/JS in the gateway repo.

In a microservice architecture, a service should describe its own dashboard presence — not require the gateway to know about it ahead of time. This proposal extends the existing service registration protocol with an optional `dashboard_panel` descriptor. Services that opt in declare what they want shown; the dashboard shell composes panels from whatever is registered. Services that don't opt in cost nothing.

The scope is deliberately declarative, not a plugin system. Services describe their panel as a small JSON structure (form schemas, iframe URLs, status queries). Dashboard renders forms and embeds iframes from the descriptor. No HTML or JS delivery from services. This gives ~80% of the "services own their UI" benefit for ~10% of the cost and none of the sandboxing risk.

This change is a prerequisite for two follow-ups that depend on it:
- Memory panel refresh (iframe Hindsight's Control Plane + small microagent2-specific form contributed by memory-service)
- Cross-service logs panel (subscribes to `log:<service>` Valkey streams)

## What Changes

- Extend `messaging.RegisterPayload` with an optional `dashboard_panel` field containing a typed descriptor. Existing agents that don't populate it are unaffected.
- New panel descriptor schema (Go types): a panel has a `title`, an `order` hint, and a list of `sections`. Section kinds in this change: `form` (renders from JSON schema, backed by a `config_key`), `iframe` (embeds a URL), `status` (renders a read-only key-value or table view fetched from a URL).
- New gateway endpoint `GET /v1/dashboard/panels` that aggregates all currently-registered panel descriptors into a single response. Response is ordered by `order` field; ties broken by service ID.
- Dashboard shell refactored so it composes tabs and panel content from the registry response rather than hardcoding panel content. The existing Chat / Memory / Agents / Sessions / System panels that are part of the gateway itself continue to ship as gateway-built-in panels (rendered alongside registered ones). This lets us migrate panels incrementally.
- Gateway itself becomes a registered participant for its built-in panels (Chat, Sessions, System). It does not replace the hand-written forms in this change — just declares them so the composition mechanism can handle everything uniformly.
- JSON-schema-driven form rendering: a minimal schema dialect (`type: string|number|integer|boolean|enum|textarea`, `min`/`max`/`step`/`values`/`label`/`description`) drives form generation. Submission POSTs to the declared `config_key` via the existing `PUT /v1/config` endpoint.

## Capabilities

### New Capabilities

- `dashboard-panel-registry`: the registry contract extension and gateway aggregation endpoint that let services declare their dashboard presence declaratively.

### Modified Capabilities

- `agent-registration`: the self-registration announcement adds an optional `dashboard_panel` field; existing required fields unchanged.
- `dashboard-ui`: the dashboard no longer hardcodes panel content. It composes panels from `GET /v1/dashboard/panels`. The existing panels continue to work as they always have, but they are now delivered via the composition mechanism rather than hardcoded in the HTML/JS shell.

## Impact

- New messaging field: `dashboard_panel` on `RegisterPayload` (optional, backward-compatible — unset is omitted).
- New gateway endpoint: `GET /v1/dashboard/panels` — returns aggregated panel descriptors.
- Dashboard shell changes: HTML keeps the top-level tab bar and a container element per panel; JS fetches descriptors and renders forms/iframes/status sections inside each container. Existing inline forms are removed from HTML and become descriptor-driven.
- New Go package `internal/dashboard` (or similar) containing the panel descriptor types, schema-driven form rendering helpers (Go-side: validation of descriptor structure at registration time), and the aggregation logic.
- Each existing service gains a few lines: declare its panel in its registration code. Services without panels are unaffected.
- Registry consumer (`internal/registry/consumer.go`) stores `dashboard_panel` on the in-memory agent record so the gateway can read it when responding to `GET /v1/dashboard/panels`.
- Test surface: unit tests for descriptor validation, schema-driven form rendering (JS), registry aggregation; integration test confirming a registered service's panel shows up in the dashboard response.
- No runtime-tunable-knob refactors in this change — existing env-only knobs stay env-only; turning them into runtime-tunable is a separate concern.
- No existing functionality regresses. The five built-in panels continue to appear and work identically after migration.
