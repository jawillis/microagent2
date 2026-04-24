## ADDED Requirements

### Requirement: Hybrid session ID strategy
The gateway SHALL support an optional `session_id` field in the chat completions request body. If the client provides a `session_id`, the gateway SHALL use it. If the client omits it, the gateway SHALL generate a UUIDv4 as the session ID.

#### Scenario: Client provides session_id
- **WHEN** a chat completions request includes a `session_id` field
- **THEN** the gateway SHALL use the provided value as the session ID for context management

#### Scenario: Client omits session_id
- **WHEN** a chat completions request does not include a `session_id` field
- **THEN** the gateway SHALL generate a UUIDv4 and use it as the session ID

### Requirement: Session ID returned in response
The gateway SHALL return the session ID to the client in both the `X-Session-ID` response header and in the response body as a `session_id` field. This applies to both streaming and non-streaming responses.

#### Scenario: Non-streaming response includes session_id
- **WHEN** a non-streaming chat completion response is sent
- **THEN** the response body SHALL include a `session_id` field and the `X-Session-ID` header SHALL be set

#### Scenario: Streaming response includes session_id header
- **WHEN** a streaming chat completion response begins
- **THEN** the `X-Session-ID` header SHALL be set before the first SSE chunk is sent

### Requirement: List sessions
The system SHALL provide an API endpoint `GET /v1/sessions` that returns all active sessions with their ID, turn count, and last activity timestamp.

#### Scenario: Sessions exist
- **WHEN** a GET request is made to `/v1/sessions` and session history keys exist in Valkey
- **THEN** the response SHALL be a JSON array of session objects containing `session_id`, `turn_count`, and `last_active`

#### Scenario: No sessions exist
- **WHEN** a GET request is made to `/v1/sessions` and no session history keys exist
- **THEN** the response SHALL be an empty JSON array

### Requirement: View session history
The system SHALL provide an API endpoint `GET /v1/sessions/:id` that returns the full chat history for a session.

#### Scenario: Session exists
- **WHEN** a GET request is made to `/v1/sessions/:id` and the session exists
- **THEN** the response SHALL contain the session ID and an array of chat messages with role and content

#### Scenario: Session does not exist
- **WHEN** a GET request is made to `/v1/sessions/:id` and the session does not exist
- **THEN** the response SHALL return HTTP 404

### Requirement: Delete session
The system SHALL provide an API endpoint `DELETE /v1/sessions/:id` that removes a session's history from Valkey.

#### Scenario: Delete existing session
- **WHEN** a DELETE request is made to `/v1/sessions/:id` and the session exists
- **THEN** the session history key SHALL be removed from Valkey and the response SHALL return HTTP 200

#### Scenario: Delete non-existent session
- **WHEN** a DELETE request is made to `/v1/sessions/:id` and the session does not exist
- **THEN** the response SHALL return HTTP 404
