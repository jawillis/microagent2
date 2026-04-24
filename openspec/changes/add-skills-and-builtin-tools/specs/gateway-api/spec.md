## ADDED Requirements

### Requirement: Gateway persists tool_calls and tool_results from the agent reply
For non-streaming `/v1/responses` requests, the gateway SHALL read `ChatResponsePayload.ToolCalls` and `ChatResponsePayload.ToolResults` from the agent's reply on the reply stream and persist them as interleaved `function_call` and `function_call_output` items in the **stored** response's `Output` array, preserving the pair ordering `function_call(c1), function_call_output(c1), function_call(c2), function_call_output(c2), ...` prior to the final assistant text message. These items are persistence-only — the client-facing response body has them stripped (see the slice-1 requirement "Client-facing response body hides server-internal tool trace").

#### Scenario: Non-streaming agentic turn stored with full trace
- **WHEN** the agent reply carries `ToolCalls = [c1, c2]` and `ToolResults = [{CallID: c1, Output: r1}, {CallID: c2, Output: r2}]` plus final text
- **THEN** the stored response's `Output` SHALL contain items in order: `function_call(c1)`, `function_call_output(c1,r1)`, `function_call(c2)`, `function_call_output(c2,r2)`, `message(assistant, final_text)`

#### Scenario: Pure-text turn unchanged
- **WHEN** the agent reply carries empty `ToolCalls` and `ToolResults`
- **THEN** the stored response's `Output` SHALL contain only the `message` item, byte-identical to the pre-change shape

### Requirement: Streaming gateway collects tool events for storage, not relay
For streaming responses, the gateway SHALL subscribe to `channel:tool-calls:{session_id}` and dispatch on the pub/sub message's `type` field. Both `TypeToolCall` and `TypeToolResult` events SHALL be appended to a per-turn `OutputItem` accumulator in arrival order. The gateway SHALL NOT forward these events to the client as SSE events. At stream completion, the accumulator is written into the stored response's `Output` array preceding the final assistant text item.

#### Scenario: Tool events not relayed as SSE
- **WHEN** the gateway receives a `TypeToolCall` or `TypeToolResult` message on `channel:tool-calls:{session_id}` during a streaming turn
- **THEN** no `response.tool_call` or `response.tool_result` SSE event SHALL be emitted to the HTTP client

#### Scenario: Streamed trace stored at stream completion
- **WHEN** the streaming turn receives, in order, a tool_call for `c1`, a tool_result for `c1`, and a terminal token_done
- **THEN** the persisted `Response.Output` SHALL contain `function_call(c1)`, `function_call_output(c1,...)`, and `message(assistant,...)` in that order
