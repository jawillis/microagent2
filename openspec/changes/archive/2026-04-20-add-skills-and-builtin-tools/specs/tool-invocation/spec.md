## ADDED Requirements

### Requirement: Tool interface
A tool SHALL implement a `Tool` interface exposing a `Name()` method returning the tool's stable identifier, a `Schema()` method returning a `messaging.ToolSchema` wire-compatible with the OpenAI chat-completions tools field, and an `Invoke(ctx, argsJSON)` method returning a string result or an error. Tool implementations SHALL be registered with a `Registry` at main-agent startup.

#### Scenario: Name uniqueness
- **WHEN** `Registry.Register(t)` is invoked with a tool whose `Name()` matches an already-registered tool
- **THEN** `Register` SHALL return a non-nil error and SHALL NOT add the duplicate

#### Scenario: Schema is wire-ready
- **WHEN** `Registry.Schemas()` is invoked
- **THEN** it SHALL return a slice of `ToolSchema` that can be passed directly as `LLMRequestPayload.Tools` without further transformation

### Requirement: Registry deterministic Schemas order
`Registry.Schemas()` SHALL return tool schemas in a deterministic order (insertion order) so that LLM request bodies are stable across identical turns.

#### Scenario: Order matches registration
- **WHEN** tools `list_skills` and `read_skill` are registered in that order
- **THEN** `Schemas()` SHALL return them in the same order on every call

### Requirement: Registry invoke resolution
`Registry.Invoke(ctx, name, argsJSON)` SHALL look up the tool by name and call its `Invoke`. When the tool is not registered, the registry SHALL return a result string `{"error": "unknown tool: <name>"}` (JSON-encoded) and a nil error, so the agent's tool loop can feed the result back to the model without treating it as a fatal failure.

#### Scenario: Unknown tool returns structured error
- **WHEN** `Invoke(ctx, "nonexistent", "{}")` is called on a registry with no matching tool
- **THEN** the result string SHALL be `{"error":"unknown tool: nonexistent"}` and the error return SHALL be nil

#### Scenario: Tool errors surface as structured errors
- **WHEN** a registered tool's `Invoke` returns a non-nil error
- **THEN** the registry SHALL return a result string containing a JSON object `{"error": "<err.Error()>"}` and a nil error, preserving the loop invariant that tool outputs are always strings

#### Scenario: Tool panic recovery
- **WHEN** a registered tool's `Invoke` panics
- **THEN** the registry SHALL recover, log at ERROR with `msg: "tool_panic"` and fields `{tool_name, panic}`, and return `{"error":"tool panicked"}` as the result

### Requirement: Built-in list_skills tool
A built-in tool named `list_skills` SHALL be registered at main-agent startup. Its schema SHALL describe a function that takes no arguments and returns the catalog. Its `Invoke` SHALL return a JSON-encoded array of `{name, description}` objects derived from `skills.Store.List()` in the store's deterministic order.

#### Scenario: Empty catalog
- **WHEN** `list_skills` is invoked against an empty skills store
- **THEN** the result SHALL be `[]` (an empty JSON array)

#### Scenario: Populated catalog
- **WHEN** the store contains skills `a` (desc `A`) and `b` (desc `B`)
- **THEN** `list_skills` SHALL return `[{"name":"a","description":"A"},{"name":"b","description":"B"}]`

#### Scenario: Extra arguments tolerated
- **WHEN** `list_skills` is invoked with a non-empty arguments object
- **THEN** it SHALL ignore the arguments and return the full catalog

### Requirement: Built-in read_skill tool
A built-in tool named `read_skill` SHALL be registered at main-agent startup. Its schema SHALL describe a function that takes a required `name` string parameter. Its `Invoke` SHALL return the skill's Markdown body when found, or a JSON error object when not found or when the arguments are invalid.

#### Scenario: Hit returns body
- **WHEN** `read_skill` is invoked with arguments `{"name": "estimate-tokens"}` for an existing skill
- **THEN** the result SHALL be the exact Markdown body bytes stored by the skills store

#### Scenario: Miss returns structured error
- **WHEN** `read_skill` is invoked with `{"name": "nonexistent"}`
- **THEN** the result SHALL be `{"error":"skill not found: nonexistent"}`

#### Scenario: Missing name argument
- **WHEN** `read_skill` is invoked with `{}` or `{"name": ""}`
- **THEN** the result SHALL be `{"error":"name argument is required"}`

#### Scenario: Malformed JSON arguments
- **WHEN** `read_skill` is invoked with `argsJSON` that does not parse as a JSON object with a string `name` field
- **THEN** the result SHALL be `{"error":"invalid arguments: <detail>"}` with `<detail>` describing the parse failure

### Requirement: Skill manifest injection
Main-agent SHALL, before publishing each LLM request during a user turn, append a skill manifest section to the first `system`-role message's content when the tool registry holds at least one skill-facing tool. The manifest section SHALL be demarcated by XML-style tags and list each skill as a bullet of `- <name>: <description>`.

