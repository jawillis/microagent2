## Context

Today, llama-server's KV slots are protected by llm-broker's SlotTable. The broker is message-queue-driven: agents publish `SlotRequestPayload` over Valkey streams, broker replies with `SlotAssignedPayload`, and the agent's subsequent `LLMRequestPayload` carries the `slot_id` the broker validates and proxies. This keeps each agent pinned to one slot so llama-server's KV cache stays warm for that agent's prompt prefix.

The memory-system redesign (`docs/memory-system-design.md`) adds Hindsight as the memory substrate. Hindsight makes frequent LLM calls for extraction, consolidation, and synthesis — against the same llama-server. Hindsight speaks OpenAI-compatible HTTP and has no concept of slots. If pointed at llama-server directly, its prompts land in whichever slot llama-server picks and evict warm agent KV caches, costing agent turns a full prompt-ingest on their next call.

The broker cannot host Hindsight directly: it has no HTTP surface, and mixing an OpenAI-compatible HTTP listener into a message-queue-driven service muddies the broker's role. We need a thin HTTP-facing service that terminates Hindsight's OpenAI traffic and coordinates with the broker for slot assignment. That is `cmd/llm-proxy`.

For slot awareness to work, the broker needs to know which requests are Hindsight-class and which are agent-class. A single pool without class distinction leads to the same thrashing — it just happens inside the broker. We extend the SlotTable with slot classes and configure which slot indices belong to which class.

## Goals / Non-Goals

**Goals:**

- Hindsight-class callers (via llm-proxy) never receive agent-class slots, and vice versa
- Hindsight's stable mission prompts (`retain_mission`, `observations_mission`, `reflect_mission`) stay warm in reserved slots across calls
- Agents' existing slot behavior is unchanged — no regressions in slot assignment, preemption, snapshot logging, or request validation
- llm-proxy is OpenAI-compatible enough that Hindsight can point its `llm_provider` at it without special handling on Hindsight's side
- Slot-class budget is configurable at broker startup; total class budgets must not exceed llama-server's configured slot count

**Non-Goals:**

- Hindsight integration itself (arrives in the next proposal)
- memory-service (next proposal)
- Priority signaling from Hindsight for user-facing reflect calls — deferred to the memory-service proposal, which can issue priority-tagged requests directly to llm-proxy
- Non-chat OpenAI endpoints (embeddings, completions legacy) — llm-proxy starts with `/v1/chat/completions` only
- Multi-tenant auth on llm-proxy — single-tenant for this change; an API-key env var is sufficient
- Cross-class preemption — Hindsight-class slots are never preempted by agents and vice versa

## Decisions

### Decision 1: New `cmd/llm-proxy` service rather than extending llm-broker with HTTP

**Choice:** New binary.

**Rationale:** The broker is message-queue-native. Adding an HTTP listener mixes two transport paradigms in one process and widens its responsibility. A thin, purpose-built proxy keeps the broker focused on slot management and gives us a clean surface for any future OpenAI-compat consumer (not just Hindsight).

**Alternatives considered:**
- *Extend broker with HTTP listener* — one fewer service, but muddies boundary and makes broker a public-facing surface
- *Second llama-server instance for Hindsight* — zero coordination, but doubles GPU memory and makes slot budget unfungible
- *Let Hindsight hit llama-server directly* — the status quo the proposal exists to avoid

### Decision 2: Slot classes as a tag on the SlotEntry, configured by slot-index ranges

**Choice:** Each `SlotEntry` carries a `Class` field (`SlotClassAgent` or `SlotClassHindsight`). At broker startup, the first `AGENT_SLOT_COUNT` slots are initialized as agent-class and the next `HINDSIGHT_SLOT_COUNT` slots as hindsight-class. `FindUnassigned` takes a class argument; slot requests carry a class; assignment only considers slots of the matching class.

**Rationale:** Minimal change to the SlotTable data model, zero cross-class interaction, straightforward to reason about. Index-based class assignment is stable (a given slot is always the same class for the life of the process), which matches llama-server's slot identity and keeps KV cache warm per-class.

**Alternatives considered:**
- *Separate SlotTable per class* — cleaner separation but duplicates reclaim/preempt/snapshot logic; more divergence to maintain
- *Dynamic class assignment (any slot can be any class)* — flexibility not needed; fixed ranges match llama-server's static slot identity

### Decision 3: Class defaults to agent when missing from slot/LLM request payloads

**Choice:** `SlotRequestPayload.SlotClass` and `LLMRequestPayload.SlotClass` default to `agent` when absent or empty.

**Rationale:** Backward-compatible with all existing agent traffic. No agent-side changes required for this proposal to ship; agents continue publishing slot requests without a class field and the broker treats them as agent-class.

**Alternatives considered:**
- *Required class field on every request* — forces agent-side changes in lockstep; larger blast radius
- *New separate message types per class* — doubles the messaging surface for no semantic gain

### Decision 4: llm-proxy identifies itself as the `agent_id` for ownership validation

