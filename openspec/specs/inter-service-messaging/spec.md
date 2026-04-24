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
