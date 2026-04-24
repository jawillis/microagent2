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
