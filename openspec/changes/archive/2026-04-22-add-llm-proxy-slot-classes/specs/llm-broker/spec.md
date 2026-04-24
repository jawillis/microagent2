## ADDED Requirements

### Requirement: Slot classes partition the slot table
The llm-broker SHALL partition its slot table into classes. Each `SlotEntry` SHALL carry a `Class` field with one of the values `agent` or `hindsight`. The first `AGENT_SLOT_COUNT` slot indices SHALL be initialized as `agent` class; the next `HINDSIGHT_SLOT_COUNT` slot indices SHALL be initialized as `hindsight` class. A slot's class SHALL NOT change for the lifetime of the broker process.

#### Scenario: Slot classes initialized from config
- **WHEN** the broker starts
- **THEN** it SHALL read `AGENT_SLOT_COUNT` (default 4) and `HINDSIGHT_SLOT_COUNT` (default 0) from environment, SHALL verify the sum does not exceed the configured total `slot_count`, and SHALL initialize slot indices `[0, AGENT_SLOT_COUNT)` as class `agent` and `[AGENT_SLOT_COUNT, AGENT_SLOT_COUNT + HINDSIGHT_SLOT_COUNT)` as class `hindsight`

#### Scenario: Misconfigured class budget fails startup
- **WHEN** `AGENT_SLOT_COUNT + HINDSIGHT_SLOT_COUNT` exceeds the configured total `slot_count`
- **THEN** the broker SHALL log a structured error at ERROR level with `msg: "slot_class_budget_exceeds_total"` and SHALL exit non-zero

#### Scenario: Slot class appears in snapshot
- **WHEN** the broker emits a periodic slot-table snapshot
- **THEN** each entry in the `slots` array SHALL include a `class` field with value `agent` or `hindsight`

### Requirement: Slot assignment honors requested class
The llm-broker SHALL match slot requests to slots of the same class. A request with `slot_class: "agent"` (or unset) SHALL only be assigned a slot of class `agent`; a request with `slot_class: "hindsight"` SHALL only be assigned a slot of class `hindsight`.

#### Scenario: Agent-class request assigned agent slot
- **WHEN** the broker receives a `SlotRequestPayload` with `slot_class: "agent"` or with `slot_class` unset/empty
- **THEN** `FindUnassigned` SHALL only consider slots whose `Class == agent` and the assigned slot SHALL be an agent-class slot

#### Scenario: Hindsight-class request assigned hindsight slot
- **WHEN** the broker receives a `SlotRequestPayload` with `slot_class: "hindsight"`
- **THEN** `FindUnassigned` SHALL only consider slots whose `Class == hindsight` and the assigned slot SHALL be a hindsight-class slot

#### Scenario: No cross-class preemption
- **WHEN** a slot request of class X arrives and no slots of class X are unassigned
- **THEN** the broker SHALL only consider slots of class X for preemption and SHALL NOT preempt slots of any other class

#### Scenario: No cross-class queue service
- **WHEN** a slot of class X is released and the slot-request queue is non-empty
- **THEN** `assignFromQueue` SHALL service the oldest queued request of class X (if any); if the oldest queued request is of a different class, it SHALL remain queued

### Requirement: Slot-class field on slot and LLM request messages
The llm-broker SHALL accept a `slot_class` field on `SlotRequestPayload` and `LLMRequestPayload`. When the field is absent or empty, the broker SHALL treat it as `agent` for backward compatibility.

#### Scenario: Missing slot_class defaults to agent
- **WHEN** the broker receives a slot request or LLM request with `slot_class` absent or empty
- **THEN** it SHALL treat the request as class `agent` and apply agent-class routing logic

#### Scenario: Unrecognized slot_class rejected
- **WHEN** the broker receives a slot request with a `slot_class` value that is neither `agent` nor `hindsight`
- **THEN** it SHALL NOT assign a slot, SHALL log at WARN with `msg: "unknown_slot_class"` and the offending value, and SHALL NOT publish a slot-assigned reply

### Requirement: LLM request class validation
The llm-broker SHALL validate that the `slot_class` field on an `LLMRequestPayload` matches the class of the slot being referenced by `slot_id`.

#### Scenario: LLM request with matching class proceeds
- **WHEN** an LLM request arrives with a `slot_id` whose slot is class X, and the payload's `slot_class` is also X (or absent and X is agent)
- **THEN** normal ownership validation and proxying SHALL apply

#### Scenario: LLM request with mismatched class rejected
- **WHEN** an LLM request arrives with a `slot_class` value that differs from the class of the slot identified by `slot_id`
- **THEN** the broker SHALL NOT proxy the request, SHALL publish a done token with a structured error payload to the reply stream, and SHALL log at WARN with `msg: "llm_request_class_mismatch"` and fields `{slot_id, slot_class_expected, slot_class_requested, agent_id}`

## MODIFIED Requirements

### Requirement: LLM request slot-ownership validation
The broker SHALL validate that the `slot_id` in an LLM request message corresponds to a slot currently `Assigned` to the requesting `agent_id` before proxying the request to the llama-server. This validation SHALL apply uniformly across slot classes: an LLM request's `agent_id` must match the currently-assigned owner of the referenced slot, regardless of slot class.

#### Scenario: Request with owned slot proceeds
- **WHEN** an LLM request arrives with a `slot_id` that the broker has in `Assigned` state owned by the sending `agent_id`
- **THEN** the broker SHALL proxy the request to llama-server as normal (subject to additional class validation; see `Requirement: LLM request class validation`)

#### Scenario: Request with unowned slot rejected
- **WHEN** an LLM request arrives with a `slot_id` that is not `Assigned` to the sending `agent_id` (unassigned, provisional, or owned by another agent)
- **THEN** the broker SHALL NOT proxy the request, SHALL publish a done token with an error payload to the reply stream, and SHALL log the rejection

#### Scenario: Proxy identity owns hindsight-class slot
- **WHEN** an LLM request arrives from llm-proxy with a `slot_id` in a hindsight-class slot assigned to the llm-proxy identity
- **THEN** the broker SHALL treat ownership as valid and proceed (ownership semantics are identity-based, not class-specific)

### Requirement: Periodic slot-table snapshot logging
The broker SHALL emit a structured INFO log line every `slot_snapshot_interval_s` (default 30) containing the current state of every slot. Each entry SHALL include the slot's class.

#### Scenario: Snapshot emitted on ticker
- **WHEN** the snapshot ticker fires
- **THEN** the broker SHALL log at INFO with `msg: "slot_table_snapshot"` and a `slots` array where each entry has `{slot, class, agent, priority, state, age_s}`

#### Scenario: Configurable snapshot cadence
- **WHEN** the broker starts
- **THEN** it SHALL read `slot_snapshot_interval_s` from environment (`SLOT_SNAPSHOT_INTERVAL_S`, default 30) and use that value as the ticker period
