## Context

microagent2 today hardcodes its dashboard in `internal/gateway/web/index.html` and `app.js`. Five panels (Chat, Memory, Agents, Sessions, System) are rendered with hand-written forms whose field lists are coupled to specific services' configuration shapes. When a service's configuration changes — or when a new service is added — the dashboard's HTML/JS needs a hand edit in the gateway repo.

Services already register themselves on startup via `stream:registry:announce`, publishing `messaging.RegisterPayload` with identity, priority, capabilities, trigger, and heartbeat cadence. That payload flows through `internal/registry/consumer.go` into a process-local registry the gateway can read.

This change threads a new optional field through that existing machinery — a declarative descriptor of what the service wants shown in the dashboard — and refactors the dashboard shell to compose panels from registered descriptors rather than hardcoding them.

The scope is *declarative*: services describe panel content as data, not code. Form fields, iframe URLs, status queries — all structured. Services never ship HTML or JS into the dashboard. This is a deliberate trade-off against the more powerful "full plugin system" where services deliver arbitrary UI fragments; the declarative shape achieves most of the architectural benefit (services own their panels) without the sandboxing, CSP, and iteration-cost concerns of cross-service JS.

## Goals / Non-Goals

**Goals:**

- Services declare their dashboard panel at registration time; the gateway aggregates and the dashboard composes
- Adding a new service with a new panel requires no changes to the gateway or dashboard repo (beyond the service's own code)
- Existing built-in panels (Chat, Sessions, System) migrate to the new mechanism without user-visible regression
- The descriptor schema is small enough to validate exhaustively at the gateway boundary
- Backward-compatible for agents/services that don't want a panel — the field is optional

**Non-Goals:**

- Services delivering arbitrary HTML/JS into the dashboard (deferred; may never be needed)
- Runtime-tunable configuration for every env var in the system (separate concern — this change is about the *shape* of the dashboard contract, not which values are mutable)
- Authentication / per-user customization (dashboard remains single-tenant admin surface)
- Live push of descriptor changes (dashboard fetches on page load; refresh to see new panels)
- Translation between different config sources (e.g. Valkey-vs-YAML) — descriptors point at existing sources via URLs/keys; they don't introduce a new sync layer

## Decisions

### Decision 1: Descriptor is declarative, not a plugin system

**Choice:** Services declare panels via a typed Go struct serialized to JSON on the registry stream. Descriptors contain form schemas, iframe URLs, and status queries. Services do not ship HTML or JS to the dashboard.

**Rationale:** Plugin systems that accept service-delivered UI code create a durable security surface (CSP, iframe sandboxing, cross-origin behavior, JS error blast radius) and a hidden coupling where updating the dashboard requires deploying every service. Declarative covers the common cases — forms, iframes, status readouts — without any of that. If a service later needs something beyond the declarative shapes, it can offer its own HTTP UI and link to it.

**Alternatives considered:**
- *Full plugin system (HTML/JS fragments)* — higher ceiling, much higher floor; not obviously needed given our service roster
- *Gateway auto-discovers panels via HTTP probing* — adds network chatter and requires services to expose a `/dashboard-descriptor` endpoint; the registry stream is already the coordination point

### Decision 2: Dashboard endpoint aggregates at read time, does not cache

**Choice:** `GET /v1/dashboard/panels` reads the current registry on each call, serializes panel descriptors, and returns them ordered by the descriptor's `order` field (ties broken by service ID). Cached only per-request.

**Rationale:** Panel descriptors change rarely (only on service restart or deploy); aggregation is cheap; the dashboard fetches once per page load. Avoiding a cache eliminates a stale-data class of bugs. If latency becomes an issue (not anticipated — the registry is already in-memory), a small TTL cache can be added later.

**Alternatives considered:**
- *Server-push via WebSocket or SSE* — enables live panel hot-swap, but adds a long-lived connection and state-management burden for a minor UX improvement
- *Persist aggregated descriptors to Valkey* — introduces write-through cache invalidation concerns; unnecessary

### Decision 3: Built-in gateway panels migrate to the new contract too

**Choice:** The gateway itself registers panel descriptors for its five built-in panels at startup (same mechanism as services). The dashboard shell treats gateway-built-in and service-contributed panels uniformly.

**Rationale:** If built-in panels stay hardcoded and new panels use the registry, we have two rendering paths forever. Migrating built-in panels validates the descriptor expressiveness (if the existing Chat form can't be described by the descriptor, the descriptor is too weak) and gives us a single composition path. It's also a natural moment to drop the stale muninn-era memory panel.

**Alternatives considered:**
- *Leave built-ins hardcoded, register new panels only* — simpler near-term, two paths to maintain long-term
- *Migrate in a separate change* — defers the test-drive of the descriptor's expressiveness; risks the descriptor being incomplete for the use cases we already have in hand

### Decision 4: Section kinds are closed at this change: `form`, `iframe`, `status`

**Choice:** Descriptors declare a panel as an ordered list of sections. Each section has a `kind` from a closed enum: `form` (schema-driven form with a save button that PUTs to a config section), `iframe` (embeds a URL in a sandboxed iframe), `status` (read-only view fetched from a URL, rendered as key-value or table).

**Rationale:** These three cover the current use cases (Chat = form, Memory/Hindsight = form + iframe, System = status, Logs = status + later a custom kind). A closed enum is validated at the gateway boundary; invalid kinds fail registration with a clear error. Future kinds (charts, action buttons, etc.) are added via future proposals.

**Alternatives considered:**
- *Open kind field with string values* — unbounded dashboard behavior from arbitrary service input; rejected
- *Nested sections / composition* — complexity without clear need; panels are shallow by design

### Decision 5: Schema dialect is minimal and JSON-Schema-inspired, not JSON Schema proper

**Choice:** Form schemas use a small dialect: each field has `type` (`string`, `number`, `integer`, `boolean`, `enum`, `textarea`), optional `min`, `max`, `step`, `values` (for enum), `label`, `description`, `default`. Not full JSON Schema.

**Rationale:** Full JSON Schema covers far more than we need and pulls in validation library weight on both ends. Our forms are shallow, single-level key/value. A hand-rolled dialect stays small (~50 lines of JS for rendering + ~50 lines of Go for validation), is easy to extend, and carries no dependency.

**Alternatives considered:**
- *Full JSON Schema (Draft-07+)* — overkill; 10× the code for generality we don't use
- *Go struct tags → schema autogen* — tempting for gateway's built-in panels but services in other languages (if we ever have them) would need to replicate the schema manually; the explicit-schema approach is language-neutral

### Decision 6: Form submission flows through existing `PUT /v1/config`

**Choice:** A `form` section declares a `config_key` (e.g. `chat`, `broker`, `memory`). Save POSTs through the existing `PUT /v1/config` gateway endpoint with `{section: config_key, values: <form data>}`. No new config write endpoint.

**Rationale:** We already have a config write path through Valkey-backed `config:*` keys. Services that read `config:chat` at startup continue to do so; dashboard changes surface via existing resolvers and existing restart-to-apply mechanics. This change doesn't touch the mutability model — it just lets services declare which form feeds which existing config key.

**Alternatives considered:**
- *Services expose their own PUT endpoints, dashboard POSTs directly to the service* — requires services to handle HTTP for config writes (most today only read from Valkey); more code
- *WebSocket bidirectional config sync* — overkill

### Decision 7: Default order by registration timestamp, overridable by descriptor

**Choice:** If the descriptor provides an `order` integer, use it. Otherwise the panel orders after all explicit-order panels, by service ID alphabetically. Gateway built-ins use explicit low order values (10, 20, 30...) to stay first.

**Rationale:** Stable, predictable order without requiring every service to set an order value. Gateway built-ins remain first because they're the "shell" panels.

## Risks / Trade-offs

- **Risk:** A malformed descriptor from a misbehaving service could break panel rendering. → **Mitigation:** Validate every descriptor at registration time on the gateway side; reject invalid descriptors with a warning log and omit them from the aggregation response. Dashboard never sees invalid descriptors.
- **Risk:** A service's panel could obscure critical built-in panels by using low `order` values or hostile content. → **Mitigation:** `order` values below a threshold (e.g. < 100) are reserved for gateway; service descriptors are clamped to 100+. `iframe` URLs are rendered with `sandbox` attribute; no JS cross-talk.
- **Risk:** Descriptor evolves and old service registrations become incompatible. → **Mitigation:** Version the descriptor shape via a `version: 1` field; gateway tolerates unknown newer versions by skipping; tolerates missing field (treats as v1).
- **Trade-off:** Migrating built-in panels to the new mechanism ships with this change rather than as a follow-up. That enlarges the change footprint but pins down whether the descriptor is expressive enough.
- **Trade-off:** Two rendering paths (old inline HTML and new descriptor-driven) never coexist — we move all at once. Smaller merge conflict surface at the cost of a slightly larger dashboard JS diff.
- **Trade-off:** We do not address "runtime-tunable" env vars in this change. A service that reads a value only from env vars at startup won't benefit from a dashboard form (the form saves to Valkey, the service never reads Valkey). That's a per-service refactor per service, left for each owning change.

## Migration Plan

This is an additive change with a mechanical refactor in the dashboard. No data migration. Rollout:

1. Land the descriptor types and validation in `internal/dashboard` or similar.
2. Land `GET /v1/dashboard/panels` in the gateway; returns an empty list initially if nothing is registered.
3. Gateway registers descriptors for its five built-in panels at startup.
4. Dashboard shell refactor: HTML reduced to tab bar and empty panel containers. JS fetches `/v1/dashboard/panels` and renders into containers. All existing form behavior preserved via the new rendering path.
5. Smoke the dashboard: every previously-working form continues to work, every previously-shown status continues to show.
6. No consumer-side changes required for this change — follow-up changes (memory panel refresh, logs panel, agents expansion) will add descriptors on memory-service, llm-broker, llm-proxy, retro-agent.

**Rollback:** trivial; revert the change. Prior HTML/JS is in git; no data state.

## Open Questions

- **Hot-swap on new registration.** When a new service registers at runtime, do we want the dashboard to notice without a refresh? Today it won't — refresh re-fetches. Worth considering SSE/WS for v2 if operators complain. Out of scope here.
- **Gateway's own registration.** The gateway hosts the registry consumer; it doesn't publish its own registration to the stream today. For built-in panels, should the gateway write a synthetic registry entry, or maintain a separate "built-in panels" list that merges with registered panels at `GET /v1/dashboard/panels` time? Leaning toward the separate list for code clarity — keeps "services that happen to have a panel" distinct from "the gateway's own dashboard scaffolding." Will resolve in implementation.
- **Panel destruction.** When a service deregisters or dies (no heartbeat), does its panel disappear immediately? Leaning yes — aggregation filters on currently-alive registry entries, so a dead service's panel drops off the next dashboard fetch.
- **Schema extensions for common patterns.** Some services will want a `readonly: true` flag (show current value, no save button) — worth adding now? Lean yes, easy to add.
