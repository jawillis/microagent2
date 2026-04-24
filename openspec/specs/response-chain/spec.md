## ADDED Requirements

### Requirement: Response object storage
The gateway SHALL store each response as a Valkey hash at key `response:{response_id}` containing fields: `id`, `input` (JSON array of input items), `output` (JSON array of output items), `previous_response_id` (string or null), `session_id`, `model`, `created_at` (ISO 8601 timestamp), and `status` (one of `completed`, `failed`, `in_progress`).

#### Scenario: Response stored after completion
- **WHEN** the agent completes a generation for a Responses API request
- **THEN** the gateway SHALL store the response hash with all fields populated, `status` set to `completed`, and output containing the generated message items

#### Scenario: Response stored with previous_response_id
- **WHEN** a Responses API request includes `previous_response_id`
- **THEN** the stored response hash SHALL include the provided `previous_response_id` value, linking it to the prior response in the chain

#### Scenario: Response stored without previous_response_id
- **WHEN** a Responses API request has no `previous_response_id`
- **THEN** the stored response hash SHALL have `previous_response_id` set to null, marking it as the root of a new chain

### Requirement: Response ID generation
The gateway SHALL generate a unique response ID for each response using the format `resp_{ulid}` where `{ulid}` is a ULID (Universally Unique Lexicographically Sortable Identifier).

#### Scenario: Unique ID assigned
- **WHEN** a new response is created
- **THEN** the gateway SHALL generate a `resp_`-prefixed ULID as the response ID, store it in the response hash `id` field, and return it in the API response

### Requirement: Response chain traversal
The gateway SHALL reconstruct a conversation by walking the response chain backward from a given response ID, collecting input and output items from each response in the chain, and returning them in chronological order.

#### Scenario: Single-response chain
- **WHEN** the gateway resolves a chain for a response with no `previous_response_id`
- **THEN** the result SHALL contain only that response's input and output items

#### Scenario: Multi-response chain
- **WHEN** the gateway resolves a chain for a response whose `previous_response_id` points to another response, which in turn has its own `previous_response_id`, etc.
- **THEN** the gateway SHALL walk the chain to the root and return all input/output items from all responses in chronological order (oldest first)

#### Scenario: Broken chain
- **WHEN** the gateway encounters a `previous_response_id` that does not correspond to any stored response
- **THEN** the gateway SHALL return an error to the client with HTTP 400 and a message indicating the referenced response was not found

### Requirement: Session index maintenance
The gateway SHALL maintain a Valkey list at key `session:{session_id}:responses` that tracks all response IDs belonging to a session in creation order.

#### Scenario: Response appended to session index
- **WHEN** a response is stored
- **THEN** the gateway SHALL RPUSH the response ID to the session's response list

#### Scenario: Session index used for history retrieval
- **WHEN** a consumer requests the full history for a session
- **THEN** the system SHALL read the response ID list from `session:{session_id}:responses` and batch-read the corresponding response hashes

### Requirement: Response retrieval by ID
The gateway SHALL support retrieving a stored response by its ID.

#### Scenario: Response exists
- **WHEN** a GET request is made for a specific response ID
- **THEN** the gateway SHALL return the full response object from the Valkey hash

#### Scenario: Response not found
- **WHEN** a GET request is made for a response ID that does not exist
- **THEN** the gateway SHALL return HTTP 404

### Requirement: Response output item types
The response output items array SHALL support item types: `message` (assistant text response), `function_call` (tool invocation request with call_id, name, and arguments), and `function_call_output` (tool result with call_id and output). Additional item types MAY be added in future changes.

#### Scenario: Message output item
- **WHEN** the LLM generates a text response
- **THEN** the output SHALL contain an item with `type: "message"`, `role: "assistant"`, and a `content` array containing text content blocks

#### Scenario: Function call output item
- **WHEN** the LLM generates a tool call (future use)
- **THEN** the output SHALL contain an item with `type: "function_call"`, `call_id`, `name`, and `arguments` (JSON string)

#### Scenario: Function call output result item
- **WHEN** a tool result is provided as input (future use)
- **THEN** the input SHALL contain an item with `type: "function_call_output"`, `call_id`, and `output` (string)

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
