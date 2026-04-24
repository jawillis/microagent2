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

## MODIFIED Requirements

### Requirement: Broker uses configurable slot and preempt settings
The LLM broker SHALL read `slot_count` and `preempt_timeout_ms` from the config store at startup, with env var and hardcoded fallbacks. The model name sent to the llama.cpp server SHALL be read from `config:chat` `model` field.

#### Scenario: Slot count from config
- **WHEN** the broker initializes its slot table
- **THEN** it SHALL create the number of slots specified by `slot_count` from `config:broker` (default 4)

#### Scenario: Preempt timeout from config
- **WHEN** the broker executes a preemption
- **THEN** it SHALL wait for `preempt_timeout_ms` from `config:broker` (default 5000) before force-releasing the slot

#### Scenario: Model name from config
- **WHEN** the broker proxies an LLM request to llama.cpp
- **THEN** it SHALL use the `model` value from `config:chat` (default "default") in the request body

### Requirement: Broker consumers use resilient consume
The llm-broker SHALL read `stream:broker:slot-requests` and `stream:broker:llm-requests` via the resilient `ConsumeStream` helper rather than hand-rolled `for { ReadGroup ... if err != nil continue }` loops, so that consumer-group disruption does not silently stall slot allocation or LLM proxying.

#### Scenario: Slot-request consumer uses ConsumeStream
- **WHEN** the broker starts
- **THEN** its slot-request consumer loop SHALL use `ConsumeStream` on `stream:broker:slot-requests`

#### Scenario: LLM-request consumer uses ConsumeStream
- **WHEN** the broker starts
- **THEN** its LLM-request consumer loop SHALL use `ConsumeStream` on `stream:broker:llm-requests`

#### Scenario: Broker recovers from FLUSHDB without slot-table corruption
- **WHEN** the Valkey consumer group for broker slot-requests is lost (e.g. `FLUSHDB`)
- **AND** a new slot request is subsequently published
- **THEN** the broker SHALL process that request, and the in-memory slot table SHALL remain consistent (no double-assignment on resumption)

## REMOVED Requirements

### Requirement: Slot 0 pinned to main-agent at startup
**Reason**: The hardcoded pin meant the broker advertised `slot_count` slots but only `slot_count − 1` were actually allocatable via `RequestSlot`. Main-agent had no knowledge of the pinned slot and never released or used it. Removing the pin makes slot allocation uniform across agents and restores effective capacity to `slot_count`.

**Migration**: No user-facing API change. Operators MUST restart the broker once after deployment so the in-memory `SlotTable` reinitializes without the pin. Main-agent continues to receive scheduling priority via its configured `AGENT_PRIORITY=0` and preemption of lower-priority agents — no pin is required for this.

## ADDED Requirements

### Requirement: Tools and tool_choice pass-through to llama.cpp
The broker SHALL forward the `tools` and `tool_choice` fields from `LLMRequestPayload` into the request body sent to llama.cpp's `/v1/chat/completions` endpoint, without inspection, validation, or transformation. When these fields are absent or empty on the incoming payload, they SHALL be omitted from the outbound request body, preserving byte-identical wire format for non-tool-calling traffic.

#### Scenario: Tools present in payload
- **WHEN** the broker receives an LLM request with a non-empty `tools` array
- **THEN** the outbound JSON body to llama.cpp SHALL include a top-level `tools` field whose value is the provided array, and SHALL include `tool_choice` when the payload's `ToolChoice` is non-empty

#### Scenario: Tools absent in payload
- **WHEN** the broker receives an LLM request with `tools` empty or unset and `tool_choice` empty or unset
- **THEN** the outbound JSON body SHALL omit both `tools` and `tool_choice`

### Requirement: Streamed tool_call reassembly
The broker's SSE reader SHALL accumulate `delta.tool_calls[]` fragments across chunks, keyed by each fragment's `index` value, merging `id` and `function.name` on first appearance and appending each `function.arguments` string. The broker SHALL NOT emit any `TypeToolCall` message until assembly for that index is complete (the stream closes, emits `[DONE]`, or the finalize ceiling trips).

