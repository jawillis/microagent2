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
