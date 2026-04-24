## ADDED Requirements

### Requirement: Responses API endpoint
The gateway SHALL expose a `POST /v1/responses` endpoint that accepts a request body with fields: `input` (string or array of input items), `model` (string), `previous_response_id` (optional string), `tools` (optional array of tool definitions), `tool_choice` (optional string or object), `store` (optional boolean, default true), and `stream` (optional boolean, default false).

#### Scenario: Simple text input
- **WHEN** a POST request is made to `/v1/responses` with `input` as a string
- **THEN** the gateway SHALL treat it as a single user message, create a response object, route through the Responses pipeline, and return the response with a unique `id`

#### Scenario: Structured input items
- **WHEN** a POST request is made to `/v1/responses` with `input` as an array of items (each with `role` and `content`)
- **THEN** the gateway SHALL use the items as the conversation input for this turn

#### Scenario: Chained response
- **WHEN** a POST request includes a valid `previous_response_id`
- **THEN** the gateway SHALL resolve the response chain, reconstruct the prior conversation history, append the new input, and route through the pipeline with full context

#### Scenario: Invalid previous_response_id
- **WHEN** a POST request includes a `previous_response_id` that does not correspond to a stored response
- **THEN** the gateway SHALL return HTTP 400 with an error message indicating the referenced response was not found

#### Scenario: Streaming response
- **WHEN** a POST request to `/v1/responses` includes `stream: true`
- **THEN** the gateway SHALL return an SSE stream of response events, with the final event containing the complete response object

#### Scenario: Non-streaming response
- **WHEN** a POST request to `/v1/responses` includes `stream: false` or omits the field
- **THEN** the gateway SHALL return the complete response object as a single JSON body after generation completes

#### Scenario: Tools carried in request
- **WHEN** a POST request includes `tools` and/or `tool_choice` fields
- **THEN** the gateway SHALL store these on the response object and include them when assembling the LLM request context. The gateway SHALL NOT execute tools in this change.

### Requirement: Response retrieval endpoint
The gateway SHALL expose a `GET /v1/responses/:id` endpoint that returns a stored response object by its ID.

#### Scenario: Response exists
- **WHEN** a GET request is made to `/v1/responses/:id` and the response exists
- **THEN** the gateway SHALL return the full response object as JSON

#### Scenario: Response not found
- **WHEN** a GET request is made to `/v1/responses/:id` and no response exists with that ID
- **THEN** the gateway SHALL return HTTP 404

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

### Requirement: Structured per-request logging
The gateway SHALL emit structured INFO log lines at every significant hand-off for each incoming request, tagged with the request's `correlation_id`, so the lifecycle of a single turn can be reconstructed from `docker compose logs`.

#### Scenario: Request received
- **WHEN** the gateway receives a request on `POST /v1/responses` or `POST /v1/chat/completions`
- **THEN** it SHALL log at INFO with `msg: "gateway_request_received"` and fields `{correlation_id, path, session_id, previous_response_id, stream, input_items}` (previous_response_id omitted when empty)

#### Scenario: Request published to gateway stream
- **WHEN** the gateway publishes the request to `stream:gateway:requests`
- **THEN** it SHALL log at INFO with `msg: "gateway_request_published"` and fields `{correlation_id, session_id}`

#### Scenario: Streaming subscribe ready
- **WHEN** the gateway subscribes to `channel:tokens:{session_id}` for a streaming response
- **THEN** it SHALL log at INFO with `msg: "gateway_stream_subscribed"` and fields `{correlation_id, session_id}`

#### Scenario: First token observed
- **WHEN** the gateway receives the first token on the tokens channel for a streaming request
- **THEN** it SHALL log at INFO with `msg: "gateway_stream_first_token"` and fields `{correlation_id, elapsed_ms_since_published}`

#### Scenario: Request completed
- **WHEN** the gateway emits the final `response.completed` event (streaming) or returns the response body (non-streaming)
- **THEN** it SHALL log at INFO with `msg: "gateway_request_completed"` and fields `{correlation_id, session_id, response_id, elapsed_ms}`

#### Scenario: Request timed out
- **WHEN** the gateway returns a 504 due to `WaitForReply` timeout, or the client disconnects before completion
- **THEN** it SHALL log at WARN with `msg: "gateway_request_timeout"` or `msg: "gateway_client_disconnected"` and fields `{correlation_id, session_id, elapsed_ms}`

### Requirement: Dashboard and new API routes
The gateway SHALL serve the dashboard static files at `GET /` and register routes for `GET /v1/sessions`, `GET /v1/sessions/:id`, `DELETE /v1/sessions/:id`, `POST /v1/retro/:session/trigger`, and `GET /v1/status`.

#### Scenario: Dashboard route does not conflict with API
- **WHEN** a GET request is made to `/`
- **THEN** the gateway SHALL serve the dashboard HTML
- **WHEN** a POST request is made to `/v1/chat/completions`
- **THEN** the gateway SHALL handle it as before with the new session ID behavior

## MODIFIED Requirements

### Requirement: Chat completions endpoint
The gateway SHALL expose a `POST /v1/chat/completions` endpoint that accepts OpenAI-compatible chat completion requests. The gateway SHALL internally translate the request into a single-turn Responses API call: the `messages` array becomes the `input` items, no `previous_response_id` is set, and the response is stored as a single-response chain with its own session. The gateway SHALL translate the internal response object back into the chat/completions response format before returning to the client. The `session_id` field is still accepted if provided by the client; if omitted, a new session is created.

#### Scenario: Chat completion routed through Responses pipeline
- **WHEN** a POST request is made to `/v1/chat/completions` with a valid messages array
- **THEN** the gateway SHALL convert messages to input items, create a response via the Responses pipeline (no previous_response_id), and translate the response back to chat/completions format

#### Scenario: Chat completion with client-provided session
- **WHEN** a POST request is made to `/v1/chat/completions` with a `session_id` field
- **THEN** the gateway SHALL associate the response with the provided session ID rather than generating a new one

#### Scenario: Chat completion without session ID
- **WHEN** a POST request is made to `/v1/chat/completions` with no `session_id` field
- **THEN** the gateway SHALL generate a new session and associate the response with it

#### Scenario: Streaming response with session ID
- **WHEN** a streaming chat completion is requested
- **THEN** the `X-Session-ID` header SHALL be set before the first SSE chunk is sent

#### Scenario: Request timeout from config
- **WHEN** a non-streaming request is waiting for a response
- **THEN** the gateway SHALL use the `request_timeout_s` value from the config store (default 120) as the timeout duration
