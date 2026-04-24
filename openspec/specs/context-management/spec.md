## ADDED Requirements

### Requirement: Structured per-request logging
The context manager SHALL emit structured INFO log lines at every significant hand-off while processing a request, tagged with the request's `correlation_id`.

#### Scenario: Request decoded
- **WHEN** the context manager decodes a `ChatRequestPayload` from `stream:gateway:requests`
- **THEN** it SHALL log at INFO with `msg: "context_request_decoded"` and fields `{correlation_id, session_id, message_count}`

#### Scenario: Memory recall completed
- **WHEN** the call to `muninn.Recall` returns (success or error)
- **THEN** the context manager SHALL log at INFO with `msg: "context_muninn_recall"` and fields `{correlation_id, elapsed_ms, memory_count, outcome}` where `outcome` is `ok` or `error`

#### Scenario: Session history loaded
- **WHEN** the context manager loads session history from the response store
- **THEN** it SHALL log at INFO with `msg: "context_history_loaded"` and fields `{correlation_id, session_id, history_count}`

#### Scenario: Context published to agent
- **WHEN** the context manager publishes a context-assembled message to `stream:agent:{agent}:requests`
- **THEN** it SHALL log at INFO with `msg: "context_published"` and fields `{correlation_id, session_id, target_agent, assembled_count}`

## MODIFIED Requirements

### Requirement: Context assembly with configurable memory settings
The context manager SHALL assemble chat context by combining the system prompt, recalled memories, conversation history, and the current user message. The conversation history SHALL be received from the gateway as a resolved messages array (reconstructed from the response chain by the gateway). The context manager SHALL NOT resolve response chains or access response storage directly. The system prompt, recall limit, recall threshold, max hops, pre-warm limit, vault name, and store confidence SHALL be read from the config store at startup, with env var and hardcoded fallbacks.

#### Scenario: Context assembly from gateway-provided history
- **WHEN** the context manager receives a request on `stream:gateway:requests`
- **THEN** it SHALL use the `messages` field from the request payload as the conversation history, without performing any response chain resolution

#### Scenario: Memory recall uses configured limits
- **WHEN** the context manager recalls memories for a user message
- **THEN** it SHALL use `recall_limit` from `config:memory` as the max results and `recall_threshold` as the minimum similarity score

#### Scenario: Graph traversal uses configured depth
- **WHEN** the context manager queries MuninnDB
- **THEN** it SHALL use `max_hops` from `config:memory` as the graph traversal depth

#### Scenario: Pre-warm uses configured limit
- **WHEN** the context manager pre-warms memories after a response
- **THEN** it SHALL use `prewarm_limit` from `config:memory` as the recall limit

#### Scenario: System prompt from config
- **WHEN** the context manager assembles context
- **THEN** it SHALL use `system_prompt` from `config:chat` as the system prompt content

#### Scenario: Vault name from config
- **WHEN** the context manager reads from or writes to MuninnDB
- **THEN** it SHALL use `vault` from `config:memory` as the vault identifier

#### Scenario: Store confidence from config
- **WHEN** the retro agent stores a new memory via MuninnDB
- **THEN** it SHALL use `store_confidence` from `config:memory` as the confidence value
