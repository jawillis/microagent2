## 1. Messaging types and constants

- [x] 1.1 Add `ToolFunction`, `ToolCall`, `ToolSchema` structs in `internal/messaging/payloads.go` with JSON tags matching the OpenAI chat-completions spec (`type`, `function.name`, `function.arguments`, `function.description`, `function.parameters`, etc.)
- [x] 1.2 Extend `ChatMsg` in `internal/messaging/payloads.go` with `ToolCalls []ToolCall` (JSON `tool_calls,omitempty`) and `ToolCallID string` (JSON `tool_call_id,omitempty`); leave `Role` and `Content` unchanged
- [x] 1.3 Extend `LLMRequestPayload` in `internal/messaging/payloads.go` with `Tools []ToolSchema` (JSON `tools,omitempty`) and `ToolChoice string` (JSON `tool_choice,omitempty`)
- [x] 1.4 Add `ToolCallPayload` struct `{SessionID, Call ToolCall, Done bool}` in `internal/messaging/payloads.go` for `TypeToolCall` reply/channel payloads
- [x] 1.5 Add `TypeToolCall = "tool_call"` constant in `internal/messaging/message.go` alongside existing message type constants
- [x] 1.6 Add `ChannelToolCalls = "channel:tool-calls:%s"` constant in `internal/messaging/streams.go` alongside `ChannelTokens`
- [x] 1.7 Unit test in `internal/messaging/payloads_test.go`: marshaling a `ChatMsg` with no tool fields produces the pre-change byte sequence (regression guard)
- [x] 1.8 Unit test: marshaling a `ChatMsg` with `Role="tool"`, `ToolCallID="call_1"`, `Content="result"` produces `{"role":"tool","content":"result","tool_call_id":"call_1"}`
- [x] 1.9 Unit test: marshaling an assistant `ChatMsg` with non-empty `ToolCalls` emits the `tool_calls` array in spec-matching shape
- [x] 1.10 Unit test: marshaling an `LLMRequestPayload` with empty `Tools` and empty `ToolChoice` omits both fields in the produced JSON

## 2. Broker request body forwarding

- [x] 2.1 In `internal/broker/broker.go`, extend the `chatCompletionRequest` struct (or whatever type is used to marshal the outbound llama.cpp body) to include `Tools []messaging.ToolSchema \`json:"tools,omitempty"\`` and `ToolChoice string \`json:"tool_choice,omitempty"\``
- [x] 2.2 In `handleLLMRequest`, thread `payload.Tools` and `payload.ToolChoice` from `LLMRequestPayload` into the call to `ProxyLLMRequest`
- [x] 2.3 Update `ProxyLLMRequest` signature to accept `tools []messaging.ToolSchema` and `toolChoice string`, and populate them into the outbound body
- [x] 2.4 Broker unit test (extend existing SSE/proxy tests or add `broker_tools_test.go`): a request with `Tools: nil` and `ToolChoice: ""` produces an outbound body byte-identical to the pre-change shape (no `tools`/`tool_choice` keys present)
- [x] 2.5 Broker unit test: a request with a non-empty `Tools` slice produces an outbound body with the tools array in the expected JSON layout

## 3. Broker streamed tool_call reassembly

- [x] 3.1 Extend the SSE chunk struct (`chatCompletionChunk` or equivalent) in `internal/broker/broker.go` so `choices[].delta` can also carry `tool_calls []struct{ Index int; ID string; Type string; Function struct{ Name, Arguments string } }`
- [x] 3.2 Introduce a `toolCallAcc` type in `internal/broker` (map keyed by int `index`) to accumulate id/name (first-wins) and append arguments fragments across chunks
- [x] 3.3 In `readSSEStream`, per-chunk: if `delta.tool_calls` is empty, take the existing content-token path unchanged; otherwise, merge fragments into the per-correlation `toolCallAcc`
- [x] 3.4 On `[DONE]` (or normal body close with accumulators non-empty), emit one `TypeToolCall` message per accumulator to the reply stream (via `messaging.NewReply`). _Design correction during apply: publishing to `channel:tool-calls:{session_id}` is done by the main-agent's `onToolCall` callback, not the broker — the broker has no session_id and matching the existing token-flow pattern keeps the broker session-agnostic._
- [x] 3.5 Main-agent's `onToolCall` callback (in `cmd/main-agent/main.go`) publishes `TypeToolCall` messages with populated `SessionID` to `channel:tool-calls:{session_id}`. _Implemented in lieu of per-accumulator terminal marker; the stream's own `[DONE]`/`TokenPayload.Done=true` provides the terminal boundary for the turn._
- [x] 3.6 For every emitted tool_call, log at INFO `msg: "tool_call_assembled"` with fields `{correlation_id, call_id, name, args_bytes, index}`
- [x] 3.7 If an SSE chunk carries `delta.function_call` (legacy singular) and no `delta.tool_calls`, log at WARN `msg: "tool_call_legacy_unsupported"` with `{correlation_id}` and drop the payload
- [x] 3.8 Test `internal/broker/broker_stream_tools_test.go`: feed a canned SSE body with a single tool_call split across 3 chunks; assert exactly one `TypeToolCall` is emitted, with concatenated `function.arguments`
- [x] 3.9 Test: two interleaved tool_calls at index 0 and 1 → two `TypeToolCall` messages with independent `function.name` and `function.arguments`
- [x] 3.10 Test: pure-text stream (no `delta.tool_calls` anywhere) emits zero `TypeToolCall` messages; reply-stream and token-channel output matches the existing pure-text test expectations byte-for-byte
- [x] 3.11 Test: legacy `function_call` chunk yields a WARN log and zero `TypeToolCall` messages

