## Why

The gateway currently exposes only `/v1/chat/completions`, which is stateless by design — clients send the full message history on every request and the server holds no conversation state. The gateway generates a `session_id` per request, but no client knows about this custom extension field, so every request creates a new session. This fragments conversation history, prevents retrospective agents from seeing coherent conversations, and will block tool-calling support (which requires multi-round server-side chaining within a single logical turn). The OpenAI Responses API (`/v1/responses`) solves this with server-side conversation state and `previous_response_id` chaining. Since llama-server doesn't support the Responses API, the gateway becomes the stateful translation layer — converting Responses API requests into chat/completions calls to llama-server internally.

## What Changes

- Gateway gains a `POST /v1/responses` endpoint accepting input, `previous_response_id`, model, and optional tool definitions (tool execution is out of scope — but the format must carry them)
- Gateway stores response objects in Valkey (indefinite retention) keyed by response ID, with links to `previous_response_id` for chain traversal
- Gateway resolves response chains to reconstruct canonical conversation history before passing to the context manager
- **BREAKING**: `POST /v1/chat/completions` becomes a thin wrapper — internally translates to a single-turn Responses call (no `previous_response_id`). Behavior is preserved but the code path unifies through the Responses pipeline
- Sessions are retained as a grouping concept: a new response chain creates or associates with a session; subsequent responses in the chain inherit the same session
- Context manager input source changes from the client-provided `messages` array to gateway-resolved conversation history from the response chain
- Session history is derived from response chains rather than independently stored — eliminates the dual-source-of-truth problem
- Dashboard and retrospection agents read conversation history by following response chains

## Capabilities

### New Capabilities
- `response-chain`: Server-side storage and traversal of response objects linked by `previous_response_id`, providing the canonical conversation record for all downstream consumers (context assembly, retrospection, dashboard)

### Modified Capabilities
- `gateway-api`: New `/v1/responses` endpoint; `/v1/chat/completions` becomes a thin wrapper routing through the Responses pipeline
- `session-management`: Sessions linked to response chains; session history derived from chain store rather than independently stored
- `context-management`: Input source changes from client-provided messages to gateway-resolved response chain history

## Impact

- **API surface**: New primary endpoint (`/v1/responses`). Existing `/v1/chat/completions` preserved but internally rerouted. Clients using chat/completions see no behavior change.
- **Storage**: New Valkey keys for response objects (indefinite retention). Response chain traversal adds a read-before-process step on requests with `previous_response_id`.
- **Inter-service messaging**: The internal message format on `stream:gateway:requests` may need to carry response chain context rather than raw client messages.
- **Downstream consumers**: Retrospection agents and dashboard read from response chains instead of session history keys. Session CRUD endpoints remain but their backing data changes.
- **Future enablement**: The response format supports tool_calls and tool result items, laying groundwork for the tool-calling change without implementing execution.
