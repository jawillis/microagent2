## ADDED Requirements

### Requirement: Responses API endpoint
The gateway SHALL expose a `POST /v1/responses` endpoint that accepts a request body with fields: `input` (string or array of input items), `model` (string), `previous_response_id` (optional string), `tools` (optional array of tool definitions), `tool_choice` (optional string or object), `store` (optional boolean, default true), `stream` (optional boolean, default false), and `speaker_id` (optional string identifying the person who uttered this turn). The gateway SHALL also accept an `X-Speaker-ID` header as a fallback when the body field is absent. Speaker resolution order is: body `speaker_id` → `X-Speaker-ID` header → previous-turn speaker (when `previous_response_id` is set) → `config:memory.primary_user_id` → `"unknown"`. When the request omits `previous_response_id`, has `store` true-or-absent, and provides a multi-message `input`, the gateway SHALL additionally consult the session-stitching index as defined in the "stitches client-side-state turns" requirement.

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

#### Scenario: Speaker_id from body
- **WHEN** a POST request includes `speaker_id="alice"` in the body
- **THEN** the gateway SHALL resolve the turn's speaker to `"alice"`, include it on the stored response object, propagate it through `ContextAssembledPayload`, and include it in the `X-Speaker-ID` response header

#### Scenario: Speaker_id from header only
- **WHEN** a POST request omits body `speaker_id` and includes `X-Speaker-ID: bob`
- **THEN** the gateway SHALL resolve the turn's speaker to `"bob"`

#### Scenario: Speaker_id inherited from previous turn
- **WHEN** a POST request has a valid `previous_response_id` whose stored response has `speaker_id="jason"` and the new request omits both body field and header
- **THEN** the gateway SHALL resolve the new turn's speaker to `"jason"`

#### Scenario: Speaker_id falls back to primary_user_id
- **WHEN** a POST request has no body field, no header, no previous-turn speaker, and `config:memory.primary_user_id="jason"`
- **THEN** the gateway SHALL resolve the turn's speaker to `"jason"`

#### Scenario: Speaker_id unknown
- **WHEN** none of the above sources are available
- **THEN** the gateway SHALL resolve the turn's speaker to `"unknown"` and log WARN with `msg="gateway_speaker_unknown"`

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
The gateway SHALL expose a `POST /v1/chat/completions` endpoint that accepts OpenAI-compatible chat completion requests, optionally extended with `session_id` and `speaker_id` fields. The gateway SHALL internally translate the request into a single-turn Responses API call: the `messages` array becomes the `input` items, no `previous_response_id` is set, and the response is stored as a single-response chain with its own session. The gateway SHALL translate the internal response object back into the chat/completions response format before returning to the client. The `session_id` field is still accepted if provided by the client; if omitted, a new session is created. `speaker_id` resolution follows the same precedence as on `/v1/responses`.

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

#### Scenario: Chat completion with speaker_id
- **WHEN** a POST request includes `speaker_id="alice"` in the body
- **THEN** the gateway SHALL resolve the turn's speaker to `"alice"` and include `X-Speaker-ID: alice` in the response headers

### Requirement: /v1/responses stitches client-side-state turns
The gateway SHALL stitch a `/v1/responses` request into an existing stored session when all of the following hold: (a) `previous_response_id` is absent or empty; (b) the parsed `input` contains more than one message; (c) `store` is absent or `true`. Stitching reuses a session id that the response store identifies by a stable hash of the replayed history prefix, so that a client operating in "client-side state" mode (replaying full history each turn without `previous_response_id`) nonetheless produces a single growing session server-side.

#### Scenario: Multi-turn client-side-state replay stitches into the same session
- **WHEN** a `/v1/responses` request arrives with `previous_response_id` absent, `store` absent-or-true, and an `input` array of ≥ 2 messages whose prefix (all messages except the last) matches the canonical hash of a previous turn's stored full conversation
- **THEN** the gateway SHALL reuse the session id from the matching index entry, SHALL set the new response's `previous_response_id` to the last response id of that session, and SHALL append the new response to the session's response list

#### Scenario: No matching prefix mints a fresh session
- **WHEN** a `/v1/responses` request matches the stitching trigger but no index entry is found for the computed prefix hash
- **THEN** the gateway SHALL mint a new session id (as it does today) and SHALL proceed with the stitching write-path so the new conversation will be matchable on the next turn

