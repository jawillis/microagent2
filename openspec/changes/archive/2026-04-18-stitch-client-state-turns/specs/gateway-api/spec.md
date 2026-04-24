## ADDED Requirements

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

## MODIFIED Requirements

### Requirement: Responses API endpoint
The gateway SHALL expose a `POST /v1/responses` endpoint that accepts a request body with fields: `input` (string or array of input items), `model` (string), `previous_response_id` (optional string), `tools` (optional array of tool definitions), `tool_choice` (optional string or object), `store` (optional boolean, default true), and `stream` (optional boolean, default false). When the request omits `previous_response_id`, has `store` true-or-absent, and provides a multi-message `input`, the gateway SHALL additionally consult the session-stitching index as defined in the "stitches client-side-state turns" requirement.

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
