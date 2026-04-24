## Why

The `add-dashboard-panel-registry` change removed the hardcoded Memory panel from the dashboard shell and left a documented gap: muninn-era fields (recall_threshold, max_hops, vault, store_confidence) no longer map to Hindsight. The memory panel needs to return, but re-introduced on the new declarative-descriptor footing, contributed by memory-service rather than hardcoded in the gateway.

The panel should reflect the split between Hindsight (which owns bank config, missions, directives, memory browse) and memory-service (which owns provenance conventions, recall defaults, tag taxonomy, and the HTTP surface). Users shouldn't need to leave the microagent2 dashboard to edit either — Hindsight's Control Plane is embedded as an iframe, microagent2-specific configuration is a form directly above it.

## What Changes

- memory-service registers a `dashboard_panel` descriptor at startup
- Panel has two sections:
  - **form** — microagent2-specific memory config: recall limit, prewarm limit, recall types default (enum: `observation` / `world + experience` / both), default provenance on retain, tag taxonomy (comma-separated list)
  - **iframe** — Hindsight Control Plane at `${HINDSIGHT_CP_URL}` (default `http://localhost:9999`). Covers bank config, missions, directives, memory browse, webhook deliveries, consolidation activity — everything Hindsight already exposes and we don't need to duplicate.
- The form's `config_key` is `memory`; `PUT /v1/config` with section `memory` writes to Valkey. memory-service reads these values at startup and on change.
- A new `MEMORY_SERVICE_CP_URL` env var on memory-service holds the externally-reachable Hindsight CP URL (the URL the operator's browser uses, not the in-network URL). Default: `http://localhost:9999`. Descriptor interpolates this into the iframe section at registration time.
- Old Valkey `config:memory` keys (vault, max_hops, store_confidence, recall_threshold) are deprecated; new keys added for the new form fields. Deprecated keys are tolerated but unused.

## Capabilities

### New Capabilities

- None.

### Modified Capabilities

- `memory-service`: registers a dashboard panel descriptor; reads a new set of recall-default and taxonomy config keys from Valkey; exposes them through the existing HTTP API handlers.
- `dashboard-ui`: the Memory panel reappears in the dashboard, now contributed by memory-service rather than hardcoded. User-visible behavior: Memory tab is back, with a small form on top and Hindsight's CP embedded below.

## Impact

- memory-service: new startup code to construct and emit the descriptor as part of its registration. Today memory-service does NOT register itself on `stream:registry:announce`; that registration call needs to be added as part of this change. Registration includes at minimum: `service_id: "memory-service"`, `capabilities: ["memory"]`, `heartbeat_interval_ms`, and the panel descriptor.
- New env var on memory-service: `MEMORY_SERVICE_CP_URL` (external Hindsight CP URL for the iframe).
- New Valkey config keys under `config:memory`:
  - `recall_limit` (existing, kept)
  - `prewarm_limit` (existing, kept)
  - `recall_default_types` (new; enum of `observation` / `world_experience` / `all`)
  - `default_provenance` (new; one of `explicit`/`implicit`/`inferred`/`researched`, default `explicit`)
  - `tag_taxonomy` (new; comma-separated list, default `identity,preferences,technical,home,ephemera`)
- Deprecated Valkey keys (no-op if present): `vault`, `max_hops`, `store_confidence`, `recall_threshold`.
- memory-service's `/recall` handler reads `recall_default_types` for the types default (replacing the hardcoded `["observation"]` default in current code).
- memory-service's `/retain` handler reads `default_provenance` for provenance defaulting (replacing the hardcoded `"explicit"` default).
- memory-service gains a heartbeat goroutine (it currently does not heartbeat; registration without heartbeat would drop out of the registry).
- No changes to Hindsight itself; we only embed its existing UI.
- Docker-compose: expose `HINDSIGHT_CP_PORT` (already done in previous change) and pass `MEMORY_SERVICE_CP_URL=http://localhost:${HINDSIGHT_CP_PORT}` to memory-service so the descriptor's iframe URL is reachable from the operator's browser.
- Tests: memory-service panel descriptor construction unit test; registration roundtrip with the descriptor; `/recall` reading `recall_default_types`; `/retain` reading `default_provenance`.
- One cleanup opportunity: the proposal's mentions of `vault`/`max_hops` removal propagate to `internal/config/config.go` `MemoryConfig` / `ResolveMemory` — those fields become dead but are kept with deprecation comments to avoid breaking any unknown readers.
