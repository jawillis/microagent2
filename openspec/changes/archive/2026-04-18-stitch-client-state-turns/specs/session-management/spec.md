## ADDED Requirements

### Requirement: Session lifecycle includes stitching extensions
A session's response list MAY be extended either by explicit `previous_response_id` chaining (an existing capability) or by server-side stitching of client-side-state replays (this capability). The two mechanisms SHALL produce session records that are behaviorally indistinguishable to downstream consumers such as the retro-agent, the dashboard, and the memory pipeline.

#### Scenario: Retro-agent sees stitched sessions as multi-turn
- **WHEN** the retro-agent reads a session's messages via `response.Store.GetSessionMessages(session_id)` for a session that was built via stitching over N turns
- **THEN** it SHALL receive the full N-turn message history in order, identical to what it would receive if the same N turns had been produced via explicit `previous_response_id` chaining

#### Scenario: Dashboard session list counts stitched sessions correctly
- **WHEN** the dashboard renders the session list
- **THEN** a stitched session SHALL appear as a single row with a turn count equal to its response-list length, not as N separate rows

### Requirement: Stitching index lifetime bounds session resurrection
A stitched session SHALL remain extendable by subsequent turns only while its most recent prefix hash entry lives in the index. Once that entry expires, a subsequent client replay of the same conversation SHALL mint a new session id. This bounds the window during which an abandoned conversation can be accidentally "revived" server-side.

#### Scenario: Extension within TTL window
- **WHEN** the client sends turn N+1 of a conversation within the configured TTL of turn N's completion
- **THEN** the gateway SHALL stitch turn N+1 into the same session as turn N

#### Scenario: Extension past TTL window
- **WHEN** the client sends turn N+1 of a conversation after the configured TTL has elapsed since turn N's completion
- **THEN** the gateway SHALL mint a new session id for turn N+1 and SHALL NOT extend the original session
