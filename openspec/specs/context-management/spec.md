## ADDED Requirements

### Requirement: Structured per-request logging
The context manager SHALL emit structured INFO log lines at every significant hand-off while processing a request, tagged with the request's `correlation_id`.

#### Scenario: Request decoded
- **WHEN** the context manager decodes a `ChatRequestPayload` from `stream:gateway:requests`
- **THEN** it SHALL log at INFO with `msg: "context_request_decoded"` and fields `{correlation_id, session_id, message_count}`

#### Scenario: Memory recall completed
- **WHEN** the call to memory-service's `/recall` endpoint returns (success or error)
- **THEN** the context manager SHALL log at INFO with `msg: "context_memory_recall"` and fields `{correlation_id, elapsed_ms, memory_count, outcome}` where `outcome` is `ok` or `error`

#### Scenario: Session history loaded
- **WHEN** the context manager loads session history from the response store
- **THEN** it SHALL log at INFO with `msg: "context_history_loaded"` and fields `{correlation_id, session_id, history_count}`

#### Scenario: Context published to agent
- **WHEN** the context manager publishes a context-assembled message to `stream:agent:{agent}:requests`
- **THEN** it SHALL log at INFO with `msg: "context_published"` and fields `{correlation_id, session_id, target_agent, assembled_count}`

### Requirement: System prompt byte-stability
The `system`-role message in the `ContextAssembledPayload.Messages` produced by the context manager SHALL be byte-identical to the configured system prompt (`config:chat.system_prompt`) on every request. The context manager SHALL NOT append, prepend, or otherwise mutate the system prompt based on recall results, session state, or any other per-turn input.

#### Scenario: System prompt unchanged when memories are recalled
- **WHEN** the context manager assembles a request for which memory-service `/recall` returned one or more memories
- **THEN** the `system`-role message in the published `ContextAssembledPayload.Messages` SHALL equal `config:chat.system_prompt` byte-for-byte, with no memory content or other per-turn content concatenated

#### Scenario: System prompt unchanged when no memories are recalled
- **WHEN** the context manager assembles a request for which memory-service `/recall` returned zero memories
- **THEN** the `system`-role message in the published `ContextAssembledPayload.Messages` SHALL equal `config:chat.system_prompt` byte-for-byte

#### Scenario: System prompt unchanged across consecutive turns of a session
- **WHEN** the context manager handles two consecutive requests for the same session
- **THEN** the `system`-role message contents of the two published `ContextAssembledPayload.Messages` SHALL be byte-identical

### Requirement: Recalled memories injected at the tail of the user turn
Recalled memories SHALL be injected into the `Content` of the final `user`-role message of the assembled output, using an XML-delimited `<context>` block preceding the user's original content. The context manager SHALL NOT emit recalled memories into the `system`-role message, and SHALL NOT emit a second `system`-role message containing recalled memories.

The exact serialization, when at least one memory is recalled, is:

```
<context>
- {memory[0].Content}
- {memory[1].Content}
...
</context>

{original userMessage.Content}
```

Rules:
- Memories SHALL be sorted by `MemorySummary.Score` descending (stable sort; ties broken by input order) before rendering.
- Each memory line SHALL be `"- " + MemorySummary.Content + "\n"`. No other fields of `MemorySummary` SHALL appear in the rendered block.
- A single blank line SHALL separate `</context>` from the original user content.

#### Scenario: Memories folded into user turn with XML delimiter
- **WHEN** memory-service `/recall` returns memories `[M1, M2]` (already sorted by score desc) for a user turn with content `"What should I eat?"`
- **THEN** the `user`-role message published to the target agent SHALL have `Content` equal to `"<context>\n- " + M1.Content + "\n- " + M2.Content + "\n</context>\n\nWhat should I eat?"`

#### Scenario: Memories sorted by score descending
- **WHEN** memory-service `/recall` returns memories with scores `[0.4, 0.9, 0.7]`
- **THEN** the rendered `<context>` block SHALL list them in score order `[0.9, 0.7, 0.4]`

