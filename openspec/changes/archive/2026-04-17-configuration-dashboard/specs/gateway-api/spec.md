## MODIFIED Requirements

### Requirement: Chat completions endpoint
The gateway SHALL expose a `POST /v1/chat/completions` endpoint that accepts OpenAI-compatible chat completion requests. The request body MAY include an optional `session_id` field. If `session_id` is provided, the gateway SHALL use it. If omitted, the gateway SHALL generate a UUIDv4. The gateway SHALL set the `X-Session-ID` response header and include `session_id` in the response body for both streaming and non-streaming responses.

#### Scenario: Chat completion with client-provided session
- **WHEN** a POST request is made to `/v1/chat/completions` with a valid messages array and a `session_id` field
- **THEN** the gateway SHALL use the provided session ID, publish the request to the gateway requests stream, and return the response with the session ID in the `X-Session-ID` header and response body

#### Scenario: Chat completion without session ID
- **WHEN** a POST request is made to `/v1/chat/completions` with a valid messages array and no `session_id` field
- **THEN** the gateway SHALL generate a UUIDv4 session ID, publish the request, and return the response with the generated session ID in the `X-Session-ID` header and response body

#### Scenario: Streaming response with session ID
- **WHEN** a streaming chat completion is requested
- **THEN** the `X-Session-ID` header SHALL be set before the first SSE chunk is sent

#### Scenario: Request timeout from config
- **WHEN** a non-streaming request is waiting for a response
- **THEN** the gateway SHALL use the `request_timeout_s` value from the config store (default 120) as the timeout duration

## ADDED Requirements

### Requirement: Config API endpoints
The gateway SHALL expose `GET /v1/config` to read all config sections and `PUT /v1/config` to update a config section. The PUT request body SHALL contain a `section` field (one of `chat`, `memory`, `broker`, `retro`) and a `values` field with the key-value pairs to write.

#### Scenario: Read all config
- **WHEN** a GET request is made to `/v1/config`
- **THEN** the response SHALL be a JSON object with keys `chat`, `memory`, `broker`, and `retro`, each containing the current effective config values

#### Scenario: Update config section
- **WHEN** a PUT request is made to `/v1/config` with `{"section": "chat", "values": {"system_prompt": "New prompt"}}`
- **THEN** the config store SHALL update the `config:chat` key in Valkey with the provided values

#### Scenario: Invalid config section
- **WHEN** a PUT request is made with an unrecognized section name
- **THEN** the gateway SHALL return HTTP 400

### Requirement: Dashboard and new API routes
The gateway SHALL serve the dashboard static files at `GET /` and register routes for `GET /v1/sessions`, `GET /v1/sessions/:id`, `DELETE /v1/sessions/:id`, `POST /v1/retro/:session/trigger`, and `GET /v1/status`.

#### Scenario: Dashboard route does not conflict with API
- **WHEN** a GET request is made to `/`
- **THEN** the gateway SHALL serve the dashboard HTML
- **WHEN** a POST request is made to `/v1/chat/completions`
- **THEN** the gateway SHALL handle it as before with the new session ID behavior
