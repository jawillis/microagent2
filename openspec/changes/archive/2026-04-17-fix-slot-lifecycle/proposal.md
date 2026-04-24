## Why

The broker leaks slots when a requester's `WaitForReply` times out: the `SlotTable` commits the assignment before the reply is delivered, and if the reply is lost or late, the agent never records the `slotID` and never releases it. Over time the `SlotTable` fills with ghost assignments, and the next slot request queues forever — surfacing to users as "turn 2 hangs" on `/v1/responses` with no request ever reaching the LLM. A live system currently shows 24 context-assembled messages delivered to main-agent but only 22 LLM requests reaching the broker, with two slot requests queued and never served. The existing heartbeat-based dead-agent cleanup does not fire for a live agent that is merely unable to acquire a slot, so the leak is permanent until broker restart. On top of this, slot 0 is hardcoded to main-agent at broker startup but main-agent has no knowledge of that ownership — it always calls `RequestSlot` and gets a different slot, so effective capacity is 3 instead of 4 and leaks bite faster.

## What Changes

- **BREAKING (internal protocol)**: Slot assignment becomes a two-phase protocol. Broker assigns provisionally and commits only after the requesting agent acknowledges receipt. If the ack does not arrive within a short window, the provisional assignment is reverted and the slot returns to the pool.
- New message type `SlotAssignedAck` published by the agent on the broker's slot-requests stream after `WaitForReply` succeeds.
- Agent runtime: on `WaitForReply` timeout or any error between receiving a slot and storing it in `Runtime.slotID`, publish a best-effort `SlotRelease` identified by `agentID` so the broker can reconcile.
- **BREAKING (startup behavior)**: Remove the hardcoded `PinSlot(0, "main-agent", 0)` at broker startup (`broker.go:56-57`). Main-agent uses the same `RequestSlot`/`ReleaseSlot` lifecycle as every other agent. Effective slot count becomes 4 (matching `SLOT_COUNT`).
- Remove `SlotTable.PinSlot` if no remaining callers.
- Structured per-request logging added across gateway, context-manager, main-agent, and broker — every hand-off logs a `correlationID` so a turn can be traced end-to-end in `docker compose logs`.
- Broker: log every reply-publish outcome in `sendSlotAssigned` and `assignFromQueue`; log `SlotTable.Assign` return value to detect collisions.
- Broker: add a periodic (30s) slot-table dump at INFO level showing ownership + age per slot, so leaks are visible before they fill the table.
- Operator note: deploying this code does not heal an already-leaked `SlotTable`; the broker must be restarted once to reset in-memory slot state.

## Capabilities

### New Capabilities
- None.

### Modified Capabilities
- `llm-broker`: ack-confirmed slot assignment; slot 0 no longer pinned; slot-table introspection logging.
- `agent-runtime`: defensive `SlotRelease` on slot-request timeout; structured request/slot logging.
- `gateway-api`: per-request structured logging with `correlationID`.
- `context-management`: per-request structured logging with `correlationID`.
- `inter-service-messaging`: new `SlotAssignedAck` message type.

## Impact

- **Broker slot protocol**: Two-phase commit. Adds one round-trip (local Valkey pub+read) to every slot assignment; negligible next to LLM inference latency. Broker state machine for a slot gains a `Provisional` state with a short timeout (default 2s).
- **Agent runtime**: `RequestSlot` now publishes an ack after `WaitForReply` and before returning. Failure to publish the ack is logged; broker will revert the provisional assignment and the caller sees `ErrTimeout` as before.
- **Operator action required on rollout**: Restart the broker once after deploying to clear leaked slots in the in-memory `SlotTable`. No data migration — `SlotTable` is not persisted.
- **Log volume**: increases meaningfully. All new logs are structured JSON with a `correlationID` field so they stay greppable and do not dominate disks under normal load.
- **Non-goals** (tracked separately):
  - Muninn HTTP client lacks a request timeout (`internal/context/muninn.go:34`). Related hang vector, distinct subsystem — follow-up proposal.
  - Heartbeat-based dead-agent cleanup (`broker.go:286`) is retained as defense-in-depth; this change addresses the live-agent leak that that backstop cannot catch.