#### Scenario: Single tool call streamed across multiple chunks
- **WHEN** llama.cpp emits N chunks each containing `delta.tool_calls[0].function.arguments` fragments
- **THEN** the broker SHALL concatenate those fragments in order into a single `arguments` string, and SHALL emit exactly one `TypeToolCall` message for that index when the stream closes

#### Scenario: Multiple parallel tool calls
- **WHEN** llama.cpp emits chunks with `delta.tool_calls[]` entries at indices 0 and 1 interleaved across the stream
- **THEN** the broker SHALL maintain independent accumulators per index, and SHALL emit one `TypeToolCall` message per index at stream close, each with a populated `id`, `function.name`, and `function.arguments`

#### Scenario: Fields merged on first appearance
- **WHEN** a chunk arrives with `delta.tool_calls[i].id` or `delta.tool_calls[i].function.name` set
- **THEN** the broker SHALL record those values into the accumulator for index `i` and SHALL ignore subsequent `id`/`name` values for the same index (first-wins), while continuing to append `arguments` fragments

### Requirement: Legacy function_call stream shape unsupported
The broker SHALL NOT attempt to parse the legacy OpenAI `delta.function_call` (singular, non-array) shape. When encountered, the broker SHALL log at WARN with `msg: "tool_call_legacy_unsupported"` and fields `{correlation_id}` and SHALL drop the legacy payload, emitting no `TypeToolCall` message for it.

#### Scenario: Legacy function_call dropped with warning
- **WHEN** an SSE chunk contains `delta.function_call` but no `delta.tool_calls`
- **THEN** the broker SHALL log `tool_call_legacy_unsupported` at WARN and SHALL NOT emit any `TypeToolCall` message for that chunk

### Requirement: Tool-call assembly finalization log
The broker SHALL emit a structured INFO log line for every tool call it successfully assembles and emits.

#### Scenario: Tool call assembled
- **WHEN** the broker emits a completed `TypeToolCall` message
- **THEN** it SHALL log at INFO with `msg: "tool_call_assembled"` and fields `{correlation_id, call_id, name, args_bytes, index}` where `args_bytes` is the byte length of the assembled `arguments` string

### Requirement: Atomic tool-call emission
The broker SHALL emit `TypeToolCall` messages only when a tool call is fully assembled at stream close. No partial or progressively-emitted tool-call JSON SHALL ever leave the broker. This guarantee combined with the broker always draining llama.cpp's stream to `[DONE]` means the broker does not itself observe or react to agent preemption — preemption-aware finalization lives in the agent runtime, not the broker.

#### Scenario: No partial tool_call JSON emitted
- **WHEN** the broker is mid-way through assembling one or more tool calls
- **THEN** it SHALL NOT publish any `TypeToolCall` message on the reply stream until the stream has closed (either via `[DONE]` or the body ending)

#### Scenario: Broker always drains llama.cpp to completion
- **WHEN** the broker begins proxying an LLM request with tools enabled
- **THEN** the broker SHALL continue reading the SSE stream until `[DONE]` or HTTP body close, regardless of any agent-side preemption, so any tool calls the LLM chose to emit are always finalizable

### Requirement: Non-tool-calling stream behavior unchanged
For any request whose SSE stream contains zero `delta.tool_calls[]` fragments, the broker's streaming behavior SHALL be byte-identical to the pre-change implementation: `delta.content` tokens flow to the reply stream as `TypeToken` messages and to `channel:tokens:{session_id}`, no `TypeToolCall` messages are emitted, and the terminal `TypeToken` with `done: true` is published exactly once.

#### Scenario: Pure-text turn unaffected
- **WHEN** llama.cpp's SSE response for a request contains only `delta.content` fragments
- **THEN** the sequence and content of messages published to the reply stream and to `channel:tokens:{session_id}` SHALL match the pre-change behavior exactly, with no `TypeToolCall` messages emitted

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

### Requirement: llm-broker registers with panel descriptor
llm-broker SHALL publish a registration message on `stream:registry:announce` at startup and maintain a periodic heartbeat. The registration SHALL include a `dashboard_panel` descriptor with two sections: a form section for config, and a status section showing the live slot table.

