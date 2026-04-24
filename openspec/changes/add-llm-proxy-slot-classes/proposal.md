## Why

The memory-system redesign in `docs/memory-system-design.md` adopts Hindsight as the memory substrate. Hindsight makes frequent LLM calls (extraction, consolidation, synthesis) against the same llama-server our agents use. If Hindsight talks to llama-server directly, its unrelated prompts land in whichever llama slot llama-server picks and evict a hot agent KV cache. Agent turns then pay a full prompt-ingest cost on the next call.

The llm-broker already owns a SlotTable that keeps each agent pinned to a slot to preserve KV cache locality, but it is message-queue-driven — it has no HTTP surface for a third-party OpenAI-compatible client like Hindsight. This proposal stands up that surface and teaches the broker about slot classes so Hindsight traffic and agent traffic can share a GPU without thrashing each other.

This change is a prerequisite for the Hindsight cutover (next proposal). Shipping it first means we never run the thrashing state.

## What Changes

- New `cmd/llm-proxy` service — OpenAI-compatible HTTP front for llama-server, forwards requests over messaging to llm-broker for slot-aware routing
- Extend `llm-broker` SlotTable with **slot classes**: `agent` (preemptable, priority-driven) and `hindsight` (pinned, reserved) — agents are never assigned hindsight-class slots; llm-proxy callers are never assigned agent-class slots
- New messaging contract between llm-proxy and llm-broker: proxy-originated slot requests carry a class identifier; slot assignment honors the class
- Slot-class budget configurable via broker env vars (e.g. `AGENT_SLOT_COUNT`, `HINDSIGHT_SLOT_COUNT`) — total must not exceed llama-server's configured slot count
- Hindsight (not in this change; arrives with Proposal 2) will configure its OpenAI LLM provider to point at llm-proxy rather than llama-server

## Capabilities

### New Capabilities

- `llm-proxy`: OpenAI-compatible HTTP front that routes external LLM requests through llm-broker's slot-class-aware assignment to llama-server, preserving KV cache locality for both agent and non-agent consumers

### Modified Capabilities

- `llm-broker`: SlotTable gains slot classes; slot requests and assignments include a class identifier; class determines which slot indices are eligible; class budget is configurable

## Impact

- New binary: `cmd/llm-proxy`, deployed in `docker-compose.yml` alongside existing services
- `internal/broker/slots.go` — `SlotEntry`, `SlotTable`, `FindUnassigned`, and related methods gain a class dimension
- `internal/broker/broker.go` — slot-request handling reads a class field; slot-ownership validation in `ProxyLLMRequest` unchanged (owned-by-agent semantics still apply within the agent class; llm-proxy will have its own caller identity)
- `internal/messaging/payloads.go` — `SlotRequest`, `SlotAssigned`, `LLMRequest`, and related messages gain a `slot_class` field (backward-compatible: missing field defaults to `agent`)
- Broker configuration: new env vars for per-class slot budgets
- No changes to existing agents — they continue to request slots without specifying a class (defaulted to `agent`)
- No changes to Hindsight or memory-service (both arrive in Proposal 2)
- Test surface: broker slot-class assignment tests, llm-proxy HTTP→messaging→llama-server end-to-end test
