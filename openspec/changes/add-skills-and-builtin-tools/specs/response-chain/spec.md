## ADDED Requirements

### Requirement: Function-call and function-call-output items appear in Output
A response's `Output` array MAY contain `function_call` and `function_call_output` items produced during an agentic turn, interleaved with the final `message` assistant item. The ordering within `Output` SHALL preserve the chronological order in which main-agent emitted them during the tool loop, so history reconstruction faithfully reproduces the turn's agentic trace.

#### Scenario: Ordered trace stored
- **WHEN** main-agent's tool loop for a turn emits, in order, a `function_call` for `list_skills`, a `function_call_output` for its result, a `function_call` for `read_skill`, a `function_call_output` for its result, and a final assistant text message
- **THEN** the persisted `Response.Output` SHALL be exactly that sequence of items, in that order

### Requirement: Session history reconstruction handles function_call_output in Output
`Store.GetSessionMessages` and the gateway's `chainToMessages` helper SHALL decode `function_call_output` items from a response's `Output` array into `ChatMsg` entries with `role: "tool"`, `content: item.Output`, and `tool_call_id: item.CallID`, placed in their original position within the output sequence.

#### Scenario: Output-side function_call_output becomes a tool message
- **WHEN** history reconstruction encounters a response whose `Output` contains a `function_call_output` with `CallID: "c1"` and `Output: "result body"`
- **THEN** the corresponding reconstructed `ChatMsg` SHALL have `Role == "tool"`, `Content == "result body"`, and `ToolCallID == "c1"`

#### Scenario: Output-side ordering preserved
- **WHEN** a response's `Output` interleaves function_call, function_call_output, and message items
- **THEN** the reconstructed `ChatMsg` slice SHALL contain them in the same relative order
