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