**Choice:** llm-proxy sets `agent_id` on slot requests and LLM requests to a stable identifier per-proxy-process (e.g. `llm-proxy-${instance}`). Slot ownership validation in the broker works unchanged: an LLM request from llm-proxy must reference a slot currently assigned to llm-proxy.

**Rationale:** Reuses existing `Requirement: LLM request slot-ownership validation` logic; llm-proxy is just another slot-owning identity from the broker's perspective, constrained to hindsight-class slots. No broker logic change for ownership semantics — only the class filter is added.

**Alternatives considered:**
- *Per-Hindsight-request identities* — Hindsight doesn't sign its requests, so llm-proxy can't distinguish callers; over-engineering for the current need
- *Skip ownership validation for hindsight-class* — weakens invariants without cause

### Decision 5: llm-proxy holds one slot per active connection, released at connection close

**Choice:** llm-proxy requests a slot when an HTTP request arrives, holds it for the duration of the streaming response, and releases it once the response completes (or the client disconnects). Idle llm-proxy holds no slots.

**Rationale:** Hindsight's traffic is bursty — it does not make overlapping requests for the same mission at the same priority. One active slot per in-flight request is the simplest model and matches how agents use slots today. If contention shows up in practice (Hindsight queues internal work behind one llm-proxy slot), we can revisit with a slot pool; simpler first.

**Alternatives considered:**
- *llm-proxy holds all hindsight-class slots permanently* — wastes slots when Hindsight is idle; complicates reclaim semantics
- *Per-mission pinned slot* — requires llm-proxy to classify incoming requests by mission, which it can't reliably do from the OpenAI payload

### Decision 6: No preemption across classes

**Choice:** When an agent-class slot request arrives and all agent slots are occupied, existing preempt logic applies — but only against agent-class slots. Hindsight-class slots are never considered. Same for hindsight-class requests.

**Rationale:** The whole point of classes is isolation. Cross-class preemption would reintroduce the exact eviction problem this change exists to prevent.

## Risks / Trade-offs

- **Risk:** A misconfigured slot budget (`AGENT_SLOT_COUNT + HINDSIGHT_SLOT_COUNT > llama_server_slots`) causes undefined behavior (requests referring to non-existent slots). → **Mitigation:** Broker validates at startup; refuses to start if total exceeds configured `slot_count`, logs a clear error.
- **Risk:** Hindsight-class slot exhaustion (llm-proxy holds all hindsight slots; a new request queues). → **Mitigation:** Existing broker `assignFromQueue` logic applies within the class — the request waits in the class queue until a slot frees. Tune `HINDSIGHT_SLOT_COUNT` if observed queueing is problematic.
- **Risk:** llm-proxy becomes a single point of failure for Hindsight. → **Mitigation:** Stateless service, fast startup, docker-compose restart policy. If llm-proxy is down, Hindsight calls fail — memory-service (future) surfaces this via its health endpoint.
- **Trade-off:** An extra network hop for Hindsight traffic (Hindsight → llm-proxy → broker → llama-server, vs. Hindsight → llama-server). The hop is negligible against llama-server generation latency, and buys us KV cache isolation.
- **Trade-off:** Static slot-class budget is set at broker startup. Rebalancing requires a broker restart. Acceptable because slot counts rarely change and llama-server itself requires a restart to change its slot count.

## Migration Plan

No user-facing behavior change in this proposal. Deployment steps:

1. Add `cmd/llm-proxy` to docker-compose with `LLAMA_SERVER_ADDR`, `VALKEY_ADDR`, `LLM_PROXY_HTTP_ADDR`, and a stable agent identity env var.
2. Restart broker with `AGENT_SLOT_COUNT` + `HINDSIGHT_SLOT_COUNT` summing to the existing `slot_count`. Existing agents continue working (default class = agent).
3. Smoke-test llm-proxy with a direct OpenAI-compat HTTP client (curl against `/v1/chat/completions`) — verify the request rides a hindsight-class slot and llama-server responds.
4. No rollback machinery needed: llm-proxy is additive. If it misbehaves, stop the container; existing agent traffic is unaffected because agents never touched the hindsight-class slots.

## Open Questions

- **llm-proxy instance identity.** Should llm-proxy's broker-identity be configurable via env var, or derived from container hostname? Container hostname is simpler; env var is more explicit. Leaning env var for clarity in logs.
- **Preempt timeout for hindsight-class.** Hindsight requests can be longer than agent turns (large consolidation). Agent preempt timeout is 5000ms. Does hindsight-class need its own preempt timeout env var? Probably yes; defer decision to implementation if queuing proves benign initially.
- **OpenAI compatibility depth.** llm-proxy needs enough compat for Hindsight specifically. Start with `POST /v1/chat/completions` (streaming + non-streaming). If Hindsight uses other endpoints (e.g. `/v1/models` for provider discovery), add minimal stubs as discovered during Proposal 2's integration. Listed as out of scope above.
