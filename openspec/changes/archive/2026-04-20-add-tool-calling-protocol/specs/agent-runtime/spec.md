## ADDED Requirements

### Requirement: Execute accepts tools and returns assembled tool_calls
The agent runtime's `Execute` function SHALL accept an optional `tools []ToolSchema` parameter and an optional `onToolCall func(ToolCall)` callback, and SHALL return the assembled text and any assembled `[]ToolCall` alongside the existing error return. When `tools` is empty, the runtime SHALL behave byte-identically to the pre-change implementation, and the returned `[]ToolCall` SHALL be empty.

#### Scenario: Tools forwarded to broker
- **WHEN** a caller invokes `Execute` with a non-empty `tools` argument
- **THEN** the runtime SHALL populate `LLMRequestPayload.Tools` with those schemas before publishing to `stream:broker:llm-requests`

#### Scenario: onToolCall invoked per assembled call
- **WHEN** the runtime receives a `TypeToolCall` reply message with a completed `ToolCall` payload
- **THEN** it SHALL invoke the provided `onToolCall` callback with that `ToolCall`, and SHALL append the call to the slice returned from `Execute`

#### Scenario: Empty tool_calls return for text-only turns
- **WHEN** `Execute` completes for a request whose reply stream contained zero `TypeToolCall` messages
- **THEN** the returned `[]ToolCall` SHALL be empty and `onToolCall` SHALL NOT have been invoked

### Requirement: Tool-call observation log
The agent runtime SHALL emit a structured INFO log line summarizing the tool calls observed for each `Execute` invocation that returned at least one.

#### Scenario: Tool calls observed log
- **WHEN** `Execute` returns with a non-empty `[]ToolCall`
- **THEN** the runtime SHALL log at INFO with `msg: "tool_calls_observed"` and fields `{correlation_id, slot, count, names}` where `names` is an array of the distinct `function.name` values observed

### Requirement: Preemption waits for in-flight tool_call finalization
When the agent runtime observes a preemption signal while consuming its reply stream, it SHALL NOT return immediately. Instead, it SHALL arm a finalize deadline of `TOOL_CALL_FINALIZE_TIMEOUT_MS` (env-configurable, default 2000ms) and continue draining the reply stream for both `TypeToken` and `TypeToolCall` messages until the terminal `TokenPayload.Done` arrives or the deadline trips. This preserves the invariant that no partial `ToolCall` is ever exposed to callers, while allowing in-flight tool-call assembly (which the broker performs atomically at stream close) to reach the runtime.

#### Scenario: Complete tool_calls returned after preemption
- **WHEN** preemption is observed mid-stream
- **AND** the broker subsequently emits a completed `TypeToolCall` followed by a terminal `TokenPayload.Done` within the finalize ceiling
- **THEN** `Execute` SHALL return with that assembled call in the returned `[]ToolCall` and an error of `ErrPreempted`

#### Scenario: Finalize ceiling trips on stalled stream
- **WHEN** preemption is observed and no terminal `TokenPayload.Done` arrives within `TOOL_CALL_FINALIZE_TIMEOUT_MS`
- **THEN** `Execute` SHALL return with whatever complete tool_calls were received before the ceiling and an error of `ErrPreempted`; no partial tool_call SHALL appear in the returned slice because the broker never emits partials

#### Scenario: Configurable finalize ceiling
- **WHEN** the runtime is initialized
- **THEN** it SHALL read `TOOL_CALL_FINALIZE_TIMEOUT_MS` from environment and use that value as the finalize ceiling, defaulting to 2000ms when unset
