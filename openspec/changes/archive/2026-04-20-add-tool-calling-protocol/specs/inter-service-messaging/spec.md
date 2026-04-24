## ADDED Requirements

### Requirement: Tool-calling wire types
The messaging package SHALL define `ToolSchema`, `ToolCall`, and `ToolFunction` types whose JSON shape is wire-compatible with the OpenAI chat-completions tool-calling spec and llama.cpp's OpenAI-compat endpoint, so that tool definitions and tool invocations can flow through the existing request/reply streams without any per-field translation at the broker boundary.

#### Scenario: ToolSchema JSON shape
- **WHEN** a `ToolSchema` is marshaled to JSON
- **THEN** the top-level object SHALL contain `type` (string, typically `"function"`) and `function` (object), where `function` contains `name` (string), `description` (string, optional), and `parameters` (object, JSON Schema draft-07 compatible)

#### Scenario: ToolCall JSON shape
- **WHEN** a `ToolCall` is marshaled to JSON
- **THEN** the object SHALL contain `id` (string), `type` (string, typically `"function"`), and `function` (object), where `function` contains `name` (string) and `arguments` (string, holding a JSON-encoded argument object)

#### Scenario: Omitted when empty
- **WHEN** a `ChatMsg` has no tool-calls and no tool-call-id, or a `LLMRequestPayload` has no tools and no tool-choice
- **THEN** the marshaled JSON SHALL omit `tool_calls`, `tool_call_id`, `tools`, and `tool_choice` respectively, ensuring non-tool-calling traffic is byte-identical to the pre-change wire format

### Requirement: ChatMsg carries tool-calling fields
`messaging.ChatMsg` SHALL carry, in addition to `role` and `content`, a `tool_calls` array (populated on assistant messages that request tool invocations) and a `tool_call_id` string (populated on messages with `role: "tool"` that carry the result of a prior tool call). The JSON field names SHALL be exactly `tool_calls` and `tool_call_id` to match the OpenAI chat-completions spec.

#### Scenario: Assistant message with tool_calls
- **WHEN** an assistant message carries one or more tool invocations
- **THEN** its JSON SHALL have `role: "assistant"`, `content` MAY be empty string, and `tool_calls` SHALL be a non-empty array of `ToolCall` objects

#### Scenario: Tool-result message
- **WHEN** a tool result is added to conversation history
- **THEN** its JSON SHALL have `role: "tool"`, `tool_call_id` SHALL equal the `id` of the originating `ToolCall`, and `content` SHALL hold the stringified tool output

#### Scenario: Plain text message unchanged
- **WHEN** a message has role `"user"`, `"assistant"`, or `"system"` with no tool involvement
- **THEN** its marshaled JSON SHALL omit `tool_calls` and `tool_call_id`, preserving byte-compatibility with the pre-change wire format

### Requirement: LLMRequestPayload carries tools and tool_choice
`messaging.LLMRequestPayload` SHALL carry optional `tools` (array of `ToolSchema`) and `tool_choice` (string) fields. These SHALL be forwarded unchanged to the llama.cpp chat-completions endpoint by the broker. When both are absent or empty, the marshaled payload SHALL be byte-identical to the pre-change shape.

#### Scenario: Tools forwarded to broker
- **WHEN** an agent constructs an `LLMRequestPayload` with a non-empty `tools` array
- **THEN** the payload's JSON SHALL include a `tools` field whose value is the provided schemas, and the broker SHALL include that field in the request body sent to llama.cpp

#### Scenario: ToolChoice forwarded
- **WHEN** an agent sets `ToolChoice` to a non-empty string (for example `"auto"`, `"none"`, or `"required"`)
- **THEN** the payload's JSON SHALL include `tool_choice` with that value

### Requirement: TypeToolCall message type
The messaging schema SHALL define a new message type constant `TypeToolCall` (wire string: `"tool_call"`) used by the broker to emit fully assembled tool-call events to reply streams and to the tool-calls pub/sub channel. This type is distinct from `TypeToken` and SHALL NOT be used for streaming content tokens.

#### Scenario: Tool-call reply message shape
- **WHEN** the broker emits a completed tool call on a reply stream
- **THEN** the message SHALL have `type: "tool_call"`, its `correlation_id` SHALL equal the originating LLM request, and its payload SHALL be a `ToolCallPayload` containing `session_id`, the assembled `ToolCall` object, and a `done` boolean that is true on the final tool-call for this turn

#### Scenario: TypeToolCall never carries content fragments
- **WHEN** the broker has not yet assembled a complete tool call
- **THEN** it SHALL NOT emit any `TypeToolCall` message for that call; partial arguments JSON SHALL NOT leave the broker

### Requirement: Tool-calls pub/sub channel
The inter-service messaging schema SHALL define a pub/sub channel `channel:tool-calls:{session_id}` parallel to `channel:tokens:{session_id}`. Tool-call events SHALL be published to the tool-calls channel and SHALL NOT be published to the tokens channel, preserving the invariant that the tokens channel carries only user-visible text tokens.

#### Scenario: Tool-call event published on tool-calls channel
- **WHEN** a completed tool call is emitted by the broker for a session
- **THEN** it SHALL be published to `channel:tool-calls:{session_id}` with message type `TypeToolCall`

#### Scenario: Tokens channel remains text-only
- **WHEN** the model emits tool-call delta fragments during streaming
- **THEN** no partial or assembled tool-call content SHALL be published to `channel:tokens:{session_id}`; that channel continues to carry only `TypeToken` messages with `delta.content` text
