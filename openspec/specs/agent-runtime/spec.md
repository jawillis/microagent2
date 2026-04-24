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
The agent runtime SHALL request a slot from the broker before any LLM interaction and release it immediately upon completion or preemption.

#### Scenario: Slot acquisition
- **WHEN** the agent has work to perform requiring LLM inference
- **THEN** it publishes a slot request to the broker with its agent ID and priority, and waits for a slot assignment response

#### Scenario: Slot release on completion
- **WHEN** the agent completes its LLM interaction
- **THEN** it publishes a slot release message to the broker within 1 second of completion

### Requirement: Token forwarding for interactive agents
The main interactive agent SHALL forward tokens to the gateway in real time via pub/sub for client-facing streaming responses.

#### Scenario: Real-time token delivery
- **WHEN** the main agent receives a token from the LLM stream
- **THEN** it publishes the token to `channel:tokens:{session_id}` with minimal added latency