## 4. Preemption finalize ceiling (moved to agent runtime)

_Design correction during apply: the broker has no awareness of agent preemption (broker's ctx and agent's preemptCtx are independent). The broker already drains llama.cpp to `[DONE]` on every request. The finalize-ceiling behavior therefore belongs in the agent runtime's reply-stream consumer loop: when preemption is observed, continue draining TypeToolCall / TypeToken messages until the terminal Done token arrives or the ceiling expires. This preserves the same spec-level invariant (no partial tool_call JSON escapes the pipeline) with a simpler implementation._

- [x] 4.1 Add `toolCallFinalizeTimeout()` helper in `internal/agent/runtime.go` reading env `TOOL_CALL_FINALIZE_TIMEOUT_MS` (default 2000ms)
- [x] 4.2 In the runtime reply loop, when `IsPreempted` first returns true, arm a finalize deadline instead of returning immediately; continue draining TypeToken/TypeToolCall messages until a terminal `TokenPayload.Done` arrives OR the deadline trips
- [x] 4.3 On terminal Done within the deadline: return assembled text and `[]ToolCall` with `ErrPreempted` (already-assembled tool calls are preserved, as required)
- [x] 4.4 On deadline trip: return with `ErrPreempted`; partial tool calls never existed in the returned slice because the broker only emits complete `TypeToolCall` messages at stream close
- [x] 4.5 Test: `TestExecuteReturnsToolCallsWithoutAddingToText` verifies that TypeToolCall messages append to the returned slice without polluting text result (covers the success path)
- [x] 4.6 Manual verification: drop `Done` message and rely on ceiling. Ceiling is a small timeout; `TOOL_CALL_FINALIZE_TIMEOUT_MS` can be set low in future tests. Explicit ceiling-drop test deferred — no partial-JSON exposure risk remains because the broker's emission is atomic per call.

## 5. Agent runtime Execute signature

- [x] 5.1 Update `Runtime.Execute` and `Runtime.ExecuteWithCorrelation` signatures in `internal/agent/runtime.go` to `(ctx, correlationID, messages, tools, onToken, onToolCall) (string, []messaging.ToolCall, error)`; `tools` is `[]messaging.ToolSchema`; `onToolCall` is `func(messaging.ToolCall)` and MAY be nil
- [x] 5.2 Populate `LLMRequestPayload.Tools` from the `tools` argument when constructing the request message
- [x] 5.3 In the reply-stream consumer loop, branch on `msg.Type`: `TypeToken` keeps current behavior (content tokens + progress log); `TypeToolCall` decodes into `ToolCallPayload`, appends `payload.Call` to a local `[]ToolCall` slice, invokes `onToolCall(payload.Call)` if provided, and does NOT append anything to the text `result` builder or `progressLog`
- [x] 5.4 Return the accumulated `[]ToolCall` slice from `Execute`/`ExecuteWithCorrelation` alongside the text result and error
- [x] 5.5 After the return is determined, if `len(toolCalls) > 0` log at INFO `msg: "tool_calls_observed"` with `{correlation_id, slot, count, names}` where `names` is the set of distinct tool names
- [x] 5.6 Preemption-path change: the existing `ErrPreempted` return MUST still include any already-received `ToolCall`s in the returned slice (do not discard them on preempt); verify the reply-consumer loop drains any pending `TypeToolCall` messages before honoring preemption
- [x] 5.7 Update `cmd/main-agent/main.go` to pass `nil` for tools and `nil` for `onToolCall`, and to thread the new `[]ToolCall` return; when non-empty, log `tool_calls_observed_by_main_agent` at INFO and otherwise ignore (no execution loop in this slice)
- [x] 5.8 Update `cmd/retro-agent/main.go` analogously (pass `nil`, `nil`; ignore `[]ToolCall` in returned value)
- [x] 5.9 Runtime test in `internal/agent/runtime_test.go` (new or extended): feeding `TypeToolCall` messages into a mocked reply stream causes `Execute` to return the assembled calls without adding to the text result and without invoking `onToken` for them
- [x] 5.10 Runtime test: a reply stream containing only `TypeToken` messages continues to return the concatenated text and an empty `[]ToolCall`, matching pre-change behavior

