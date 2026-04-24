## Context

After `add-dashboard-panel-registry` lands, the dashboard composes panels from service-contributed descriptors. This change fills in the memory panel that was explicitly removed during the registry change's shell refactor.

The memory panel has an unusual split: Hindsight owns most of the interesting surface (bank config, missions, directives, browse, stats, consolidation activity) and provides a Control Plane UI at port 9999 that already handles all of that. microagent2 layers conventions on top (provenance metadata, tag taxonomy, recall-type defaults, `/retain` and `/recall` policy) that are not Hindsight-aware and must be configured on our side.

Duplicating Hindsight's CP in our dashboard would be a lot of UI work that drifts as Hindsight evolves. The pragmatic answer is iframe embedding: our panel gives the form for microagent2-specific fields, then embeds Hindsight's CP below it. Two UIs, one browser tab, no duplication.

## Goals / Non-Goals

**Goals:**

- Memory tab returns in the dashboard, contributed by memory-service
- microagent2-specific memory configuration (recall defaults, default provenance, tag taxonomy) is editable through a simple form
- Hindsight's Control Plane is reachable from inside the dashboard without leaving it
- memory-service appears in the agent registry so its descriptor flows through the aggregation endpoint
- Deprecated muninn-era config fields no longer appear anywhere a user sees them

**Non-Goals:**

- Replicating Hindsight's bank/mission/directive/browse UI natively (iframe handles it)
- Per-retain editing or memory browse in our dashboard (iframe handles it)
- Making recall defaults tunable per-call from the dashboard (dashboard sets the defaults; per-call overrides are API concerns)
- Custom microagent2-side mission editing (missions live in `deploy/memory/*.yaml` and sync to Hindsight; the CP can override at Hindsight layer)
- Auth on the embedded iframe (CP is unauthenticated today; dashboard is single-tenant admin)

## Decisions

### Decision 1: Iframe URL is passed via env to memory-service

**Choice:** memory-service reads `MEMORY_SERVICE_CP_URL` at startup and bakes it into the iframe section URL of its panel descriptor before registering.

**Rationale:** The URL an operator's browser can reach is different from the URL memory-service can reach (external hostname + port-mapping vs docker-network internal hostname). The descriptor has to carry the external URL for the iframe to work. Env var keeps it config-driven and out of code.

**Alternatives considered:**
- *Descriptor declares `${env.HINDSIGHT_CP_URL}` and dashboard resolves* — pushes env reading into the dashboard, which is the wrong layer
- *Dashboard knows Hindsight's CP URL directly* — reverts the whole "services own their panel" design

### Decision 2: memory-service starts registering and heartbeating

**Choice:** memory-service gains registration on `stream:registry:announce` and a heartbeat goroutine, matching the pattern used by main-agent / retro-agent. Registration carries the panel descriptor.

**Rationale:** Panel aggregation filters to alive services. Without heartbeat, memory-service's panel would never appear. This is also long-overdue plumbing — memory-service is a first-class service and should show up in `GET /v1/status`.

**Alternatives considered:**
- *Register without heartbeat, add some "infra services skip liveness" bypass* — introduces an untidy class of services; bad architectural move
- *Continue with memory-service unregistered, special-case its panel* — defeats the registry design

### Decision 3: Keep deprecated config keys tolerated, not removed

**Choice:** `vault`, `max_hops`, `store_confidence`, `recall_threshold` stay in the `MemoryConfig` struct with `// deprecated` comments. Reads on these keys don't error; writes through the new form don't set them. A future cleanup change can delete them.

**Rationale:** Some operator's live Valkey may still have values under these keys. Silently tolerating them avoids a hard-break migration; the real cleanup is that nothing USES them anymore.

