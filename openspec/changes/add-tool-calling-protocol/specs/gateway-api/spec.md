## ADDED Requirements

### Requirement: Gateway collects tool-call events for storage only
For any streaming `/v1/responses` request, the gateway SHALL subscribe to `channel:tool-calls:{session_id}` alongside `channel:tokens:{session_id}` to collect internal tool-call events for persistence in the stored response, but SHALL NOT relay these events to the client as SSE events. The client didn't request tool calling (tools are server-configured built-ins); the internal agent loop's trace is an implementation detail hidden from the API surface.

#### Scenario: Client-facing SSE stream is text-only
- **WHEN** the gateway is streaming a `/v1/responses` turn whose agent loop invokes one or more tools
- **THEN** the SSE events emitted to the HTTP client SHALL be limited to `response.created`, `response.output_text.delta`, and `response.completed`; no `response.tool_call` or `response.tool_result` events SHALL reach the client

#### Scenario: Tool-call subscription used for storage
- **WHEN** the gateway receives a `TypeToolCall` or `TypeToolResult` message on `channel:tool-calls:{session_id}` during a streaming turn
- **THEN** it SHALL append the corresponding `function_call` or `function_call_output` `OutputItem` to an internal accumulator and SHALL persist the accumulator alongside the final assistant text when the turn completes

#### Scenario: Subscription lifecycle
- **WHEN** the gateway begins streaming a response
- **THEN** it SHALL subscribe to both `channel:tokens:{session_id}` and `channel:tool-calls:{session_id}` before emitting the first SSE event, and SHALL unsubscribe from both when the response completes or the client disconnects

### Requirement: Client-facing response body hides server-internal tool trace
For a non-streaming `/v1/responses` response, and for the `response.completed` SSE event in the streaming path, the gateway SHALL filter `function_call` and `function_call_output` items out of the `output` array before sending to the client. These items are retained in the stored response so the dashboard can render the full agentic trace.

#### Scenario: Tool trace stripped from client body
- **WHEN** an agent reply carries `ToolCalls` and `ToolResults` from a server-side tool loop
- **THEN** the non-streaming response body returned to the HTTP client SHALL contain only `message`-typed items in its `output` array, and SHALL NOT contain any `function_call` or `function_call_output` items

#### Scenario: Pure-text turns unaffected
- **WHEN** an agent reply carries no tool activity
- **THEN** the client-facing `output` array SHALL be byte-identical to the pre-change shape (a single `message` item)

#### Scenario: Storage retains full trace
- **WHEN** the gateway persists a response produced by a tool-executing turn
- **THEN** the stored `Response.Output` SHALL contain the interleaved `function_call` + `function_call_output` items followed by the assistant `message` item, even though the client-facing body omitted them