#### Scenario: Manifest appended when skills exist
- **WHEN** main-agent prepares the `messages` array for a turn and the skills store has at least one skill (`a: A`, `b: B`)
- **THEN** the `system`-role message's `Content` SHALL be suffixed with `"\n\n<available_skills>\n- a: A\n- b: B\n</available_skills>"` (single blank line separator, trailing newline inside the closing tag)

#### Scenario: No manifest when catalog empty
- **WHEN** the skills store is empty
- **THEN** no `<available_skills>` block SHALL be appended, and the `system`-role message's `Content` SHALL be byte-identical to what the context-manager produced

#### Scenario: Injection is downstream of context-manager
- **WHEN** the context-manager produces a `ContextAssembledPayload` with a byte-stable system prompt
- **THEN** main-agent SHALL inject the manifest on its own copy of the messages before calling the broker; the context-manager's output SHALL remain unchanged

#### Scenario: Regenerated per turn
- **WHEN** main-agent handles two consecutive turns for the same session while the skills store's contents have not changed
- **THEN** each turn SHALL produce the same injected manifest content (the registry is immutable post-startup; no caching is required to achieve this)

### Requirement: Bounded tool-execution loop
Main-agent SHALL run a bounded tool-execution loop per user turn. Each iteration SHALL (a) acquire a slot, (b) invoke the runtime's `Execute` with the current `messages` and `Registry.Schemas()`, (c) release the slot, (d) if `Execute` returned tool_calls, invoke each through the registry and append the assistant-with-tool_calls and corresponding tool-result messages to `messages`, then continue. The loop SHALL terminate when `Execute` returns zero tool_calls, returns `ErrPreempted`, returns any other error, or the iteration cap is reached.

#### Scenario: Single-iteration pure-text turn
- **WHEN** a user turn produces zero tool_calls from the model
- **THEN** the loop SHALL run exactly one iteration, publish the final `ChatResponsePayload` with the assistant text, and exit

#### Scenario: Two-iteration agentic turn
- **WHEN** the model emits one tool_call in iteration 1 and zero tool_calls in iteration 2
- **THEN** the loop SHALL invoke the tool between iterations, append the assistant tool_calls message and the tool-result message to `messages`, and terminate after iteration 2 with the iteration-2 assistant text

#### Scenario: Iteration cap reached
- **WHEN** the loop has run `TOOL_LOOP_MAX_ITER` iterations (default 10) and the most recent iteration still returned non-empty tool_calls
- **THEN** main-agent SHALL log at WARN with `msg: "tool_loop_max_iter_hit"` and fields `{correlation_id, iterations}`, SHALL publish a final `ChatResponsePayload` containing whatever assistant text was last produced appended with a newline and `"(max iterations reached)"`, and SHALL NOT invoke further tools

#### Scenario: Preemption mid-loop
- **WHEN** `Execute` returns `ErrPreempted` during an iteration
- **THEN** the loop SHALL exit; main-agent SHALL NOT acquire another slot or invoke further tools for this turn

#### Scenario: Configurable iteration cap
- **WHEN** main-agent starts
- **THEN** it SHALL read `TOOL_LOOP_MAX_ITER` from environment and use that value as the iteration cap, defaulting to 10 when unset

### Requirement: Slot acquisition per iteration
Main-agent SHALL acquire a slot only for the duration of each `Execute` call within the tool loop, releasing it before invoking tools. Tool invocations SHALL NOT hold a broker slot. This ensures slot fairness for concurrent agents when a tool takes non-trivial time.

#### Scenario: Slot held during Execute
- **WHEN** main-agent enters iteration N of the loop
- **THEN** it SHALL call `RequestSlot`, then `Execute`, then `ReleaseSlot`, in that order, before invoking any tools

#### Scenario: No slot held during tool invocation
- **WHEN** main-agent invokes a registered tool between iterations
- **THEN** the broker slot previously held for the prior `Execute` SHALL have been released before `Registry.Invoke` is called

### Requirement: Tool invocation logging
Main-agent SHALL emit a structured INFO log line for every tool invocation.

#### Scenario: Invocation logged
- **WHEN** main-agent invokes a tool via `Registry.Invoke`
- **THEN** it SHALL log at INFO with `msg: "tool_invoked"` and fields `{correlation_id, tool_name, args_bytes, elapsed_ms, outcome, result_bytes}` where `outcome` is one of `ok`, `error` (including unknown-tool and argument-parse errors), or `panic`

### Requirement: Tool-call and tool-result pub/sub events
Main-agent SHALL publish each tool_call and each tool result to `channel:tool-calls:{session_id}` so the gateway can relay live SSE events and collect results for persistence. Tool-result events SHALL use a distinct message type that allows consumers to distinguish calls from results.

#### Scenario: Tool call event published
- **WHEN** the runtime's `onToolCall` callback is invoked with an assembled `ToolCall`
- **THEN** main-agent SHALL publish a `TypeToolCall` message with `ToolCallPayload{SessionID, Call}` to `channel:tool-calls:{session_id}`

#### Scenario: Tool result event published
- **WHEN** main-agent has invoked a tool via the registry and obtained its result string
- **THEN** main-agent SHALL publish a `TypeToolResult` message with `ToolResultPayload{SessionID, CallID, Output}` to `channel:tool-calls:{session_id}`
