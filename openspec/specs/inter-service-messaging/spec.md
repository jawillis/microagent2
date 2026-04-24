## ADDED Requirements

### Requirement: Stream-based request/reply messaging
Services SHALL use Valkey Streams for all durable request/reply communication, with consumer groups for reliable delivery.

#### Scenario: Request published to stream
- **WHEN** a service needs to send a request to another service
- **THEN** it publishes a message to the target stream using XADD containing at minimum: `type`, `correlation_id` (UUID), `payload`, `priority`, `timestamp`, and `reply_stream` (the stream where the response should be published)

#### Scenario: Response correlation
- **WHEN** a service receives a response on its reply stream
- **THEN** it matches the response to the original request using the `correlation_id` field

#### Scenario: Consumer group processing
- **WHEN** multiple instances of a service consume from the same stream
- **THEN** Valkey consumer groups ensure each message is delivered to exactly one consumer instance, with pending entry list (PEL) tracking for acknowledgment

### Requirement: Pub/sub event broadcasting
Services SHALL use Valkey pub/sub for ephemeral event broadcasting where message durability is not required.

#### Scenario: System event publication
- **WHEN** a significant event occurs (session ended, memory stored, agent registered)
- **THEN** the originating service publishes to `channel:events` with a message containing `type`, `timestamp`, `source`, and event-specific payload

#### Scenario: Token streaming
- **WHEN** an agent generates tokens from the LLM
- **THEN** tokens are published to `channel:tokens:{session_id}` for real-time consumption by the gateway

#### Scenario: Preemption signal
- **WHEN** the broker needs to preempt an agent
- **THEN** it publishes to `channel:agent:{agent_id}:preempt` with the preemption reason and requesting agent info

### Requirement: Correlation ID tracking
Every request message SHALL include a unique `correlation_id` that is echoed in all related response messages, enabling request tracing across service boundaries.

#### Scenario: End-to-end correlation
- **WHEN** a user request flows from gateway → context manager → agent → broker → llama-server and back
- **THEN** the original `correlation_id` is present in every intermediate message, allowing the full request lifecycle to be traced

### Requirement: Message schema consistency
All Valkey messages SHALL conform to a base schema with required fields, ensuring services can be developed and tested against a stable contract.

#### Scenario: Base message format
- **WHEN** any service publishes a message to a stream
- **THEN** the message contains at minimum: `type` (string), `correlation_id` (UUID string), `timestamp` (ISO 8601), `source` (service identifier), and `payload` (object)

### Requirement: SlotAssignedAck message type
The inter-service messaging schema SHALL define a `SlotAssignedAck` message type used by agents to confirm receipt of a slot assignment reply, enabling the broker's two-phase slot-assignment protocol.

#### Scenario: Ack message schema
- **WHEN** an agent publishes a `SlotAssignedAck` message
- **THEN** the message SHALL have `type: "slot_assigned_ack"`, its `correlation_id` SHALL match the original slot request, and the payload SHALL include `agent_id` (string) and `slot_id` (int)

#### Scenario: Ack routed on the broker inbound stream
- **WHEN** an agent publishes a `SlotAssignedAck`
- **THEN** it SHALL be published to `stream:broker:slot-requests` so the broker's existing slot-request consumer handles it

### Requirement: Resilient stream consumer helper
The messaging client SHALL expose a `ConsumeStream` function that runs a long-running consumer loop with explicit error classification, automatic consumer-group recovery, and phantom-consume detection, so that every long-running consumer in the system shares one resilience implementation.

#### Scenario: Handler invoked once per decoded message
- **WHEN** a caller runs `ConsumeStream` and N messages are published to the stream
- **THEN** the handler SHALL be invoked exactly N times, once per message, with each message decoded from the stream entry

#### Scenario: Successful handler acks the message
- **WHEN** the handler returns nil for a message
- **THEN** `ConsumeStream` SHALL call `XACK` for that message's id before reading the next entry

