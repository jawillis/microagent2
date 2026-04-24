## Context

After `add-dashboard-panel-registry` lands, the Agents panel is gone. Operators currently edit broker settings, retro policy, and MCP servers via the dashboard; that functionality needs to come back — but as per-service contributions, not as a monolithic Agents tab.

Four distinct concerns, each with a clean owner:

```
 Broker panel    ← llm-broker     (slot budgets, timeouts, slot table)
 LLM Proxy panel ← llm-proxy      (timeouts)
 Retro panel     ← retro-agent    (policy + manual triggers)
 MCP panel       ← main-agent     (MCP server CRUD)
```

Each becomes its own tab. The descriptor-driven composition supports this naturally — four new service registrations with panel descriptors.

Two new design problems this change must solve:

1. **Live data from services**: the broker's slot table is in-process state. How does the dashboard get it? The gateway needs to fetch from the broker. Options: new messaging round-trip, or broker exposes HTTP directly.

2. **Action semantics**: "trigger retro" is not config-edit; it's a one-shot RPC. The `form` section kind doesn't fit. An `action` section kind is the natural addition — a button that POSTs a documented body to a URL.

## Goals / Non-Goals

**Goals:**

- Operators regain the functionality lost when the Agents panel was removed
- Each service contributes exactly the panel that describes it; no cross-service coupling
- Live slot table visibility (at least on-tab-open refresh)
- Broker + llm-proxy config becomes runtime-tunable (not requiring container restart)
- Retro manual-trigger button works from the dashboard
- MCP CRUD flows through its owning service's panel

**Non-Goals:**