**Alternatives considered:**
- *Delete the fields in this change* — forces anyone with existing config to manually clean up or risk unknown-field errors on next deploy (Go's JSON decode tolerates unknown fields anyway, so risk is minimal — worth reconsidering)

### Decision 4: Panel has exactly two sections: form, then iframe

**Choice:** Descriptor declares `sections: [form, iframe]` in that order. Form is small (5-6 fields). Iframe takes the bulk of the vertical space with `height: "800px"` or similar.

**Rationale:** Clear visual hierarchy — the microagent2-specific settings are compact at top, Hindsight's deep functionality expands below. No accordion / collapsible sections; the form is small enough to always-show.

**Alternatives considered:**
- *Iframe-only panel, no form* — then microagent2-specific knobs have no home
- *Two separate tabs* — splits a cohesive concept; worse UX

### Decision 5: Recall types enum surfaces three choices, not raw Hindsight types

**Choice:** The recall types form field is an enum with three values: `observation` (the current default), `world_experience` (raw facts), `all` (both). memory-service translates these to Hindsight's `types` list on each recall (observation → `["observation"]`; world_experience → `["world","experience"]`; all → `["observation","world","experience"]`).

**Rationale:** Raw Hindsight types leak implementation detail; the operator-facing choice is really "do I want synthesized knowledge, raw facts, or both?" Translation at the memory-service boundary keeps the config semantic.

**Alternatives considered:**
- *Multi-select of raw types* — more flexible, worse UX, forces operators to learn Hindsight's vocabulary
- *Free-form CSV* — too loose; easy to misconfigure

## Risks / Trade-offs

- **Risk:** Hindsight CP iframe fails to load due to `X-Frame-Options: DENY` or `Content-Security-Policy: frame-ancestors`. → **Mitigation:** verify during implementation; if Hindsight sends those headers, the iframe won't work. Fallbacks: link out instead, or configure Hindsight to allow our origin. The `sandbox="allow-same-origin"` on our side plus matching origin handling is still subject to Hindsight's CSP.
- **Risk:** Operator changes `default_provenance` via the form; in-flight retains use the old default until memory-service sees the change. → **Mitigation:** memory-service reads the config via the same `ResolveMemory` path at request time rather than caching at startup. The value is already backed by Valkey; reading on demand is cheap.
- **Risk:** `MEMORY_SERVICE_CP_URL` is misconfigured (wrong port, wrong host). → **Mitigation:** iframe just shows a broken page; operator sees it immediately and fixes env. No silent failure.
- **Trade-off:** Two configuration loci for memory: our form (microagent2 conventions) and Hindsight's CP (bank config, missions). Cognitive load on the operator, but matches reality — the two layers do different things.
- **Trade-off:** We don't surface Hindsight stats (memory count, observation count) directly in the microagent2 panel. They're one iframe-click away. Adding status sections that proxy to Hindsight's stats endpoints is a future enhancement.

## Migration Plan

1. memory-service gains registration + heartbeat + descriptor construction.
2. `MEMORY_SERVICE_CP_URL` added to docker-compose memory-service env.
3. Form-driven config keys added to `MemoryConfig` / `ResolveMemory` / `DefaultMemoryConfig`.
4. `/recall` reads `recall_default_types`; `/retain` reads `default_provenance`; both fall back to hardcoded defaults if Valkey is empty.
5. On deploy, dashboard Memory tab reappears; existing behavior preserved (defaults match current hardcoded values).
6. Operators with old `vault` / `max_hops` keys in Valkey: no action needed; values ignored.

**Rollback:** revert; memory-service stops registering and the panel vanishes again. No data state.

## Open Questions

- Should the panel's iframe have a "open in new tab" link alongside it for operators who want Hindsight CP fullscreen? Easy to add, small polish. Lean yes.
- Should the form include a `memory_bank_id` readonly display (show which bank is active)? Useful for multi-bank futures; harmless today. Lean yes as a readonly string field.
- Does Hindsight's CP actually render inside a sandboxed iframe from a different origin? Needs a spike during implementation to verify. If it refuses, plan B is link-out only.
