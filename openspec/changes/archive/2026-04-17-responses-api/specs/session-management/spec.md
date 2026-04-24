## MODIFIED Requirements

### Requirement: Hybrid session ID strategy
The gateway SHALL associate sessions with response chains. When a Responses API request has no `previous_response_id`, the gateway SHALL create a new session and associate it with the response. When a request has a `previous_response_id`, the gateway SHALL inherit the session ID from the previous response. For chat/completions requests, the gateway SHALL accept an optional `session_id` in the request body (if provided, associate the response with that session; if omitted, create a new session).

#### Scenario: New response chain creates session
- **WHEN** a Responses API request has no `previous_response_id`
- **THEN** the gateway SHALL create a new session with a generated UUIDv4 and associate the response with it

#### Scenario: Continued chain inherits session
- **WHEN** a Responses API request includes a valid `previous_response_id`
- **THEN** the gateway SHALL read the `session_id` from the previous response and assign the same session to the new response

#### Scenario: Chat/completions with client-provided session_id
- **WHEN** a chat/completions request includes a `session_id` field
- **THEN** the gateway SHALL associate the response with the provided session ID

#### Scenario: Chat/completions without session_id
- **WHEN** a chat/completions request omits the `session_id` field
- **THEN** the gateway SHALL create a new session and associate the response with it

### Requirement: Session ID returned in response
The gateway SHALL return the session ID in the `X-Session-ID` response header for all responses (both Responses API and chat/completions). For chat/completions responses, the `session_id` field SHALL also be included in the response body. For Responses API responses, the `session_id` SHALL be included as a field in the response object.

#### Scenario: Responses API includes session_id
- **WHEN** a Responses API response is returned (streaming or non-streaming)
- **THEN** the response object SHALL include a `session_id` field and the `X-Session-ID` header SHALL be set

#### Scenario: Chat/completions includes session_id
- **WHEN** a chat/completions response is returned
- **THEN** the response body SHALL include a `session_id` field and the `X-Session-ID` header SHALL be set

#### Scenario: Streaming response includes session_id header
- **WHEN** a streaming response begins (either API format)
- **THEN** the `X-Session-ID` header SHALL be set before the first SSE chunk is sent

### Requirement: View session history
The system SHALL provide an API endpoint `GET /v1/sessions/:id` that returns the full conversation history for a session by reading the session's response chain.

#### Scenario: Session exists with responses
- **WHEN** a GET request is made to `/v1/sessions/:id` and the session has stored responses
- **THEN** the response SHALL contain the session ID and the conversation reconstructed from the session's response list (all input and output items from all responses in chronological order)

#### Scenario: Session does not exist
- **WHEN** a GET request is made to `/v1/sessions/:id` and no session with that ID exists
- **THEN** the response SHALL return HTTP 404

### Requirement: Delete session
The system SHALL provide an API endpoint `DELETE /v1/sessions/:id` that removes a session and all its associated responses from Valkey.

#### Scenario: Delete existing session
- **WHEN** a DELETE request is made to `/v1/sessions/:id` and the session exists
- **THEN** the system SHALL delete all response hashes referenced by the session's response list, delete the session response list key, delete the session metadata, and return HTTP 200

#### Scenario: Delete non-existent session
- **WHEN** a DELETE request is made to `/v1/sessions/:id` and no session exists
- **THEN** the response SHALL return HTTP 404

### Requirement: List sessions
The system SHALL provide an API endpoint `GET /v1/sessions` that returns all active sessions with their ID, turn count, and last activity timestamp.

#### Scenario: Sessions exist
- **WHEN** a GET request is made to `/v1/sessions` and sessions exist
- **THEN** the response SHALL be a JSON array of session objects containing `session_id`, `turn_count` (number of responses in the session), and `last_active` (timestamp of the most recent response)

#### Scenario: No sessions exist
- **WHEN** a GET request is made to `/v1/sessions` and no sessions exist
- **THEN** the response SHALL be an empty JSON array