#### Scenario: Single-message input bypasses stitching
- **WHEN** a `/v1/responses` request arrives with `previous_response_id` absent and `input` containing exactly one message
- **THEN** the gateway SHALL NOT consult the stitching index and SHALL mint a new session id (today's behavior)

#### Scenario: Explicit previous_response_id bypasses stitching
- **WHEN** a `/v1/responses` request carries a non-empty `previous_response_id`
- **THEN** the gateway SHALL use the existing chain-walking path unchanged, SHALL NOT consult the stitching index, and SHALL NOT write to the stitching index

#### Scenario: store=false bypasses stitching
- **WHEN** a `/v1/responses` request carries `store: false`
- **THEN** the gateway SHALL NOT consult or write to the stitching index, regardless of whether the other trigger conditions hold

#### Scenario: Stitch decisions are observable in logs
- **WHEN** the stitching path fires and the index lookup hits
- **THEN** the gateway SHALL log at INFO with `msg: "stitch_matched"` and fields `{correlation_id, session_id, prefix_hash, previous_response_id}` where `prefix_hash` is the first 8 hex characters of the full SHA-256
- **WHEN** the stitching path fires and the index lookup misses
- **THEN** the gateway SHALL log at INFO with `msg: "stitch_minted"` and fields `{correlation_id, session_id, prefix_hash}`
- **WHEN** the post-storage index write returns an error
- **THEN** the gateway SHALL log at WARN with `msg: "stitch_index_write_failed"` and the error

### Requirement: Canonical session shape across stitching and chain walks
A session built via stitching SHALL be indistinguishable downstream from a session built via explicit `previous_response_id` chaining. Specifically, every stored response in a stitched session SHALL have `previous_response_id` populated (except the first, which is empty) such that `WalkChain` from the most recent response returns the complete session history in order.

#### Scenario: WalkChain from stitched session's last response
- **WHEN** a session has N responses created via the stitching path
- **THEN** `response.Store.WalkChain(lastResponseID)` SHALL return exactly those N responses in chronological order

## ADDED Requirements

### Requirement: Gateway collects tool-call events for storage only
For any streaming `/v1/responses` request, the gateway SHALL subscribe to `channel:tool-calls:{session_id}` alongside `channel:tokens:{session_id}` to collect internal tool-call events for persistence in the stored response, but SHALL NOT relay these events to the client as SSE events. The client didn't request tool calling (tools are server-configured built-ins); the internal agent loop's trace is an implementation detail hidden from the API surface.

#### Scenario: Client-facing SSE stream is text-only
- **WHEN** the gateway is streaming a `/v1/responses` turn whose agent loop invokes one or more tools
- **THEN** the SSE events emitted to the HTTP client SHALL be limited to `response.created`, `response.output_text.delta`, and `response.completed`; no `response.tool_call` or `response.tool_result` events SHALL reach the client

#### Scenario: Tool-call subscription used for storage
- **WHEN** the gateway receives a `TypeToolCall` or `TypeToolResult` message on `channel:tool-calls:{session_id}` during a streaming turn
- **THEN** it SHALL append the corresponding `function_call` or `function_call_output` `OutputItem` to an internal accumulator and SHALL persist the accumulator alongside the final assistant text when the turn completes

#### Scenario: Subscription lifecycle
- **WHEN** the gateway begins streaming a response
- **THEN** it SHALL subscribe to both `channel:tokens:{session_id}` and `channel:tool-calls:{session_id}` before emitting the first SSE event, and SHALL unsubscribe from both when the response completes or the client disconnects

### Requirement: Client-facing response body hides server-internal tool trace
For a non-streaming `/v1/responses` response, and for the `response.completed` SSE event in the streaming path, the gateway SHALL filter `function_call` and `function_call_output` items out of the `output` array before sending to the client. These items are retained in the stored response so the dashboard can render the full agentic trace.

#### Scenario: Tool trace stripped from client body
- **WHEN** an agent reply carries `ToolCalls` and `ToolResults` from a server-side tool loop
- **THEN** the non-streaming response body returned to the HTTP client SHALL contain only `message`-typed items in its `output` array, and SHALL NOT contain any `function_call` or `function_call_output` items

#### Scenario: Pure-text turns unaffected
- **WHEN** an agent reply carries no tool activity
- **THEN** the client-facing `output` array SHALL be byte-identical to the pre-change shape (a single `message` item)

#### Scenario: Storage retains full trace
- **WHEN** the gateway persists a response produced by a tool-executing turn
- **THEN** the stored `Response.Output` SHALL contain the interleaved `function_call` + `function_call_output` items followed by the assistant `message` item, even though the client-facing body omitted them

### Requirement: Gateway persists tool_calls and tool_results from the agent reply
For non-streaming `/v1/responses` requests, the gateway SHALL read `ChatResponsePayload.ToolCalls` and `ChatResponsePayload.ToolResults` from the agent's reply on the reply stream and persist them as interleaved `function_call` and `function_call_output` items in the **stored** response's `Output` array, preserving the pair ordering `function_call(c1), function_call_output(c1), function_call(c2), function_call_output(c2), ...` prior to the final assistant text message. These items are persistence-only — the client-facing response body has them stripped (see the slice-1 requirement "Client-facing response body hides server-internal tool trace").

#### Scenario: Non-streaming agentic turn stored with full trace
- **WHEN** the agent reply carries `ToolCalls = [c1, c2]` and `ToolResults = [{CallID: c1, Output: r1}, {CallID: c2, Output: r2}]` plus final text
- **THEN** the stored response's `Output` SHALL contain items in order: `function_call(c1)`, `function_call_output(c1,r1)`, `function_call(c2)`, `function_call_output(c2,r2)`, `message(assistant, final_text)`

#### Scenario: Pure-text turn unchanged
- **WHEN** the agent reply carries empty `ToolCalls` and `ToolResults`
- **THEN** the stored response's `Output` SHALL contain only the `message` item, byte-identical to the pre-change shape

### Requirement: Streaming gateway collects tool events for storage, not relay
For streaming responses, the gateway SHALL subscribe to `channel:tool-calls:{session_id}` and dispatch on the pub/sub message's `type` field. Both `TypeToolCall` and `TypeToolResult` events SHALL be appended to a per-turn `OutputItem` accumulator in arrival order. The gateway SHALL NOT forward these events to the client as SSE events. At stream completion, the accumulator is written into the stored response's `Output` array preceding the final assistant text item.

#### Scenario: Tool events not relayed as SSE
- **WHEN** the gateway receives a `TypeToolCall` or `TypeToolResult` message on `channel:tool-calls:{session_id}` during a streaming turn
- **THEN** no `response.tool_call` or `response.tool_result` SSE event SHALL be emitted to the HTTP client

#### Scenario: Streamed trace stored at stream completion
- **WHEN** the streaming turn receives, in order, a tool_call for `c1`, a tool_result for `c1`, and a terminal token_done
- **THEN** the persisted `Response.Output` SHALL contain `function_call(c1)`, `function_call_output(c1,...)`, and `message(assistant,...)` in that order

## ADDED Requirements

### Requirement: MCP servers CRUD endpoints
The gateway SHALL expose four endpoints for managing the MCP server list: `GET /v1/mcp/servers`, `PUT /v1/mcp/servers`, `POST /v1/mcp/servers`, `DELETE /v1/mcp/servers/:name`. All endpoints operate on the `config:mcp:servers` Valkey key.

#### Scenario: Read server list
- **WHEN** a GET request is made to `/v1/mcp/servers`
- **THEN** the gateway SHALL return `{"servers": [...]}` with the current stored list (may be empty)

#### Scenario: Replace server list
- **WHEN** a PUT request is made to `/v1/mcp/servers` with body `{"servers": [...]}`
- **THEN** the gateway SHALL validate every entry (name format, uniqueness, required fields), and on success write the full list to Valkey; on validation failure SHALL return 400 with an error describing the first invalid entry

#### Scenario: Append single server
- **WHEN** a POST request is made to `/v1/mcp/servers` with a single server object body
- **THEN** the gateway SHALL append it to the stored list; if a server with that name already exists, SHALL return 409 without mutating the list

#### Scenario: Delete by name
- **WHEN** a DELETE request is made to `/v1/mcp/servers/:name` and that name exists
- **THEN** the gateway SHALL remove that entry from the stored list and return 204

#### Scenario: Delete missing name
- **WHEN** a DELETE request is made to `/v1/mcp/servers/:name` and no entry matches
- **THEN** the gateway SHALL return 404

### Requirement: /v1/status surfaces MCP server health
The gateway's `/v1/status` handler SHALL include an `mcp_servers` field populated from the `health:main-agent:mcp` Valkey key. Each entry SHALL contain `name`, `enabled`, `connected`, `tool_count`, and `last_error` fields.

#### Scenario: Status includes MCP health
- **WHEN** a GET request is made to `/v1/status` and `health:main-agent:mcp` is populated
- **THEN** the response SHALL include a top-level `mcp_servers` field whose value is the parsed JSON array from that key

#### Scenario: Status when MCP health absent
- **WHEN** `health:main-agent:mcp` is missing or empty (main-agent not yet reporting)
- **THEN** the response SHALL include `mcp_servers` as an empty array, not omit the field

### Requirement: Speaker_id returned in response headers
The gateway SHALL return the resolved `speaker_id` in the `X-Speaker-ID` response header for every response on `/v1/responses` and `/v1/chat/completions`. When the resolved value is `"unknown"`, the header SHALL still be emitted with that literal value so clients can detect the unknown-speaker case without inspecting log output.

#### Scenario: Known speaker echoed
- **WHEN** the gateway resolves a turn's speaker to `"jason"`
- **THEN** the response SHALL include the header `X-Speaker-ID: jason`

#### Scenario: Unknown speaker echoed
- **WHEN** the gateway resolves a turn's speaker to `"unknown"`
- **THEN** the response SHALL include the header `X-Speaker-ID: unknown`

#### Scenario: Streaming sets header before first chunk
- **WHEN** a streaming response begins
- **THEN** `X-Speaker-ID` SHALL be set before the first SSE chunk is emitted
