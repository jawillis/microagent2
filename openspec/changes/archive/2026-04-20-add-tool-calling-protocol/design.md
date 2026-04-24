## Context

Today the broker proxies `{model, messages, stream}` to llama.cpp `/v1/chat/completions` and streams `delta.content` tokens back. Main-agent calls `Execute`, receives a single string, publishes. `ChatMsg` is `{Role, Content}`. There is no protocol path for tools, tool calls, or tool-result messages anywhere in the system.

This change is the foundational slice of a three-slice effort to add tool calling. It intentionally lands no tools — it lands only the wire-format extensions, broker pass-through, SSE reassembly, a separate pub/sub channel for tool-call events, and a runtime `Execute` return extension. Slices 2 (skills + built-in tools) and 3 (MCP integration) depend on this slice.

Stakeholders: main-agent, llm-broker, gateway, dashboard-ui, context-manager, response-store. Retro-agent is not in scope — retrospection treats tool-call content as opaque message content.

## Goals / Non-Goals

**Goals:**
- `ChatMsg`, `LLMRequestPayload`, and the broker request body speak OpenAI-compatible tool-calling wire format.
- Streamed `delta.tool_calls[]` fragments are reassembled server-side and emitted as complete `ToolCall` events, never as partial JSON.
- Tool-call events flow on a separate pub/sub channel from user-visible tokens, so the existing transcript rendering is not polluted.
- Non-tool-calling turns are byte-identical to today (regression-free).
- Tool-call messages (`role: "tool"`, `tool_calls` on assistant messages) round-trip through the context-assembler and response-store without mutation.
- Preemption mid-tool-call-stream finalizes any in-flight tool_call assembly before honoring the preemption, so no partial JSON ever surfaces.

**Non-Goals:**
- Building any tools. No `list_skills`, no `read_skill`, no `shell`, no MCP.
- Implementing a tool-execution loop in main-agent. `Execute` returns tool_calls; main-agent logs them and ignores them for now.
- Injecting skill manifests into the system prompt (slice 2).
- Configuring or connecting to MCP servers (slice 3).
- Retro-agent awareness of tool-call semantics (treated as opaque content).
- Changing slot allocation, preemption signaling transport, registry, or context-manager stream resilience.

## Decisions

### D1: Tool loop lives in the agent, not the broker

Broker stays a dumb proxy: slots + llama.cpp pass-through. Tool orchestration is an agent-layer concern — the agent is what decides what tools exist, what to invoke, and what to do with results. This slice doesn't build the loop, but it sets up the return surface (`Execute` returns tool_calls) so slice 2 can add the loop without changing the broker.

*Alternatives:* put the loop in the broker (wrong layer — broker is a traffic cop, not an agent); add a tool-executor service (extra hops, slot contention, protocol bloat for no clear win at this scale).

### D2: Wire format is OpenAI chat-completions, verbatim

`ChatMsg` JSON fields are exactly `tool_calls`, `tool_call_id`, matching OpenAI / llama.cpp. `LLMRequestPayload` gets `tools` and `tool_choice`, matching the chat-completions request body. No transformation between our types and llama.cpp's body — pass through. This is what llama.cpp expects; departing from it buys us nothing.

*Alternatives:* an internal normalized schema translated on each hop (unnecessary abstraction for one LLM backend).

### D3: Streamed tool_calls reassembled in the broker, not in the agent

The SSE stream from llama.cpp emits `delta.tool_calls[]` fragments — `index`, `id`, `function.name` typically arrive once; `function.arguments` streams in pieces across many chunks. The broker already owns SSE parsing; teaching it to accumulate per-`index` and emit complete tool_calls at `[DONE]` keeps SSE-parsing logic in one place and gives the rest of the system a clean `ToolCall` event boundary.

Accumulator structure:

```go
type toolCallAcc struct {
    ID        string
    Name      string
    ArgsBuf   strings.Builder  // concatenated arguments fragments
}
// keyed by delta.tool_calls[i].index
```

On each chunk, merge fields if present (id/name only on first appearance for a given index; arguments append). On `[DONE]` — or stream close for any reason — emit each accumulator as a complete `ToolCall` via a new `TypeToolCall` reply message.