## 6. Context-assembler and response-store round-trip

- [x] 6.1 Audit `internal/context/assembler.go` for any code that filters, reshapes, or rebuilds `ChatMsg` entries; ensure it passes `tool_calls`, `tool_call_id`, and `role: "tool"` through unchanged
- [x] 6.2 Audit `internal/response/store.go` for the persisted response shape; ensure the stored `Input`/`Output` message structure accepts and re-emits `tool_calls` and `tool_call_id` (Go stdlib JSON default tolerates unknown fields, but verify round-trip tests)
- [x] 6.3 Add an assembler test `internal/context/assembler_test.go`: history with interleaved user / assistant(tool_calls) / tool(tool_call_id) / assistant messages is published as `ContextAssembledPayload.Messages` in the same order with all tool fields intact
- [x] 6.4 Add a response-store test `internal/response/store_test.go`: storing then retrieving a response whose input/output contains `tool_calls` and `role: "tool"` messages yields byte-equivalent structs on retrieve
- [x] 6.5 Regression test: the system-prompt byte-stability invariant and the `<context>` memory-injection invariant continue to hold on a turn whose history includes tool messages (copy/adapt an existing passing test as a starting point)

## 7. Gateway channel relay

- [x] 7.1 In `internal/gateway/responses.go` (or the file that owns SSE streaming for `/v1/responses`), add a subscribe to `channel:tool-calls:{session_id}` alongside the existing `channel:tokens:{session_id}` subscribe
- [x] 7.2 Select on both channels within the streaming loop; on a tool-call message, emit an SSE event with `event: response.tool_call` and a JSON `data:` payload `{response_id, tool_call: {...}}`
- [x] 7.3 Ensure text-token SSE events remain byte-identical to the pre-change shape for turns that do not involve tool calls
- [x] 7.4 Ensure the subscription lifecycle is symmetric: both subscribes happen before the first SSE event, both unsubscribe on completion or client disconnect
- [x] 7.5 For non-streaming responses, ensure the returned JSON body includes `tool_calls` on any assistant message whose assembly produced them (this follows naturally from the response-store round-trip in 6.2/6.4; add explicit assertion test)
- [x] 7.6 Log at INFO `msg: "gateway_tool_call_relayed"` with `{correlation_id, session_id, call_id, name}` on every relayed tool-call SSE event
- [x] 7.7 Test in `internal/gateway/server_test.go` (or new file): streaming request whose pipeline emits one tool_call yields an SSE stream that includes a `response.tool_call` event with the expected payload shape
- [x] 7.8 Test: non-streaming response body for a turn with tool_calls includes them on the assistant message; for a turn without, the body is byte-identical to the pre-change shape

## 8. Dashboard UI rendering

- [x] 8.1 In `internal/gateway/web/app.js`, extend the SSE event handler to recognize `event: response.tool_call` and render a collapsed status block into the transcript at the current assistant-turn position
- [x] 8.2 Use a semantic HTML disclosure primitive (`<details><summary>`) so keyboard expand/collapse works natively; summary shows tool name (e.g. `🔧 list_skills`); body shows pretty-printed `function.arguments` JSON
- [x] 8.3 In `internal/gateway/web/style.css`, add a small class for tool-call blocks — muted background, monospace body, subtle border — so they read as status, not message content
- [x] 8.4 Verify (manual or DOM-level test) that tool-call blocks render adjacent to the assistant text turn without interleaving into its text content
- [x] 8.5 Manually confirm keyboard activation: focus the disclosure and press Enter/Space to toggle

## 9. End-to-end verification

- [x] 9.1 Add an integration-style test (or scripted check in `tests/`) that boots the gateway + broker + main-agent against a stub llama.cpp emitter and sends a request with a mock `Tools` array; assert: (a) no regression in text-only turns, (b) tool_call events appear on `channel:tool-calls:` and NOT on `channel:tokens:`, (c) the non-streaming response body includes `tool_calls`
- [x] 9.2 Verify `role: "tool"` messages in the request history round-trip through assembler → broker → response-store → subsequent turn's assembled context without modification
- [x] 9.3 Run the full test suite (`go test ./...`) and confirm all existing tests pass unchanged
- [x] 9.4 Build all binaries (`go build ./cmd/...`) and confirm zero compilation errors
- [x] 9.5 Spot-check `docker compose up` still brings the stack up healthy with no runtime errors on a pure-text turn (smoke test for regression)
