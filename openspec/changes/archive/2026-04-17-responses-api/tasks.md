## 1. Response Chain Store

- [x] 1.1 Implement response ID generation using `resp_` prefix + ULID
- [x] 1.2 Implement response hash storage: write response objects to `response:{id}` Valkey hashes with fields `id`, `input`, `output`, `previous_response_id`, `session_id`, `model`, `created_at`, `status`
- [x] 1.3 Implement response retrieval by ID (read all fields from Valkey hash)
- [x] 1.4 Implement response chain traversal: given a response ID, walk `previous_response_id` links to root, return input/output items in chronological order. Return error on broken chain.
- [x] 1.5 Implement session index maintenance: RPUSH response ID to `session:{session_id}:responses` on every response store
- [x] 1.6 Implement session-based history retrieval: read response IDs from session list, batch-read response hashes, return ordered conversation

## 2. Session Association

- [x] 2.1 Implement session creation for new response chains (no `previous_response_id`): generate UUIDv4 session ID, store session metadata
- [x] 2.2 Implement session inheritance for continued chains: read `session_id` from previous response, propagate to new response
- [x] 2.3 Implement session association for chat/completions wrapper: use client-provided `session_id` if present, otherwise create new session

## 3. Responses API Endpoint

- [x] 3.1 Implement `POST /v1/responses` request parsing: validate `input` (string or array), `model`, optional `previous_response_id`, optional `tools`/`tool_choice`, optional `stream`, optional `store`
- [x] 3.2 Implement request pipeline: resolve response chain (if `previous_response_id`), reconstruct conversation history, determine session, publish resolved messages to `stream:gateway:requests`
- [x] 3.3 Implement non-streaming response: wait for agent completion, build response object with output items, store response, return JSON body
- [x] 3.4 Implement streaming response: subscribe to token channel, emit SSE events as tokens arrive, build and store final response object on stream completion, include `session_id` in `X-Session-ID` header before first chunk
- [x] 3.5 Implement response output item formatting: convert agent LLM output to response items (`message` type with content array, `function_call` type with call_id/name/arguments)
- [x] 3.6 Implement `GET /v1/responses/:id` endpoint: retrieve stored response by ID, return 404 if not found

## 4. Chat/Completions Wrapper

- [x] 4.1 Refactor `POST /v1/chat/completions` to translate incoming request: convert `messages` array to Responses API `input` items format
- [x] 4.2 Route translated request through the Responses pipeline (no `previous_response_id`, session from client or auto-generated)
- [x] 4.3 Translate response object back to chat/completions format: convert output items to `choices[].message`, include `session_id` in body and `X-Session-ID` header
- [x] 4.4 Preserve streaming behavior: SSE chunks in chat/completions delta format, translated from internal response events

## 5. Session CRUD Updates

- [x] 5.1 Update `GET /v1/sessions` to derive turn count and last_active from the session response list
- [x] 5.2 Update `GET /v1/sessions/:id` to reconstruct conversation from response chain (read session response list, batch-read response hashes, format as messages)
- [x] 5.3 Update `DELETE /v1/sessions/:id` to delete all response hashes referenced by the session's response list, the response list key, and session metadata

## 6. Context Manager Update

- [x] 6.1 Update gateway request publishing: include resolved conversation history as `messages` in the `stream:gateway:requests` payload (gateway resolves chain, context manager receives messages array as before)
- [x] 6.2 Verify context manager processes gateway-provided messages without changes (it should already work since the input format is unchanged — this task is a validation/test pass)

## 7. Integration Testing

- [x] 7.1 Test: new Responses API request (no previous_response_id) creates response, session, and session index entry
- [x] 7.2 Test: chained Responses API request (with previous_response_id) inherits session, appends to session index, response chain traversal returns full conversation
- [x] 7.3 Test: broken chain (invalid previous_response_id) returns HTTP 400
- [x] 7.4 Test: chat/completions request routes through Responses pipeline, returns valid chat/completions format response with session_id
- [x] 7.5 Test: GET /v1/responses/:id returns stored response; 404 for unknown ID
- [x] 7.6 Test: GET /v1/sessions/:id returns conversation reconstructed from response chain
- [x] 7.7 Test: DELETE /v1/sessions/:id removes all response hashes and session index
- [x] 7.8 Test: streaming responses (both Responses API and chat/completions) deliver tokens via SSE with session_id in header
