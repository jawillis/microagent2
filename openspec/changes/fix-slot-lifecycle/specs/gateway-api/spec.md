## ADDED Requirements

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
