## ADDED Requirements

### Requirement: SlotAssignedAck message type
The inter-service messaging schema SHALL define a `SlotAssignedAck` message type used by agents to confirm receipt of a slot assignment reply, enabling the broker's two-phase slot-assignment protocol.

#### Scenario: Ack message schema
- **WHEN** an agent publishes a `SlotAssignedAck` message
- **THEN** the message SHALL have `type: "slot_assigned_ack"`, its `correlation_id` SHALL match the original slot request, and the payload SHALL include `agent_id` (string) and `slot_id` (int)

#### Scenario: Ack routed on the broker inbound stream
- **WHEN** an agent publishes a `SlotAssignedAck`
- **THEN** it SHALL be published to `stream:broker:slot-requests` so the broker's existing slot-request consumer handles it