#### Scenario: Registration + heartbeat
- **WHEN** llm-broker starts
- **THEN** it SHALL publish a `RegisterPayload` with `agent_id: "llm-broker"`, `capabilities: ["llm-broker"]`, `heartbeat_interval_ms` from env (default 3000), `preemptible: false`, and a valid `dashboard_panel` descriptor; it SHALL continue heartbeating on `channel:heartbeat:llm-broker`

#### Scenario: Panel descriptor sections
- **WHEN** the broker constructs its descriptor
- **THEN** the descriptor SHALL have `title: "Broker"`, `order: 300`, and sections:
  - form section with `config_key: "broker"` and fields for `agent_slot_count` (integer), `hindsight_slot_count` (integer), `preempt_timeout_ms` (integer), `provisional_timeout_ms` (integer), `slot_snapshot_interval_s` (integer). Fields that require restart SHALL have a description noting so.
  - status section with `layout: "table"` pointing at `/v1/broker/slots`

### Requirement: Runtime-tunable broker config with env fallback
llm-broker SHALL resolve its configuration via `config.ResolveBroker` which reads `config:broker` from Valkey first, then falls back to environment variables (`AGENT_SLOT_COUNT`, `HINDSIGHT_SLOT_COUNT`, `PREEMPT_TIMEOUT_MS`, `PROVISIONAL_TIMEOUT_MS`, `SLOT_SNAPSHOT_INTERVAL_S`), then hardcoded defaults. Timeout values SHALL be re-read at request time so changes from the dashboard take effect without restart. Slot-count values SHALL be read only at startup; changing them at runtime requires a broker restart.

#### Scenario: Valkey value wins over env
- **WHEN** `config:broker.preempt_timeout_ms` is set in Valkey and `PREEMPT_TIMEOUT_MS` is set in env
- **THEN** the broker SHALL use the Valkey value

#### Scenario: Env fallback on empty Valkey
- **WHEN** `config:broker.preempt_timeout_ms` is absent and `PREEMPT_TIMEOUT_MS` is in env
- **THEN** the broker SHALL use the env value

#### Scenario: Default on empty Valkey and env
- **WHEN** neither Valkey nor env has the value
- **THEN** the broker SHALL use the hardcoded default

#### Scenario: Preempt timeout hot-reloads
- **WHEN** the dashboard saves a new `preempt_timeout_ms` to `config:broker`
- **THEN** subsequent preemption events SHALL use the new value without a broker restart

#### Scenario: Slot-count change ignored at runtime
- **WHEN** the dashboard saves a new `agent_slot_count` or `hindsight_slot_count`
- **THEN** the value is persisted but the live SlotTable SHALL NOT rebuild; the operator is expected to restart the broker to apply slot-count changes

### Requirement: Gateway slot snapshot endpoint
The gateway SHALL expose `GET /v1/broker/slots` that returns a JSON object containing the broker's current slot table snapshot. The gateway SHALL fetch the snapshot from the broker via a messaging request/reply pair on a dedicated stream.

#### Scenario: Snapshot fetched and returned
- **WHEN** a client GETs `/v1/broker/slots`
- **THEN** the gateway SHALL publish a `SlotSnapshotRequest` message on `stream:broker:slot-snapshot-requests`, wait up to 5 seconds for a `SlotSnapshotResponse` on the reply stream, and return a JSON object `{"slots": [<entry>...]}` with one entry per slot containing `{slot, class, state, agent, priority, age_s}`

#### Scenario: Timeout on broker unreachable
- **WHEN** the broker does not reply within 5 seconds
- **THEN** the gateway SHALL return HTTP 503 with `{"error": "broker_unreachable"}`

### Requirement: Broker exposes snapshot via messaging
llm-broker SHALL consume `stream:broker:slot-snapshot-requests` and respond with the current slot table on the reply stream.

#### Scenario: Snapshot publish
- **WHEN** the broker receives a `SlotSnapshotRequest`
- **THEN** it SHALL publish a `SlotSnapshotResponse` to the request's reply stream containing the current snapshot within 1 second under normal load
