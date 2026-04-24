## ADDED Requirements

### Requirement: ToolResult wire type
The messaging package SHALL define a `ToolResult` struct carrying `CallID` (string, matching the originating `ToolCall.ID`) and `Output` (string, the tool's result body). The JSON shape SHALL use field names `call_id` and `output`.

#### Scenario: ToolResult JSON shape
- **WHEN** a `ToolResult` is marshaled to JSON
- **THEN** the object SHALL contain `call_id` (string) and `output` (string)

### Requirement: ChatResponsePayload carries tool results
`messaging.ChatResponsePayload` SHALL carry an optional `tool_results` field (array of `ToolResult`) in addition to the existing `tool_calls` field. When main-agent completes a tool-executing turn, its final reply on the reply stream SHALL include the full sequence of tool_calls and matching tool_results so the gateway can persist them as `function_call` and `function_call_output` output items.

#### Scenario: ToolResults round-trip
- **WHEN** main-agent publishes a `ChatResponsePayload` with `ToolResults = [{CallID: "c1", Output: "result"}]`
- **THEN** the gateway SHALL receive a `ChatResponsePayload` with `ToolResults` decoded byte-identical to what was sent

#### Scenario: Omitted when empty
- **WHEN** a `ChatResponsePayload` is produced for a pure-text turn with no tool activity
- **THEN** its marshaled JSON SHALL omit both `tool_calls` and `tool_results`, preserving byte-compatibility with the pre-change wire format

### Requirement: TypeToolResult message type
The messaging schema SHALL define a message type constant `TypeToolResult` (wire string: `"tool_result"`) used for pub/sub events representing tool-execution results. Together with `TypeToolCall`, consumers of `channel:tool-calls:{session_id}` can distinguish calls from results.

#### Scenario: ToolResult message shape
- **WHEN** main-agent publishes a tool-result event
- **THEN** the message SHALL have `type: "tool_result"`, and its payload SHALL be a `ToolResultPayload` containing `session_id`, `call_id`, and `output`

#### Scenario: Distinct from TypeToolCall
- **WHEN** the gateway consumes messages on `channel:tool-calls:{session_id}`
- **THEN** `TypeToolCall` and `TypeToolResult` SHALL be independently dispatchable (distinct `msg.Type` values)
