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
