## ADDED Requirements

### Requirement: LLM streaming execution loop
The agent runtime SHALL communicate with the LLM via the broker using streaming mode, maintaining a token-by-token progress log of the in-flight generation.

#### Scenario: Streaming execution
- **WHEN** the agent receives assembled context and an assigned slot from the broker
- **THEN** it sends a streaming completion request and processes tokens as they arrive, appending each to a progress log

#### Scenario: Complete generation
- **WHEN** the LLM finishes generating a response (stream ends)
- **THEN** the agent publishes the complete response to the appropriate Valkey stream and releases its slot

### Requirement: Progress tracking for resumption
The agent runtime SHALL maintain an up-to-date log of streamed LLM output so that work can be checkpointed and potentially resumed after preemption.

#### Scenario: Preemption during generation
- **WHEN** the agent receives a preemption signal while streaming a response
- **THEN** it saves the current progress log (tokens generated so far), cancels the LLM stream, releases the slot, and publishes a slot-released acknowledgment

#### Scenario: Resumption after preemption
- **WHEN** a preempted agent is re-assigned a slot
- **THEN** it MAY resume generation by including the previously generated partial output in the new request context, avoiding full regeneration

### Requirement: Slot request and release lifecycle
The agent runtime SHALL request a slot from the broker before any LLM interaction and release it immediately upon completion or preemption. After receiving a slot-assigned reply, the agent runtime SHALL publish a `SlotAssignedAck` message on the broker's slot-requests stream confirming receipt. If `WaitForReply` returns an error or times out, the agent runtime SHALL publish a defensive `SlotRelease` with `slot_id == -1` and its own `agent_id` so the broker can reclaim any slot the broker may have attributed to this agent.

#### Scenario: Slot acquisition
- **WHEN** the agent has work to perform requiring LLM inference
- **THEN** it publishes a slot request to the broker with its agent ID and priority, and waits for a slot assignment response

#### Scenario: Ack published after slot assignment received
- **WHEN** the agent's `WaitForReply` returns a slot-assigned message
- **THEN** the agent SHALL publish a `SlotAssignedAck` to the broker's slot-requests stream with the same correlation ID before storing the slot ID in its runtime state or returning from `RequestSlot`

#### Scenario: Defensive release on WaitForReply timeout
- **WHEN** the agent's `WaitForReply` returns `ErrTimeout` or any other error while requesting a slot
- **THEN** the agent SHALL publish a `SlotRelease` with `slot_id == -1` and its `agent_id`, log the timeout with the correlation ID at WARN, and return the original error to the caller

#### Scenario: Slot release on completion
- **WHEN** the agent completes its LLM interaction
- **THEN** it publishes a slot release message to the broker within 1 second of completion

### Requirement: Structured per-request logging
The agent runtime and agent main loops SHALL emit structured INFO log lines at every significant hand-off, tagged with the current message's `correlation_id`.

#### Scenario: Message received
- **WHEN** the agent reads a context-assembled message from its request stream
- **THEN** it SHALL log at INFO with `msg: "message_received"` and fields `{correlation_id, session_id}`

#### Scenario: Slot request outcome
- **WHEN** `RequestSlot` returns (success or error)
- **THEN** the agent SHALL log at INFO with `msg: "slot_request_result"` and fields `{correlation_id, outcome, slot_id, elapsed_ms}` where `outcome` is one of `acquired`, `timeout`, `error`

#### Scenario: LLM request published
- **WHEN** the agent publishes the LLM request to `stream:broker:llm-requests`
- **THEN** it SHALL log at INFO with `msg: "llm_request_published"` and fields `{correlation_id, slot_id}`

#### Scenario: Execution completed
- **WHEN** `Execute` returns a final result or error
- **THEN** the agent SHALL log at INFO with `msg: "execute_done"` and fields `{correlation_id, slot_id, elapsed_ms, outcome}` where `outcome` is one of `ok`, `preempted`, `error`

#### Scenario: Slot release outcome
- **WHEN** the deferred `ReleaseSlot` runs
- **THEN** the agent SHALL log at INFO with `msg: "slot_released"` and fields `{correlation_id, slot_id}`, or at ERROR if the release publish failed

### Requirement: Token forwarding for interactive agents
The main interactive agent SHALL forward tokens to the gateway in real time via pub/sub for client-facing streaming responses.

#### Scenario: Real-time token delivery
- **WHEN** the main agent receives a token from the LLM stream
- **THEN** it publishes the token to `channel:tokens:{session_id}` with minimal added latency

### Requirement: Agent worker loops use resilient consume
Agent main loops (main-agent and retro-agent) SHALL read their inbound streams via the resilient `ConsumeStream` helper rather than hand-rolled `for { ReadGroup ... if err != nil continue }` loops, so that consumer-group disruption and other recoverable stream errors do not silently stall agent processing.

#### Scenario: Main-agent context-assembled stream uses ConsumeStream
- **WHEN** main-agent starts
- **THEN** it SHALL read from `stream:agent:main-agent:requests` using `ConsumeStream`

#### Scenario: Retro-agent trigger stream uses ConsumeStream
- **WHEN** retro-agent starts
- **THEN** it SHALL read from `stream:retro:triggers` using `ConsumeStream`

#### Scenario: Agent loop recovers from FLUSHDB
- **WHEN** the agent's consumer group is lost (e.g. `FLUSHDB`)
- **AND** a new message is subsequently published to the agent's inbound stream
- **THEN** the agent SHALL process that message without requiring a restart
