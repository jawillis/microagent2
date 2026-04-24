## Why

Main-agent today is a single-shot LLM caller: context in, tokens out, done. To support skills and external tools (slices 2 and 3), the LLM request/response protocol must first learn to speak OpenAI-style tool calling end-to-end — `tools` on the request, `tool_calls` on the response, `role: "tool"` messages in history. Landing the protocol independently of any actual tool implementation keeps the foundational wire-format change small, reviewable, and regression-verifiable before we start wiring skill execution or MCP servers on top of it.

## What Changes

- Extend `messaging.ChatMsg` with `ToolCalls []ToolCall` and `ToolCallID string`, and allow `Role == "tool"`.
- Extend `messaging.LLMRequestPayload` with `Tools []ToolSchema` and `ToolChoice string`.
- Introduce `ToolSchema`, `ToolCall`, and `ToolFunction` types in the messaging package, wire-compatible with OpenAI / llama.cpp chat completions.
- Broker passes `Tools` and `ToolChoice` straight through to llama.cpp's `/v1/chat/completions` endpoint unchanged.
- Broker SSE reader reassembles `delta.tool_calls[].function.arguments` fragments (keyed by `delta.tool_calls[i].index`) and emits completed `ToolCall`s at stream close.
- New message type `TypeToolCall` distinct from `TypeToken`, flowing on a new pub/sub channel `channel:tool-calls:{session_id}` separate from `channel:tokens:{session_id}` so the UI can render tool invocations as collapsed status blocks without polluting the transcript.
- Agent runtime `Execute` returns both accumulated text and any assembled `ToolCall`s; this slice does NOT implement a tool loop — callers inspect and ignore tool_calls for now.
- Preemption mid-tool-call-stream finalizes the in-flight tool_call assembly before honoring preemption, so no partial tool_call JSON ever leaves the broker.
- Context-assembler and response-store preserve `role: "tool"` and `tool_calls` fields round-trip through session history.
- Structured log line `tool_call_assembled` emitted per completed tool_call with `{correlation_id, call_id, name, args_bytes}`.

Explicitly **out of scope** for this slice (deferred to later slices):
- Any built-in tool implementations (`list_skills`, `read_skill`, `shell`).
- Skills directory scanning or `SKILL.md` parsing.
- System-prompt skill manifest injection.
- Main-agent tool-execution loop.
- MCP client, MCP server configuration, or dashboard MCP UI.

## Capabilities

### New Capabilities
<!-- None. This slice strictly extends existing capabilities. -->

### Modified Capabilities
- `inter-service-messaging`: `ChatMsg` gains `tool_calls` and `tool_call_id`; `LLMRequestPayload` gains `tools` and `tool_choice`; new `TypeToolCall` message type.
- `llm-broker`: pass `tools`/`tool_choice` through to llama.cpp; reassemble streamed `delta.tool_calls[]` fragments; finalize in-flight tool_call on preemption.
- `agent-runtime`: `Execute` returns assembled tool_calls alongside text; preemption blocks until any in-flight tool_call assembly is complete.
- `context-management`: preserve `role: "tool"` messages and `tool_calls` fields in assembled context and stored history without modification.
- `gateway-api`: expose `channel:tool-calls:{session_id}` to dashboard clients alongside the existing `channel:tokens:{session_id}`.
- `dashboard-ui`: render tool-call events from the new channel as collapsed status blocks in the transcript, not as visible text tokens.

## Impact

**Code**
- `internal/messaging/payloads.go`, `internal/messaging/message.go` — type extensions, new message type constant.
- `internal/broker/broker.go` — request body includes `tools`/`tool_choice`; SSE reader gains per-index tool_call accumulator; new tool_call emission path distinct from content-token path.
- `internal/agent/runtime.go` — `Execute` return signature gains `[]ToolCall`; preemption handling blocks during active tool_call assembly.
- `cmd/main-agent/main.go` — threads the `(string, []ToolCall, error)` return; ignores tool_calls for this slice (logs for observability only).
- `internal/context/assembler.go`, `internal/response/store.go` — round-trip `role: "tool"` and `tool_calls` without stripping.
- `internal/gateway/server.go`, `internal/gateway/responses.go` — subscribe to and forward `channel:tool-calls:{session_id}` events to dashboard clients.
- `internal/gateway/web/app.js`, `internal/gateway/web/index.html`, `internal/gateway/web/style.css` — render tool-call events as collapsed status blocks.

**Wire format**
- `ChatMsg` JSON gains `tool_calls` (array) and `tool_call_id` (string); existing consumers must tolerate these fields.
- `LLMRequestPayload` JSON gains `tools` and `tool_choice`; omitted when empty (no regression for current callers).

**Regression surface**
- Non-tool-calling turns must behave byte-identically to today. Verified by existing broker/agent tests continuing to pass unmodified.
- Tool-call events must never appear on `channel:tokens:{session_id}`; visible token streaming must not include tool_call JSON fragments.

**Dependencies**
- No new third-party Go dependencies. Protocol types are plain structs with JSON tags.

**Does not touch**
- Slot allocation, preemption signaling mechanism, registry, retro-agent, context-manager stream resilience — all unchanged structurally.
