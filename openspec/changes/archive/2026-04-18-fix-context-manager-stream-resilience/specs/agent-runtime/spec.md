## ADDED Requirements

### Requirement: Agent worker loops use resilient consume
Agent main loops (main-agent and retro-agent) SHALL read their inbound streams via the resilient `ConsumeStream` helper rather than hand-rolled `for { ReadGroup ... if err != nil continue }` loops, so that consumer-group disruption and other recoverable stream errors do not silently stall agent processing.

#### Scenario: Main-agent context-assembled stream uses ConsumeStream
- **WHEN** main-agent starts
- **THEN** it SHALL read from `stream:agent:main-agent:requests` using `ConsumeStream`

#### Scenario: Retro-agent trigger stream uses ConsumeStream
- **WHEN** retro-agent starts
- **THEN** it SHALL read from `stream:retro:triggers` using `ConsumeStream`

#### Scenario: Agent loop recovers from FLUSHDB
- **WHEN** the agent's consumer group is lost (e.g. `FLUSHDB`)
- **AND** a new message is subsequently published to the agent's inbound stream
- **THEN** the agent SHALL process that message without requiring a restart
