## Context

microagent2 exposes an OpenAI-compatible `/v1/chat/completions` endpoint via its gateway service. The chat/completions API is stateless by design: the client sends the full message history on every request, and the server holds no conversation state. The gateway generates a `session_id` per request, but no standard client (Open WebUI, curl, SDKs) knows about this custom extension, so every request creates a new disconnected session.

This causes two concrete problems:
1. Retrospective agents see fragmented single-turn "conversations" instead of coherent multi-turn sessions, degrading memory extraction quality.
2. Future tool-calling support requires multi-round server-side chaining within a single logical turn — impossible when every request starts a new session.

Additionally, the client may modify message history between requests (compaction, pruning, removing tool responses), so the client's `messages` array is not an authoritative record. microagent2 needs its own canonical conversation store.

The gateway sits between clients and all internal services (context manager, broker, agents). It already mediates every request. This makes it the natural place to implement server-side conversation state.

## Goals / Non-Goals

**Goals:**
- Implement the OpenAI Responses API (`/v1/responses`) as the primary external endpoint with server-side conversation state via `previous_response_id` chaining
- Maintain backward compatibility via `/v1/chat/completions` as a thin wrapper that routes through the Responses pipeline
- Store response objects indefinitely in Valkey as the canonical conversation record
- Link response chains to sessions so existing session-based consumers (dashboard, retrospection, session CRUD) continue working
- Support tool_calls and tool result items in the response format (execution is out of scope)
- Preserve the context manager's role: it still enriches conversations with memories and assembles prompts for llama-server

