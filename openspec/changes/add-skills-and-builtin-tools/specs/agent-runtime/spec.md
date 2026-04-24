## ADDED Requirements

### Requirement: Main-agent ownership of tool loop
The main-agent SHALL own the tool-execution loop described in the `tool-invocation` capability. The agent runtime (`Runtime.Execute`) SHALL remain a single-turn primitive — one LLM call per invocation — and SHALL NOT itself orchestrate multi-iteration loops. Main-agent composes `Runtime.Execute` calls into a loop via `cmd/main-agent/main.go`.

#### Scenario: Runtime.Execute stays single-turn
- **WHEN** a caller invokes `Runtime.Execute` or `Runtime.ExecuteWithCorrelation`
- **THEN** the call SHALL perform exactly one LLM exchange (one slot-scoped request/response) and return; it SHALL NOT invoke tools or perform additional LLM calls

#### Scenario: Loop orchestration sits in main-agent
- **WHEN** main-agent handles a user turn whose model emits `tool_calls`
- **THEN** the iteration, tool invocation, message-appending, and re-entry into `Execute` SHALL all happen in `cmd/main-agent/main.go`, not inside `internal/agent/runtime.go`

### Requirement: Per-iteration slot lifecycle
Main-agent SHALL acquire a broker slot at the start of each loop iteration and release it before invoking any tools. A slot SHALL NOT be held across a tool invocation.

#### Scenario: Iteration-scoped slot
- **WHEN** main-agent enters a loop iteration
- **THEN** it SHALL invoke `Runtime.RequestSlot`, call `Runtime.Execute`, invoke `Runtime.ReleaseSlot` (even on error), and only then proceed to evaluate the returned tool_calls and invoke tools

#### Scenario: Release before tool invocation even if only iteration
- **WHEN** the loop runs exactly one iteration (pure-text turn)
- **THEN** the slot SHALL still be released as part of that iteration before main-agent publishes the final `ChatResponsePayload` or any pub/sub terminal message
