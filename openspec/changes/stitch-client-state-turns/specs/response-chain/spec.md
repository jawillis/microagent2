## ADDED Requirements

### Requirement: Session prefix hash index
The response store SHALL expose a hash-keyed index that maps a canonical hash of a conversation's message prefix to the session id that owns that conversation. The index is the mechanism by which the gateway stitches repeated client-side-state replays into a single growing session.

#### Scenario: Canonical hash function
- **WHEN** the response store or gateway computes the index key for a list of input items
- **THEN** the key SHALL be derived by (a) flattening each item's `content` to plain text using the same rules the gateway uses to feed the LLM, (b) emitting a canonical byte form in order of items as `<lowercased-role> "\x00" <flattened-content> "\x1e"`, skipping items whose flattened content is empty, and (c) computing the SHA-256 over those bytes. The stored Valkey key SHALL be `session_hash:<lowercase-hex-digest>`.

#### Scenario: Write after response storage
- **WHEN** a response is stored to a session via the Responses API stitching path
- **THEN** the response store SHALL write `session_hash:<hex>` → `<session_id>` with an expiry set from the configured TTL, where `<hex>` is the canonical hash of the entire conversation (prior messages plus the newly stored assistant turn)

#### Scenario: Lookup by prefix hash
- **WHEN** the gateway's stitching path queries the index for a given prefix hash
- **THEN** the response store SHALL return `(session_id, true)` if a key for that hash exists, or `("", false)` otherwise. Read errors SHALL be surfaced, not silently masked.

#### Scenario: TTL refresh on active conversations
- **WHEN** an active conversation receives a new turn via the stitching path
- **THEN** the post-storage index write SHALL refresh the TTL for the newly-computed hash. The prior hash entry (matching the old prefix) SHALL be left alone to age out naturally.

#### Scenario: Configurable TTL
- **WHEN** the response store initializes
- **THEN** it SHALL read `SESSION_HASH_TTL_HOURS` from the environment, defaulting to 24, and SHALL apply that TTL on every index write

#### Scenario: store=false skips index write
- **WHEN** a response is processed with `store: false`
- **THEN** no `session_hash:*` index entry SHALL be written

### Requirement: Stitched sessions preserve the response chain
A response appended to an existing session via the stitching path SHALL have its `previous_response_id` set to the id of that session's most recent prior response. The stitched session's response chain SHALL be traversable via `WalkChain` from the most recent response exactly as if the client had used explicit `previous_response_id` chaining.

#### Scenario: Stitched response links to previous response
- **WHEN** the gateway stitches a new response into an existing session
- **THEN** it SHALL call `response.Store.GetLastResponseID(session_id)` and set the new response's `previous_response_id` to that value before storage

#### Scenario: Stitched session is walkable
- **WHEN** `WalkChain` is invoked with the most recent response of a stitched N-turn session
- **THEN** it SHALL return all N responses in chronological order
