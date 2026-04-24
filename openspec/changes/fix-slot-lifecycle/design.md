## Context

The broker (`internal/broker/broker.go`) manages a fixed-size `SlotTable` of LLM concurrency slots. Agents acquire a slot via a request/reply over Valkey streams before issuing an LLM request, and release it when the request completes. Two design gaps produce the observed "turn 2 hangs":

1. **Commit-before-ack**: `handleSlotRequest` (`broker.go:117-124`) and `assignFromQueue` (`broker.go:186-219`) both call `SlotTable.Assign` first and then publish the reply with `_, _ = b.client.Publish(...)`. If the publish drops, or the reply arrives after the requester's 30s `WaitForReply` (`runtime.go:55`) has given up, the `SlotTable` stays assigned to an agent that never records the assignment in `Runtime.slotID`. Because `Runtime.slotID` is the only way the agent knows which slot to release, nothing ever clears the entry. The heartbeat-based cleanup in `handleDeadAgent` (`broker.go:286`) only fires for agents that stop sending heartbeats — a live, healthy agent that merely lost a single assignment reply is not dead and is not cleaned up.

2. **Pinned slot 0**: `broker.go:56-57` calls `PinSlot(0, "main-agent", 0)` at startup. Main-agent has no corresponding knowledge of slot 0 — its `Runtime.slotID` starts at `-1` and every `RequestSlot` call returns a different slot. The pinned entry is never released and `SlotTable.FindUnassigned` treats slot 0 as occupied forever. The system advertises `SLOT_COUNT=4` but operates with 3, so leaks in slots 1/2/3 exhaust the pool faster.

Live evidence captured from the running system: `stream:agent:main-agent:requests` delivered 24 messages, `stream:broker:llm-requests` saw only 22. Two turns reached main-agent, failed to get a slot, and were silently ACK'd with no trace beyond the broker's "slot request queued" log. The gateway does not log per-request events at all, so the user's only signal was a client that hung forever.

Primary stakeholders: agents (main-agent, retro-agent), the broker, and anyone debugging through `docker compose logs`.

## Goals / Non-Goals

**Goals:**
- No slot can remain assigned in the `SlotTable` without a live agent that knows it owns that slot.
- Slot 0 is allocated dynamically like every other slot; `SLOT_COUNT=N` means N usable slots.
- Every turn is traceable end-to-end through the logs via a single `correlationID`.
- A leaked slot — if one ever occurs — is visible in the broker logs within 30 seconds.
- Failure modes of the new protocol (dropped ack, slow requester) degrade gracefully: the slot returns to the pool, the requester sees a normal timeout error, and the user can retry.

**Non-Goals:**
- Persisting the `SlotTable` across broker restarts. It remains in-memory; restart is the documented recovery for a corrupted state (and is required once after this rollout to clear existing leaks).
- Replacing the heartbeat-based dead-agent cleanup. It stays as defense-in-depth.
- Giving the `muninn` HTTP client a request timeout (`internal/context/muninn.go:34`). Separate follow-up proposal — same failure shape (hang), different subsystem.
- Instrumenting the LLM broker's request-to-llama-server path beyond what is needed to correlate a slot assignment with the eventual LLM call. That path is not implicated in the current bug.

## Decisions

### 1. Two-phase slot assignment with requester ack

**Decision:** The broker transitions a slot through `Unassigned → Provisional → Assigned`. `FindUnassigned` treats `Provisional` slots as occupied. The broker publishes the reply, then waits for a `SlotAssignedAck` message on its own `stream:broker:slot-requests` stream. On ack, the slot is promoted to `Assigned`. If the ack does not arrive within `provisionalTimeout` (default 2s), a background reclaim goroutine reverts the slot to `Unassigned` and triggers `assignFromQueue`.

**Rationale:** Solves the root cause directly — a slot cannot stay pinned to a requester that never saw the reply. 2s is comfortably longer than the expected one-hop round trip (sub-millisecond locally) but short enough that a genuine hang is not masked.

**Alternatives considered:**
- *(b) TTL with agent heartbeat refresh*: Would work but duplicates the existing `handleDeadAgent` path and adds a distributed timer per slot. More moving parts, no additional safety over (a) + (c) below.
- *Broker-side retry of the reply publish*: Doesn't help when the requester has already given up on `WaitForReply`.
- *Make the assignment message idempotent and let the agent call `ReleaseSlot` aggressively*: This is the defensive (c) below, but on its own it still leaves a window where the broker commits and the agent never gets the message. (a) + (c) cover both sides of the failure.

### 2. Defensive release on requester timeout

**Decision:** In `Runtime.RequestSlot`, wrap `WaitForReply` so that if it returns `ErrTimeout` (or any error), the agent publishes a best-effort `SlotRelease` with `AgentID: r.agentID` and `SlotID: -1` ("whatever you assigned to me"). The broker's `handleSlotRelease` grows a path for `SlotID == -1` that releases any slot currently attributed to `AgentID`.

**Rationale:** Covers the case where the ack would have succeeded but the requester is already in a bad state (e.g., its context was cancelled mid-wait). Belt and suspenders on top of (1). Also fixes leaks from earlier code paths if any are still reachable.

**Alternatives considered:**
- *Rely only on (1)*: Leaves a window where the broker committed the provisional-to-assigned transition right as the requester timed out. (1)'s reclaim only fires on missing ack, not on late ack.