#### Scenario: Failing handler is logged loudly but still acked
- **WHEN** the handler returns a non-nil error for a message
- **THEN** `ConsumeStream` SHALL log at ERROR with the correlation_id and error, SHALL increment the `HandlerErrors` counter, and SHALL still call `XACK` for that message — the queue MUST NOT stall on a single poisonous message. `XREADGROUP >` does not redeliver PEL entries, so leaving messages unacked achieves nothing useful beyond head-of-line blocking.

### Requirement: Consumer-group recovery on NOGROUP
The `ConsumeStream` helper SHALL detect when the consumer group no longer exists (Valkey returns `NOGROUP` or "no such key") and SHALL recreate the group and continue, without requiring a service restart.

#### Scenario: NOGROUP after FLUSHDB
- **WHEN** `FLUSHDB` is executed against the Valkey instance while `ConsumeStream` is running
- **AND** a new message is subsequently published to the stream
- **THEN** `ConsumeStream` SHALL log at WARN with `msg: "consumer_group_missing_recovering"`, call `EnsureGroup` to recreate the consumer group, and SHALL process the new message via the handler

#### Scenario: Repeated recovery does not spam logs
- **WHEN** NOGROUP errors occur repeatedly within a single 10-second window
- **THEN** only the first recovery in that window SHALL be logged at WARN; subsequent ones SHALL be logged at DEBUG

### Requirement: Error classification for ReadGroup results
The `ConsumeStream` helper SHALL classify errors returned from `XReadGroup` into three buckets and act on each distinctly.

#### Scenario: redis.Nil is a normal idle
- **WHEN** `XReadGroup` returns `redis.Nil` (block elapsed with no new messages)
- **THEN** `ConsumeStream` SHALL continue the loop without logging

#### Scenario: Unknown error triggers backoff and warning
- **WHEN** `XReadGroup` returns any error other than `redis.Nil` or NOGROUP
- **THEN** `ConsumeStream` SHALL log at WARN with `msg: "consume_stream_error"` and the error text, sleep for an exponential backoff (starts 100ms, caps 5s, resets on the next successful read), and continue

#### Scenario: Consecutive unknown errors escalate to ERROR
- **WHEN** `XReadGroup` has returned the same unknown error class N times in a row (default N=10)
- **THEN** `ConsumeStream` SHALL additionally log at ERROR with `msg: "consumer_loop_degraded"` so the degraded state surfaces to operators

### Requirement: Phantom-consume detection
The `ConsumeStream` helper SHALL periodically check whether the consumer group's `entries-read` count exceeds the local count of handler invocations and SHALL emit a WARN log when a persistent delta is detected, so that go-redis / Valkey interactions that silently advance the group without delivering messages to the handler are observable.

#### Scenario: Group-ahead-of-handler logged
- **WHEN** a second consecutive `XINFO GROUPS` check shows `entries-read` greater than the local handler-invocation count
- **THEN** `ConsumeStream` SHALL log at WARN with `msg: "phantom_consume_detected"` and fields `{stream, group, entries_read, handled_count, delta}`

#### Scenario: Transient delta does not fire warning
- **WHEN** a single check shows a delta but the next check shows handler has caught up
- **THEN** no `phantom_consume_detected` log SHALL fire

#### Scenario: Configurable check cadence
- **WHEN** the `CONSUME_PHANTOM_CHECK_S` environment variable is set
- **THEN** `ConsumeStream` SHALL use that value (in seconds) as the phantom-check interval, defaulting to 30 when unset

### Requirement: Observable consume stats
The `ConsumeStream` helper SHALL accept an optional stats pointer that tracks handler invocations, handler errors, recoveries, and phantom-consume events, and SHALL emit a periodic INFO snapshot for operators.

#### Scenario: Stats snapshot emitted periodically
- **WHEN** the phantom-check interval fires
- **THEN** `ConsumeStream` SHALL log at INFO with `msg: "consume_stream_stats"` and fields `{stream, group, handled, handler_errors, recoveries, phantom_logs}`

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