- SSE-driven live slot table updates (on-demand fetch is enough; operators refresh)
- Full broker observability (we don't add metrics panels here; operators have llm-broker's snapshot logs)
- Runtime-tunable everything (we make broker + llm-proxy config tunable because they're small and operator-facing; other env vars stay env-only)
- Cross-agent MCP coordination (each agent owns its MCPs; main-agent owns the one we expose)

## Decisions

### Decision 1: Broker slot snapshot fetched via new messaging round-trip

**Choice:** Gateway exposes `GET /v1/broker/slots`. Handler publishes a `SlotSnapshotRequest` message to a broker-consumed stream, broker responds with a `SlotSnapshotResponse` on the reply stream. Standard request/reply pattern.

**Rationale:** Consistent with how the gateway already talks to other services — messaging is the substrate. No new HTTP surface on the broker. Latency is trivial (single round-trip within docker-compose network).

**Alternatives considered:**
- *Broker exposes its own HTTP port* — another port to manage, another place for auth/TLS policy, inconsistent with how other gateway→service flows work
- *Broker publishes a snapshot stream and gateway reads the latest entry* — adds persistent state in Valkey for what should be on-demand

### Decision 2: `action` section kind with simple POST semantics

**Choice:** New `action` section kind: `title`, `actions` (array of `{label, url, method, body (object), confirm (string, optional), status_key (string, response field to display)}`). Dashboard renders each action as a button; clicking it (optionally with confirm prompt) POSTs the declared body and displays the declared status field from the response, or an error on non-2xx.

**Rationale:** One-shot RPCs are a distinct UI idiom. Generic enough to cover "trigger retro," "restart MCP server," "force consolidation," etc. Small enough to validate exhaustively. Declarative confirm prompt keeps destructive actions safe.

**Alternatives considered:**
- *Embed action buttons inside `form` sections* — overloads the form's purpose
- *Action descriptor with JS hooks* — defeats the declarative design

### Decision 3: Config migration for broker + llm-proxy is bootstrap-fallback, not cutover

**Choice:** Broker and llm-proxy both read their settings via `config.ResolveBroker(...)` / `config.ResolveLLMProxy(...)` which check Valkey first, then env vars, then hardcoded defaults. If Valkey is empty (fresh deploy), env vars continue to seed behavior exactly as today. Dashboard writes to Valkey; env var is ignored for subsequent reads.

**Rationale:** No operator action required on deploy. Existing docker-compose `.env` stays valid. Dashboard edits take precedence once made. Standard pattern we already use for chat/memory/retro config.

**Alternatives considered:**
- *Require Valkey-only config* — breaks deploys without a bootstrap step
- *Continue env-only* — defeats the purpose of the panel

### Decision 4: Re-reading config on change

**Choice:** Broker and llm-proxy re-resolve config at request time (or on a short timer) rather than caching at startup. Changes from the dashboard take effect within seconds without a restart.

**Rationale:** This is the whole point of runtime-tunable. Startup-only caching would make the form misleading (user saves, nothing changes, user confused).

**Open:** The broker's SlotTable is initialized at startup with a specific slot count; changing `agent_slot_count`/`hindsight_slot_count` at runtime is meaningful only if we rebuild the slot table. Design notes call this out — the slot-count fields become "takes effect on restart" readonly-ish inputs; only the timeout fields are true hot-reload. The form marks slot-count fields with a "restart required" note in their description.

### Decision 5: Descriptor `order` values

**Choice:**
- llm-broker: `order: 300`
- llm-proxy: `order: 310`
- retro-agent: `order: 320`
- main-agent MCP panel: `order: 330`

**Rationale:** All in the 300s so they cluster after Memory (200) and before anything else. Broker first since it's the most frequently tuned.

### Decision 6: Retro "trigger" actions declared in the retro panel, endpoints already exist

**Choice:** Retro's action section declares three actions pointing at the existing `POST /v1/retro/{session}/trigger` endpoint, with the `job_type` varied per button. Each action requires a session ID — the panel renders a session input next to the trigger buttons (OR the action section supports per-action parameters as a minimal input).

**Rationale:** The endpoint exists and works. Reusing it avoids new surface. A pragma: the `action` section kind supports parameterized actions via a small `params` field (array of param definitions). Defined in the schema.

## Risks / Trade-offs

- **Risk:** The new messaging round-trip for slot snapshot has no established pattern in the repo today. → **Mitigation:** pattern is straightforward (correlation_id, reply stream, timeout); follow the existing slot-request/reply shape. Tests cover timeout + normal path.
- **Risk:** Changing `agent_slot_count` at runtime may trick operators into thinking it hot-reloads. → **Mitigation:** field description says "Requires broker restart to apply." Alternatively we rebuild the SlotTable in place, but that has ownership and in-flight-request complications; not worth the risk for a rare operation.
- **Trade-off:** Four services now need dashboard-panel code (llm-broker and llm-proxy add registration from zero). It's ~20 lines each plus a new heartbeat goroutine — small. But it's four services, not one.
- **Trade-off:** `action` section kind expands the descriptor schema after we said the kind enum was closed in `add-dashboard-panel-registry`. That's deliberate — this change was expected to add kinds; the closed enum is closed *per change*, open to extension via future changes.
- **Trade-off:** main-agent's MCP panel duplicates existing hardcoded MCP HTML rather than rewriting it. The old HTML was in the dashboard shell and got removed. We could restore it under main-agent's descriptor using table + action + form sections, but the MCP add-form has fields whose validity depends on other fields (command vs URL), which the current schema doesn't capture well. Either simplify the MCP add-form or accept that MCP editing gets basic through the descriptor system.

## Migration Plan

1. Land `action` section kind in `internal/dashboard` (types, validation, JS renderer).
2. Land broker slot snapshot endpoint + messaging pair.
3. Land broker + llm-proxy config readers with env fallback; confirm existing .env deploys still work.
4. Land panel descriptors on llm-broker, llm-proxy, retro-agent, main-agent (add registration + heartbeat to llm-broker and llm-proxy).
5. Smoke: deploy, open dashboard, see Broker / LLM Proxy / Retro / MCP tabs, edit a broker timeout, see it take effect without restart; trigger a retro job from the Retro panel; see the slot table in the Broker panel match live broker state.

**Rollback:** revert; panels disappear from dashboard (it still renders whatever's left). Services that newly register under this change continue registering but their descriptors are unused — no harm.

## Open Questions

- **MCP add-form**: how much of the existing MCP add UX (command vs URL kind, env vars textarea) translates cleanly into `form` fields? If it doesn't, MCP is reduced to "name + command-line + enabled" and more complex editing moves to direct `PUT /v1/mcp/servers`. Worth resolving during implementation.
- **Parameterized actions**: the retro trigger action needs a session ID. Is that a panel-level shared input, a per-action param, or just a free-text box that each action picks up? Leaning per-action `params: [{name: "session_id", type: "string", required: true}]` in the descriptor schema.
- **Preempt timeout runtime tuning**: changing preempt timeout at runtime while a preemption is in progress could race. Most likely harmless (the timer is read per-call), but worth a code audit during implementation.
