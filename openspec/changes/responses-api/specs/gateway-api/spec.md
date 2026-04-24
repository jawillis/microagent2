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