**Non-Goals:**
- Tool-calling execution (future change — this change provides the format, not the loop)
- Response pruning, compaction, or TTL-based expiry (indefinite storage for now)
- Server-side context compaction (the `context_management` field from OpenAI's API)
- Streaming via WebSocket (SSE remains the streaming transport)
- Changes to llm-broker, agent-runtime, agent-registration, or retrospection job logic
- Built-in tools (web_search, file_search) — only function-type tool definitions are carried

## Decisions

### 1. Response objects stored as Valkey hashes

**Decision:** Each response is stored as a Valkey hash at key `response:{response_id}` with fields for input, output, previous_response_id, session_id, model, metadata, and timestamps.

**Alternatives considered:**
- JSON blob in a string key: Simpler writes, but requires full deserialization for any field access. Response chain traversal needs only `previous_response_id` and `session_id` — hash field access is cheaper.
- Dedicated database (SQLite, Postgres): Overkill for single-user system. Adds infrastructure. Valkey is already the backbone.
- Valkey Streams for response storage: Streams are append-only logs — poor fit for keyed lookup by response ID. Streams are right for messaging, hashes are right for entity storage.

**Rationale:** Hashes give O(1) field access by response ID. Chain traversal reads only the `previous_response_id` field per hop. Full response reconstruction reads all fields only for the final assembly. Valkey persistence (AOF) covers durability.

### 2. Response chain traversal at request time, not pre-materialized

**Decision:** When a request includes `previous_response_id`, the gateway walks the chain backward at request time to reconstruct the conversation. There is no pre-materialized "conversation" data structure.

**Alternatives considered:**
- Materialized conversation view updated on each response: Avoids chain walking but creates a write-amplification problem — every new response updates the full conversation record. Also creates consistency risk if the write fails after the response is stored.
- Doubly-linked chain (forward + backward pointers): Forward pointers enable walking from the start, but require updating the previous response when a new one is created — same write-amplification issue.

**Rationale:** Chain walks are bounded by conversation length (typically tens of turns, not thousands). Each hop is a single HGET for `previous_response_id` — cheap. This keeps writes simple (one hash per response, no back-patching) and avoids consistency issues. If chain walking becomes a bottleneck, a response-to-session index (`session:{id}:responses` as a list) provides O(1) ordered access as a future optimization.

### 3. Session association is determined at chain start

**Decision:** When a request has no `previous_response_id` (new conversation), the gateway creates a new session and associates it with the response. When a request has a `previous_response_id`, the gateway inherits the session from the previous response. The session_id is stored on every response object for O(1) lookup.

**Alternatives considered:**
- Derive session by walking to chain root: Expensive and redundant. Every response in a chain belongs to the same session — just propagate it.
- Let clients specify session_id: Restores the original problem — clients don't send it. Session association should be server-side and implicit.

**Rationale:** Simple propagation. Root response creates the session. All subsequent responses copy `session_id` from their predecessor. Session CRUD endpoints query responses by session_id using the session index.

### 4. Chat/completions translates to a single-turn Response internally

**Decision:** `POST /v1/chat/completions` converts the incoming request into a Responses API call: the `messages` array becomes the `input`, no `previous_response_id` is set, and the response is stored as a single-response chain. The chat/completions response format is reconstructed from the internal response object.

**Alternatives considered:**
- Keep chat/completions as a separate code path: Duplicates request handling logic. Two paths to maintain and debug. Divergence over time is inevitable.
- Attempt session inference from message history matching: Fragile heuristic. Message content matching is unreliable when clients modify history. Added complexity for an imperfect result.

**Rationale:** One code path, two external formats. The translation is mechanical: `messages` → `input` items, response object → chat/completions response format. Each chat/completions call creates an independent single-turn response chain with its own session. This means chat/completions clients still create one session per request (same as today), but the data flows through the same pipeline as Responses API calls.

### 5. Session index for efficient session-based queries

**Decision:** Maintain a Valkey list `session:{session_id}:responses` that appends each response ID as responses are created. This index supports the session CRUD endpoints and retrospection without full-scan queries.

**Alternatives considered:**
- Query by scanning all response hashes for matching session_id: O(N) over all responses. Unacceptable as response count grows.
- Store conversation history separately from responses: Reintroduces the dual-source-of-truth problem this change is designed to eliminate.

**Rationale:** The list is append-only and cheap to maintain (one RPUSH per response). Session history reconstruction reads the list, then batch-reads the response hashes. The list provides ordering. Deletion of a session deletes the list and all referenced response hashes.

### 6. Internal message format carries resolved conversation, not raw client input

**Decision:** The gateway resolves the response chain and publishes the reconstructed conversation (as a messages array in the existing internal format) to `stream:gateway:requests`. The context manager receives a conversation identical to what it receives today — it doesn't need to know about response chains.

**Alternatives considered:**
- Pass response chain IDs to context manager, let it resolve: Couples the context manager to response storage. Breaks the current clean boundary where the context manager receives messages and enriches them.
- Pass both raw input and chain metadata: Unnecessary complexity. The context manager only needs the conversation history and the current user input.

**Rationale:** The gateway is the stateful translation layer. Everything downstream sees a resolved conversation. This minimizes changes to the context manager, broker, and agent runtime — they continue to work with messages arrays. The gateway absorbs the complexity of the new API format.

## Risks / Trade-offs

**Response chain traversal latency on long conversations** → Bounded by conversation length. Each hop is a single HGET. For a 50-turn conversation, that's 50 Valkey reads — sub-millisecond each. If this becomes measurable, add the session response list as an ordered index and batch-read in one MGET. Not worth optimizing until proven necessary.

**Indefinite response storage grows Valkey memory** → Accepted for v1. Single-user system with text-only responses. Even aggressive usage (100 turns/day, 2KB/response) is ~73MB/year. Future pruning change can add TTL or archival.

**Chat/completions clients still fragment sessions** → By design. Without `previous_response_id`, there's no reliable way to chain requests. Each chat/completions call is an independent single-turn conversation. This is acceptable — the Responses API is the intended path for stateful interactions.

**Response format must support tool_calls without executing them** → The response output items schema must include `function_call` and `function_call_output` item types. These are stored but never processed by the agent in this change. Malformed tool items are stored as-is — validation is deferred to the tool-calling change.

**Breaking change to session history backing** → Session history currently would be stored independently. After this change, it's derived from response chains. The session CRUD API contract doesn't change, but the storage mechanism does. Any code reading raw session Valkey keys will break — all access must go through the session API or response chain traversal.