#### Scenario: Only Content is rendered
- **WHEN** memory-service `/recall` returns a memory with `Content: "likes pizza"`, `Tags: ["preferences"]`, `Score: 0.8`
- **THEN** the rendered line for that memory SHALL be exactly `"- likes pizza\n"`, containing no other field values

#### Scenario: Empty recall leaves user content unchanged
- **WHEN** memory-service `/recall` returns zero memories for a user turn with content `"Hello"`
- **THEN** the `user`-role message published to the target agent SHALL have `Content` equal to `"Hello"`, with no `<context>` block emitted

#### Scenario: No second system message
- **WHEN** the context manager assembles any request
- **THEN** the published `ContextAssembledPayload.Messages` SHALL contain at most one message with role `"system"`, and that message SHALL be the first element of the array

### Requirement: Context-manager decorations are ephemeral
The `<context>` decoration applied by the context manager SHALL exist only on the in-flight `ContextAssembledPayload.Messages` bound for the target agent. The context manager SHALL NOT cause decorated content to be persisted to the response store, nor to be included in any message that is later consumed as canonical session history.

The raw user turn (prior to decoration) is what SHALL appear in any downstream history-reconstruction path, including but not limited to `response.Store.GetSessionMessages`, `response.Store.WalkChain`, and any gateway-side reconstruction of the `messages` array on future turns.

#### Scenario: Raw user turn persisted on the response store
- **WHEN** a request flows through the context manager with memories `[M1]` recalled, and the resulting LLM response is stored by the gateway
- **THEN** the `Response.Input` persisted in the response store SHALL contain the raw user turn content (e.g. `"What should I eat?"`), and SHALL NOT contain the `<context>` block or any memory content

#### Scenario: Subsequent-turn history is undecorated
- **WHEN** the context manager handles turn N+1 of a session whose turn N carried a decorated user message to the LLM
- **THEN** the `history` portion of the assembled `ContextAssembledPayload.Messages` for turn N+1 SHALL contain only the raw user/assistant content from turn N, with no `<context>` block and no memory content from turn N's recall

## MODIFIED Requirements

### Requirement: Context assembly with configurable memory settings
The context manager SHALL assemble chat context by combining the system prompt, recalled memories, conversation history, and the current user message. The conversation history SHALL be received from the gateway as a resolved messages array (reconstructed from the response chain by the gateway). The context manager SHALL NOT resolve response chains or access response storage directly. The system prompt, recall limit, recall threshold, pre-warm limit, and memory-service address SHALL be read from config and env vars at startup. The context manager SHALL consume requests via the resilient `ConsumeStream` helper so that consumer-group loss and other recoverable stream errors do not silently stall the pipeline.

#### Scenario: Context assembly from gateway-provided history
- **WHEN** the context manager receives a request on `stream:gateway:requests`
- **THEN** it SHALL use the `messages` field from the request payload as the conversation history, without performing any response chain resolution

#### Scenario: Memory recall uses configured limits
- **WHEN** the context manager recalls memories for a user message
- **THEN** it SHALL call memory-service `/recall` with `limit` from `recall_limit` in `config:memory` and pass through the user message as the query

#### Scenario: Pre-warm uses configured limit
- **WHEN** the context manager pre-warms memories after a response
- **THEN** it SHALL call memory-service `/recall` with `limit` from `prewarm_limit` in `config:memory`

#### Scenario: System prompt from config
- **WHEN** the context manager assembles context
- **THEN** it SHALL use `system_prompt` from `config:chat` as the system prompt content

#### Scenario: Memory-service address from env
- **WHEN** the context manager reads from memory-service
- **THEN** it SHALL use `MEMORY_SERVICE_ADDR` from environment (no default; required) as the HTTP base URL

#### Scenario: Resilient consume loop
- **WHEN** the context manager starts
- **THEN** it SHALL read from `stream:gateway:requests` using the resilient `ConsumeStream` helper, which recovers from consumer-group loss and surfaces error classes via logs

## ADDED Requirements