*Alternatives:* emit fragments downstream, reassemble in the agent (exposes half-parsed JSON to the agent layer and complicates preemption handling — the same fragment stream would need to flow to two places).

### D4: Tool-call events go on a separate pub/sub channel

Introduce `channel:tool-calls:{session_id}`, parallel to `channel:tokens:{session_id}`. Tool-call events published here carry the assembled `ToolCall` as the payload. The gateway subscribes to both channels when streaming a response.

Rationale: if we reused `channel:tokens:`, the UI would either need to filter tool-call messages from the token stream (fragile) or render partial JSON as typed text (broken). A dedicated channel is the clean cut.

*Alternatives:* one channel with a `type` field discriminator (works but forces every token consumer to re-check type on every message); interleave tool_call JSON as tokens (breaks the "tokens are visible text" invariant).

### D5: `Execute` returns `(string, []ToolCall, error)`

Current: `func (r *Runtime) Execute(ctx, messages, onToken) (string, error)`.
New: `func (r *Runtime) Execute(ctx, messages, tools, onToken, onToolCall) (string, []ToolCall, error)`.

- `tools []ToolSchema` — forwarded to the broker as `Tools` in `LLMRequestPayload`.
- `onToolCall func(ToolCall)` — optional callback, invoked as each tool_call is fully assembled (symmetric to `onToken`). Useful for pub/sub publishing without bundling that into the runtime.
- Return includes `[]ToolCall` — the final assembled list. Empty for non-tool-calling turns (regression-free).

Main-agent threads the new return but ignores tool_calls in this slice (logs a `tool_calls_observed` line for observability).

*Alternatives:* a dedicated `ExecuteWithTools` method (two code paths, drift risk); a single `ExecutionResult` struct return (cleaner but larger refactor surface — deferred).

### D6: Preemption finalizes in-flight tool_call assembly before returning

The current preemption path cancels the SSE read mid-stream. For pure-text turns that's fine — we return whatever accumulated. For tool_call turns, a mid-stream cancel leaves partial `arguments` JSON in the accumulator, which is unusable.

Rule: when preemption is observed by the broker's SSE loop, finish consuming the current chunk's delta, then if any tool_call accumulator has non-empty `Name`, continue consuming until `[DONE]` or the underlying HTTP body closes. Only then honor preemption (emit assembled tool_calls, emit `done` with preemption outcome).

Bounded-latency concern: if llama.cpp takes a long time to finish after preemption, we block. Mitigate with a hard ceiling (`TOOL_CALL_FINALIZE_TIMEOUT_MS`, default 2000 — one-shot env var, not per-config-store). If the ceiling trips, drop partial tool_calls, log a `tool_call_preempt_dropped` WARN with `{correlation_id, pending_indices}`, and honor preemption.

*Alternatives:* always honor preemption immediately and discard partial tool_calls (loses every mid-call preemption; common if slots are hot-contested); keep partial fragments and flag them (pushes unusable state downstream).

### D7: Context-assembler and response-store round-trip tool fields without touching them

The assembler's job stays: system prompt + history + current turn. A `role: "tool"` message gets the same treatment as a `role: "user"` message — it's a line in history. The `tool_calls` array on assistant messages travels as-is. This is a pure additive-field round-trip; no re-serialization logic changes semantics.

Response-store's canonical persisted shape needs to include `tool_calls` and `tool_call_id` if present. This is a wire-format extension of the stored `Response.Input` / `Response.Output` content. Existing responses without these fields deserialize fine (default empty).

*Alternatives:* strip tool fields on store, re-derive on read (impossible — the LLM emitted them; the store is the source of truth).

### D8: Gateway forwards tool-call events alongside tokens for streaming clients

The gateway already subscribes to `channel:tokens:{session_id}` for streaming. It will additionally subscribe to `channel:tool-calls:{session_id}` for the same session and emit tool-call events as a distinct SSE event type (e.g., `event: tool_call`, payload: the assembled `ToolCall`). The existing `event: token` (or equivalent) path is unchanged.

The Responses API's structured events already accommodate typed events; we add one event kind. Non-streaming responses include tool_calls in the final response object (if any were assembled) — no API break.

