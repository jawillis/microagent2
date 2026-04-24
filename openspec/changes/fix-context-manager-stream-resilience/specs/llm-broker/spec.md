## ADDED Requirements

### Requirement: Broker consumers use resilient consume
The llm-broker SHALL read `stream:broker:slot-requests` and `stream:broker:llm-requests` via the resilient `ConsumeStream` helper rather than hand-rolled `for { ReadGroup ... if err != nil continue }` loops, so that consumer-group disruption does not silently stall slot allocation or LLM proxying.

#### Scenario: Slot-request consumer uses ConsumeStream
- **WHEN** the broker starts
- **THEN** its slot-request consumer loop SHALL use `ConsumeStream` on `stream:broker:slot-requests`

#### Scenario: LLM-request consumer uses ConsumeStream
- **WHEN** the broker starts
- **THEN** its LLM-request consumer loop SHALL use `ConsumeStream` on `stream:broker:llm-requests`

#### Scenario: Broker recovers from FLUSHDB without slot-table corruption
- **WHEN** the Valkey consumer group for broker slot-requests is lost (e.g. `FLUSHDB`)
- **AND** a new slot request is subsequently published
- **THEN** the broker SHALL process that request, and the in-memory slot table SHALL remain consistent (no double-assignment on resumption)