### Requirement: Tool-role and tool_calls round-trip in assembled context
The context manager SHALL preserve `ChatMsg` entries with `role: "tool"` and `ChatMsg` entries with non-empty `tool_calls` exactly as received from session history, without stripping, reordering, or modifying their content, `tool_calls`, or `tool_call_id` fields. These messages SHALL appear in the published `ContextAssembledPayload.Messages` in their original position within the history, with all fields preserved byte-for-byte.

#### Scenario: Tool-result message preserved in assembled history
- **WHEN** session history contains a message with `role: "tool"`, a non-empty `tool_call_id`, and `content` holding a tool result
- **THEN** the published `ContextAssembledPayload.Messages` SHALL include that message in its original position with `role`, `tool_call_id`, and `content` unchanged

#### Scenario: Assistant tool_calls preserved in assembled history
- **WHEN** session history contains a message with `role: "assistant"` and a non-empty `tool_calls` array
- **THEN** the published `ContextAssembledPayload.Messages` SHALL include that message with its `tool_calls` array unchanged (same `id`, `type`, `function.name`, and `function.arguments` for each call)

#### Scenario: Memory recall and system prompt invariants unchanged
- **WHEN** the context manager assembles a turn whose history contains tool-role messages or assistant messages with tool_calls
- **THEN** the existing system-prompt byte-stability and `<context>` memory-injection requirements SHALL continue to hold exactly as specified for non-tool turns, with the `<context>` block applied only to the final `user`-role message's content

### Requirement: Tool-call decorations are never added by the context manager
The context manager SHALL NOT invent, synthesize, inject, or modify `tool_calls` or `tool_call_id` fields on any message. These fields SHALL be touched only by components that legitimately produce them: the LLM via the broker's SSE reassembly, and the main-agent's tool-execution layer when it (in a future slice) synthesizes `role: "tool"` result messages.

#### Scenario: Context manager does not synthesize tool_calls
- **WHEN** the context manager assembles any request
- **THEN** no message emitted by the context manager SHALL have a `tool_calls` array that was not present verbatim on the corresponding input message from history

#### Scenario: Context manager does not synthesize tool_call_id
- **WHEN** the context manager assembles any request
- **THEN** no message emitted by the context manager SHALL have a `tool_call_id` value that was not present verbatim on the corresponding input message from history

### Requirement: ContextAssembledPayload carries speaker_id
The `ContextAssembledPayload` published by the context manager SHALL include a `speaker_id` field that echoes the value resolved by the gateway for the originating turn. The field SHALL be present on every payload; when the gateway resolved the speaker to `"unknown"`, the field SHALL be the literal string `"unknown"`.

#### Scenario: Known speaker propagated
- **WHEN** the gateway publishes a request with resolved `speaker_id="jason"`
- **THEN** the `ContextAssembledPayload` emitted by the context manager SHALL have `speaker_id="jason"`

#### Scenario: Unknown speaker propagated
- **WHEN** the gateway resolves a request's speaker to `"unknown"`
- **THEN** the `ContextAssembledPayload` SHALL have `speaker_id="unknown"`

### Requirement: Memory recall is scoped by speaker when configured
During context assembly, the context manager SHALL call memory-service `/recall` with `speaker_id` set to the turn's resolved speaker when `config:memory.recall_default_speaker_scope` is one of `primary` or `explicit`. When the scope is `any`, the context manager SHALL NOT add an implicit `speaker_id` filter to the recall call.

#### Scenario: Scope `primary` scopes recall
- **WHEN** `config:memory.recall_default_speaker_scope="primary"` and the turn's resolved speaker is `"jason"`
- **THEN** the context manager's `/recall` call SHALL include `speaker_id="jason"`

#### Scenario: Scope `any` does not scope recall
- **WHEN** `config:memory.recall_default_speaker_scope="any"`
- **THEN** the context manager's `/recall` call SHALL NOT include a `speaker_id` field

#### Scenario: Scope `explicit` without speaker_id logs warning
- **WHEN** `config:memory.recall_default_speaker_scope="explicit"` and the turn's resolved speaker is `"unknown"`
- **THEN** the context manager SHALL skip the recall call, log WARN with `msg="context_recall_skipped_no_speaker"`, and proceed with context assembly without memory injection