*Alternatives:* require clients to poll a separate endpoint for tool-call events (breaks the streaming contract).

### D9: Dashboard renders tool-call events as collapsed status blocks

The transcript UI shows assistant/user messages. A tool-call event renders as a distinct line: `🔧 list_skills(...)` collapsed — click to expand and see the full JSON arguments. Tool-result messages (`role: "tool"`) render the same way, collapsed, showing the tool name + output preview.

No new styling system needed — a class + `<details>`/`<summary>` primitives. This is small, contained UI work.

*Alternatives:* render tool calls in the transcript as inline JSON (verbose, unreadable); hide them entirely (obscures what the agent is doing).

## Risks / Trade-offs

- **[Risk] llama.cpp SSE chunk shape varies by build.** `delta.tool_calls[]` layout is stable per OpenAI spec, but some llama.cpp builds may emit variations (e.g., arguments fully in the first chunk, or named `function_call` in legacy mode). → Mitigation: accumulator treats missing fields as "preserve previous"; `function_call` legacy path not supported (log a `tool_call_legacy_unsupported` WARN if encountered and drop).

- **[Risk] Preemption finalize-ceiling may drop tool_calls under load.** A 2s ceiling can trip on a slow model emitting a long arguments blob. → Mitigation: env-configurable; log drops prominently; slice 2's tool-loop can be built to tolerate a dropped tool_call (treat as "no call made" and let the model retry on next turn).

- **[Risk] Stored responses now carry `tool_calls` that existing downstream readers may not expect.** Anything JSON-parsing with a strict schema would break. → Mitigation: verify existing readers use Go struct decoding with field omission tolerance (they do — stdlib `encoding/json` ignores unknown and accepts missing fields by default).

- **[Risk] Regression in non-tool-calling turns from SSE reader refactor.** The accumulator path must be a no-op when no `delta.tool_calls[]` ever appears. → Mitigation: the accumulator is only touched when a delta chunk contains tool_calls; content-only chunks take the existing path. Regression test: existing broker SSE tests must pass unchanged.

- **[Trade-off] Two pub/sub channels per session instead of one.** Slight operational cost (another subscribe per request). → Accepted: clean separation is worth the subscription overhead; gateway already multiplexes multiple subscribes elsewhere.

- **[Trade-off] `Execute` signature change touches every caller.** Only two callers exist (main-agent, retro-agent). Retro-agent passes `nil` tools and `nil` onToolCall and ignores the empty `[]ToolCall` return. Small, localized. → Accepted.

## Migration Plan

No data migration. Deployment is a standard rolling restart of gateway, broker, main-agent in any order — the protocol extensions are additive:

- Old broker + new agent: agent sends `tools` in payload; old broker ignores unknown JSON fields and proxies without them. Model returns content only. `[]ToolCall` is empty. No regression.
- New broker + old agent: agent sends no `tools`; broker passes through empty tools array. No regression.
- Old gateway + new broker/agent: gateway doesn't subscribe to `channel:tool-calls:`; tool-call events are published but unconsumed (harmless on pub/sub). No regression.

Rollback: revert binaries. Response-store entries written with `tool_calls` continue to deserialize cleanly on the old binary (unknown fields ignored by `encoding/json`).

## Open Questions

1. **Should `ToolCall.Type` be enforced as `"function"` only?** Today llama.cpp only emits function-typed tool calls. Enforcing at the type level is simpler; leaving it as a string preserves forward compat if llama.cpp adds other types. **Lean:** keep as string, no enum, document that only `"function"` is handled downstream in slice 2.

2. **Should the gateway emit a synthetic `tool_call_pending` event as fragments arrive, for "typing indicator"–style UI?** Currently the gateway only learns about a tool_call at assembly time. A streaming "the model is composing a call" indicator would require fragment-level events. **Lean:** no. Defer to slice 2 or beyond — collapsed status blocks don't need it.

3. **What happens if the same correlation_id produces tool_calls on both the old channel and new channel during a rolling deploy?** Strictly shouldn't happen because the new broker is the only thing emitting `channel:tool-calls:`, but worth confirming the gateway doesn't double-count during the overlap window. **Lean:** confirm during rollout verification; not worth a protocol guard.
