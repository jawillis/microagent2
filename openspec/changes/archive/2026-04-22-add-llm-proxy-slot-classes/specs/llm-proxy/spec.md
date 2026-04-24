## ADDED Requirements

### Requirement: OpenAI-compatible chat completions endpoint
The llm-proxy service SHALL expose an HTTP endpoint at `POST /v1/chat/completions` that accepts OpenAI-compatible chat completion requests (both streaming and non-streaming) and returns OpenAI-compatible responses.

#### Scenario: Non-streaming chat completion
- **WHEN** a client POSTs a JSON body with `"stream": false` to `/v1/chat/completions`
- **THEN** llm-proxy SHALL acquire a hindsight-class slot from llm-broker, forward the request via the broker-proxied LLM request flow, and return a single JSON response body matching the OpenAI chat completions response shape

#### Scenario: Streaming chat completion
- **WHEN** a client POSTs a JSON body with `"stream": true` to `/v1/chat/completions`
- **THEN** llm-proxy SHALL acquire a hindsight-class slot, forward the request, and stream the response back as Server-Sent Events with a terminating `data: [DONE]` line, matching OpenAI's SSE format

#### Scenario: Request with tools
- **WHEN** a client POSTs a chat completion request with a non-empty `tools` array
- **THEN** llm-proxy SHALL forward `tools` and `tool_choice` through the broker to llama-server without modification, and SHALL return assembled `tool_calls` in the response (non-streaming) or SSE deltas (streaming)

### Requirement: Slot acquisition per HTTP request
The llm-proxy service SHALL acquire a hindsight-class slot from llm-broker at the start of each incoming HTTP request and SHALL release the slot when the response completes or the client disconnects.

#### Scenario: Slot acquired before forwarding
- **WHEN** an HTTP request arrives at `/v1/chat/completions`
- **THEN** llm-proxy SHALL publish a `SlotRequestPayload` with `slot_class: "hindsight"` and its own proxy identity, wait for a `SlotAssignedPayload`, and only then forward the request to llm-broker's LLM-request stream

#### Scenario: Slot released on response completion
- **WHEN** the upstream response completes (streaming `[DONE]` or non-streaming body end)
- **THEN** llm-proxy SHALL publish a `SlotReleasePayload` with the slot_id it held

#### Scenario: Slot released on client disconnect
- **WHEN** the HTTP client disconnects before the upstream response completes
- **THEN** llm-proxy SHALL publish a `SlotReleasePayload` for the held slot and cease forwarding

#### Scenario: Slot acquisition timeout
- **WHEN** llm-proxy waits longer than `LLM_PROXY_SLOT_TIMEOUT_MS` (default 10000) for a `SlotAssignedPayload`
- **THEN** llm-proxy SHALL publish a defensive `SlotReleasePayload` with `slot_id: -1` and its own identity, return HTTP 503 to the client with a structured error, and log at WARN

### Requirement: Stable proxy identity for broker ownership validation
The llm-proxy service SHALL publish slot and LLM requests with a stable `agent_id` value for the lifetime of the process, so that llm-broker's slot-ownership validation treats llm-proxy as a single slot-owning identity.

#### Scenario: Proxy identity from environment
- **WHEN** llm-proxy starts
- **THEN** it SHALL read `LLM_PROXY_IDENTITY` from environment (default `llm-proxy`) and use that value as the `agent_id` on all published slot and LLM request messages

#### Scenario: Proxy identity stable across requests
- **WHEN** llm-proxy handles multiple concurrent HTTP requests
- **THEN** every outbound slot and LLM request payload SHALL carry the same `agent_id` value

### Requirement: Broker-mediated request forwarding
The llm-proxy service SHALL forward LLM requests through llm-broker's `stream:broker:llm-requests` channel rather than calling llama-server directly.

#### Scenario: LLM request published to broker
- **WHEN** llm-proxy holds an assigned slot and is ready to forward a chat completion
- **THEN** it SHALL publish an `LLMRequestPayload` to the broker's LLM-request stream with the held `slot_id`, the translated `messages`, `stream`, `tools`, and `tool_choice`, and SHALL NOT make any direct HTTP call to llama-server

#### Scenario: Response reassembled from broker reply
- **WHEN** the broker publishes tokens, tool calls, and the terminal done token on llm-proxy's reply stream
- **THEN** llm-proxy SHALL reassemble those messages into the OpenAI-compatible HTTP response shape (SSE for streaming, single JSON for non-streaming) and deliver it to the HTTP client

### Requirement: Health endpoint
The llm-proxy service SHALL expose a health endpoint at `GET /health` that returns HTTP 200 when the service is operational.

#### Scenario: Health check returns 200
- **WHEN** a client GETs `/health`
- **THEN** llm-proxy SHALL return HTTP 200 with a JSON body `{"status": "ok"}`

### Requirement: Configurable HTTP listen address
The llm-proxy service SHALL read its HTTP listen address from `LLM_PROXY_HTTP_ADDR` at startup.

#### Scenario: Listen address from environment
- **WHEN** llm-proxy starts
- **THEN** it SHALL read `LLM_PROXY_HTTP_ADDR` (default `:8082`) and bind its HTTP server to that address