### 3. Remove the slot 0 pin

**Decision:** Delete `broker.go:56-57`. If `SlotTable.PinSlot` has no other callers after removal, delete it too. Main-agent uses the identical lifecycle as retro-agent.

**Rationale:** The pin was a holdover from an earlier design; it serves no current purpose and actively reduces capacity. Removing it makes the broker's contract uniform across agents. Main-agent's priority-based preemption still gives it the strong scheduling guarantee it needs.

**Alternatives considered:**
- *Keep the pin but teach main-agent about it*: Requires main-agent to track a second `slotID`, special-case its first request, and never release slot 0. More code, no real benefit over removal.

### 4. Structured per-request logging with correlationID

**Decision:** Every service logs at INFO on every state transition for a turn, tagged with `correlation_id`:
- **Gateway (`responses.go`, `server.go`)**: request_received, publish_submitted, subscribe_ok, first_token, completed, timed_out.
- **Context-manager (`manager.go`)**: request_decoded, muninn_recall_done (with elapsed_ms), history_loaded (with count), publish_to_agent.
- **Main-agent (`cmd/main-agent/main.go`)**: message_received, slot_requested, slot_acquired (with elapsed_ms), llm_published, execute_done (with elapsed_ms), reply_published, slot_released.
- **Broker (`broker.go`)**: every `sendSlotAssigned` / `assignFromQueue` publish outcome (ok/err), every `SlotTable.Assign` return value, every provisional reclaim.

Broker additionally runs a 30-second ticker that logs `slot_table_snapshot` at INFO: an array of `{slot, agent, priority, state, age_s}` entries. Always on; cheap.

**Rationale:** The only reason the existing leak was diagnosable was post-hoc stream inspection via `valkey-cli`. A single `correlation_id | xargs` pipeline should show the whole life of a request. The periodic snapshot makes leaks visible within 30s rather than after user-visible hangs accumulate.

**Alternatives considered:**
- *DEBUG-level logs gated by a flag*: Defeats the purpose — the logs are needed when a user is complaining, not when they're pre-enabled.
- *HTTP endpoint for slot dump instead of periodic log*: Doesn't show up in `docker compose logs`, requires extra tooling. A periodic log is simpler and achieves the same visibility.

### 5. Message schema for the new ack

**Decision:** Add `messaging.TypeSlotAssignedAck` with payload `SlotAssignedAckPayload{AgentID, SlotID, CorrelationID}`. Published to `stream:broker:slot-requests` so the broker's existing consumer handles it.

**Rationale:** Reuses the existing broker-inbound stream and consumer group. No new stream, no new goroutine. Correlation ID matches the original slot request so the broker can locate the provisional entry without a global lock search.

## Risks / Trade-offs

- [Ack publish fails silently] → The 2s reclaim timer in (1) reverts the slot. Requester already saw a `WaitForReply` success and will try to use a slot the broker no longer owns. *Mitigation:* The requester's next LLM request goes to the broker with `SlotID`; the broker should validate that the slot is currently assigned to the caller before proxying, and return an error if not. This is a small additional check in `handleLLMRequest` (`broker.go:249`).
- [Provisional timeout too short under load] → Spurious reclaims. *Mitigation:* 2s is very comfortable for local Redis. Make it configurable (`PROVISIONAL_TIMEOUT_MS` env, default 2000).
- [Log volume] → Disks fill under sustained load. *Mitigation:* JSON logs at INFO, one line per hand-off, ~10 lines per turn. Typical log-rotation handles this. No request bodies or message contents are logged — only metadata.
- [Slot-table dump at 30s is too slow to catch rapid leaks] → Ops might want faster feedback when debugging. *Mitigation:* Cadence configurable via `SLOT_SNAPSHOT_INTERVAL_S` (default 30). Also, the per-transition logs already capture every assign/release; the snapshot is a safety net, not the primary signal.
- [Operator forgets to restart broker after rollout] → Existing leaks persist until something triggers `handleDeadAgent`. *Mitigation:* Documented in this design and in proposal Impact. A one-line `docker compose restart llm-broker` is the procedure.
- [Backward compatibility with agents that don't send `SlotAssignedAck`] → The broker would reclaim every slot after 2s. *Mitigation:* This is an internal protocol and all in-tree agents are updated in the same change. There is no externally-deployed agent.

## Migration Plan

1. Merge this change.
2. Build new images for broker, main-agent, retro-agent, gateway, context-manager.
3. `docker compose up -d --build` — this restarts broker and clears the leaked `SlotTable`.
4. Verify in broker logs that the first `slot_table_snapshot` after startup shows all 4 slots as `Unassigned` (no pinned slot 0).
5. Send two turns through `/v1/responses`; verify `correlation_id` can be grepped across all 5 services.

Rollback: revert the merge and redeploy. No data migration either direction.

## Open Questions

- Should the 30s slot-table snapshot log only when state changed since the last snapshot, or unconditionally? Unconditional is simpler and serves as a heartbeat that the broker is alive. Leaning unconditional; revisit if log volume complaints come in.
- Should `handleLLMRequest` reject an LLM request whose `SlotID` does not match a currently-assigned slot for that `AgentID` (see the first risk above)? Strong lean yes — it closes the last window where a reclaimed slot could be double-used. Will include in tasks.
