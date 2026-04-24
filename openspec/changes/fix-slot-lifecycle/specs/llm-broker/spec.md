## ADDED Requirements

### Requirement: Two-phase slot assignment with requester ack
The broker SHALL treat slot assignment as a two-phase protocol. A slot transitions `Unassigned → Provisional → Assigned`. The broker SHALL commit the assignment (transition to `Assigned`) only after the requesting agent has published a `SlotAssignedAck` message matching the original slot request's correlation ID. A slot in `Provisional` state SHALL be treated as occupied by the slot-finding logic.

#### Scenario: Provisional assignment on slot request
- **WHEN** the broker receives a slot request and finds an unassigned slot
- **THEN** it SHALL mark the slot `Provisional` with the requester's agent ID, publish the slot-assigned reply, and start a reclaim timer

#### Scenario: Assignment committed on ack
- **WHEN** the broker receives a `SlotAssignedAck` with a correlation ID matching a `Provisional` slot
- **THEN** it SHALL transition the slot to `Assigned` and cancel the reclaim timer

#### Scenario: Assignment reclaimed on ack timeout
- **WHEN** the reclaim timer fires before a matching `SlotAssignedAck` arrives
- **THEN** the broker SHALL return the slot to `Unassigned`, log the reclaim, and trigger `assignFromQueue`

#### Scenario: Configurable provisional timeout
- **WHEN** the broker starts
- **THEN** it SHALL read `provisional_timeout_ms` from environment (`PROVISIONAL_TIMEOUT_MS`, default 2000) and use that value as the reclaim window

### Requirement: Defensive release by agent ID
The broker SHALL accept a `SlotRelease` message with `slot_id == -1` as a request to release any slot currently attributed to the specified `agent_id`, supporting best-effort cleanup by agents that timed out waiting for a slot assignment.

#### Scenario: Release by agent ID releases one slot
- **WHEN** the broker receives a `SlotRelease` with `slot_id == -1` and an `agent_id` that currently owns a slot
- **THEN** the broker SHALL release that slot and log the release

#### Scenario: Release by agent ID with no owned slot is a no-op
- **WHEN** the broker receives a `SlotRelease` with `slot_id == -1` and an `agent_id` that does not currently own any slot
- **THEN** the broker SHALL log and ignore the release

### Requirement: LLM request slot-ownership validation
The broker SHALL validate that the `slot_id` in an LLM request message corresponds to a slot currently `Assigned` to the requesting `agent_id` before proxying the request to the llama-server.

#### Scenario: Request with owned slot proceeds
- **WHEN** an LLM request arrives with a `slot_id` that the broker has in `Assigned` state owned by the sending `agent_id`
- **THEN** the broker SHALL proxy the request to llama-server as normal

#### Scenario: Request with unowned slot rejected
- **WHEN** an LLM request arrives with a `slot_id` that is not `Assigned` to the sending `agent_id` (unassigned, provisional, or owned by another agent)
- **THEN** the broker SHALL NOT proxy the request, SHALL publish a done token with an error payload to the reply stream, and SHALL log the rejection

### Requirement: Periodic slot-table snapshot logging
The broker SHALL emit a structured INFO log line every `slot_snapshot_interval_s` (default 30) containing the current state of every slot.

#### Scenario: Snapshot emitted on ticker
- **WHEN** the snapshot ticker fires
- **THEN** the broker SHALL log at INFO with `msg: "slot_table_snapshot"` and a `slots` array where each entry has `{slot, agent, priority, state, age_s}`

#### Scenario: Configurable snapshot cadence
- **WHEN** the broker starts
- **THEN** it SHALL read `slot_snapshot_interval_s` from environment (`SLOT_SNAPSHOT_INTERVAL_S`, default 30) and use that value as the ticker period

### Requirement: Slot-assignment outcome logging
The broker SHALL log the result of every slot-assignment reply publish and every `SlotTable.Assign` call.

#### Scenario: Reply publish success
- **WHEN** `sendSlotAssigned` or the equivalent path in `assignFromQueue` successfully publishes the reply
- **THEN** the broker SHALL log at INFO with `msg: "slot_assigned_reply_published"` and fields `{slot, agent, correlation_id}`

#### Scenario: Reply publish failure
- **WHEN** the reply publish returns an error
- **THEN** the broker SHALL log at ERROR with `msg: "slot_assigned_reply_failed"`, the error, and fields `{slot, agent, correlation_id}`, and SHALL revert the provisional assignment

#### Scenario: Assign collision detected
- **WHEN** `SlotTable.Assign` returns false (slot already occupied)
- **THEN** the broker SHALL log at WARN with `msg: "slot_assign_collision"` and fields `{slot, agent}`

## REMOVED Requirements

### Requirement: Slot 0 pinned to main-agent at startup
**Reason**: The hardcoded pin meant the broker advertised `slot_count` slots but only `slot_count − 1` were actually allocatable via `RequestSlot`. Main-agent had no knowledge of the pinned slot and never released or used it. Removing the pin makes slot allocation uniform across agents and restores effective capacity to `slot_count`.

**Migration**: No user-facing API change. Operators MUST restart the broker once after deployment so the in-memory `SlotTable` reinitializes without the pin. Main-agent continues to receive scheduling priority via its configured `AGENT_PRIORITY=0` and preemption of lower-priority agents — no pin is required for this.
