## ADDED Requirements

### Requirement: OpenAI-compatible chat completions endpoint
The gateway SHALL expose a `/v1/chat/completions` endpoint that accepts requests conforming to the OpenAI Chat Completions API format and returns responses in the same format.

#### Scenario: Non-streaming chat completion
- **WHEN** a client sends a POST to `/v1/chat/completions` with `stream: false` and a valid `messages` array
- **THEN** the gateway returns a complete response with `id`, `object: "chat.completion"`, `choices` array containing the assistant message, and `usage` statistics

#### Scenario: Streaming chat completion
- **WHEN** a client sends a POST to `/v1/chat/completions` with `stream: true` and a valid `messages` array
- **THEN** the gateway returns a `text/event-stream` response where each SSE event contains a `chat.completion.chunk` object with delta content, ending with a `[DONE]` sentinel

#### Scenario: Invalid request format
- **WHEN** a client sends a request missing required fields or with malformed JSON
- **THEN** the gateway returns an error response with an appropriate HTTP status code and an `error` object containing `message`, `type`, and `code` fields matching OpenAI error format

### Requirement: Request translation to internal format
The gateway SHALL translate incoming OpenAI-format requests into internal message format and publish them to the Valkey request stream, and SHALL translate internal responses back to OpenAI format for the client.

#### Scenario: Inbound translation
- **WHEN** the gateway receives a valid chat completion request
- **THEN** it publishes a message to `stream:gateway:requests` containing a `correlation_id`, the extracted `messages` array, session identifier, and streaming preference

#### Scenario: Outbound streaming translation
- **WHEN** the gateway receives tokens on `channel:tokens:{session_id}` for a streaming request
- **THEN** it wraps each token in an OpenAI `chat.completion.chunk` SSE event and sends it to the client in real time

### Requirement: Session continuity
The gateway SHALL support multi-turn conversations by deriving a session identifier from the request context, allowing the context manager to maintain conversation history.

#### Scenario: Continuing a conversation
- **WHEN** a client sends a request that includes prior message history in the `messages` array
- **THEN** the gateway forwards the full message history and a stable session identifier so the context manager can reconcile with stored history

#### Scenario: New conversation
- **WHEN** a client sends a request with only a system message and a single user message
- **THEN** the gateway generates a new session identifier and includes it in the internal message
