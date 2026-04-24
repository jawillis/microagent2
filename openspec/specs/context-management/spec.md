## ADDED Requirements

### Requirement: Session history tracking
The context manager SHALL maintain a per-session conversation history in Valkey, appending each user message and assistant response as they occur.

#### Scenario: Recording a conversation turn
- **WHEN** the context manager receives a new user message for a session
- **THEN** it appends the message to the session's history store and the history is available for subsequent prompt assembly

#### Scenario: Session history retrieval
- **WHEN** prompt assembly is requested for a session
- **THEN** the context manager retrieves the full conversation history for that session from Valkey

### Requirement: Memory retrieval and injection
The context manager SHALL query MuninnDB with the user's input to retrieve relevant memories and inject them into the prompt context.

#### Scenario: Memory recall on user message
- **WHEN** a new user message arrives for prompt assembly
- **THEN** the context manager queries MuninnDB using the user message content and receives a ranked list of relevant memories

#### Scenario: Memory injection into prompt
- **WHEN** relevant memories are retrieved from MuninnDB
- **THEN** the context manager inserts them into the prompt between the system prompt and conversation history, formatted as contextual information the agent can reference

#### Scenario: No relevant memories found
- **WHEN** MuninnDB returns no memories above the relevance threshold
- **THEN** the context manager assembles the prompt without a memory section

### Requirement: Parallel fetch for prompt assembly
The context manager SHALL fetch session history and query MuninnDB in parallel to minimize prompt assembly latency.

#### Scenario: Parallel retrieval
- **WHEN** the context manager begins prompt assembly for a user message
- **THEN** it initiates session history retrieval and MuninnDB memory recall concurrently, assembling the final prompt only after both complete

### Requirement: Thin system prompt
The context manager SHALL use a minimal system prompt that delegates behavioral guidance to injected memories and skills, keeping the static prompt token count low to maximize KV cache effectiveness.

#### Scenario: Prompt structure
- **WHEN** the context manager assembles a prompt
- **THEN** the resulting prompt follows the structure: thin system prompt → injected memories/skills → conversation history → current user message

### Requirement: Speculative memory pre-warming
The context manager SHOULD support pre-fetching likely-relevant memories after each assistant response, caching results in the session for faster retrieval on the next turn.

#### Scenario: Pre-warm after response
- **WHEN** an assistant response is completed for a session
- **THEN** the context manager MAY extract key topics from the response and pre-fetch related memories, storing results in a session-local cache

#### Scenario: Cache hit on next turn
- **WHEN** a new user message arrives and pre-warmed memories are cached for the session
- **THEN** the context manager uses cached results where relevant and supplements with a fresh MuninnDB query only for uncovered topics
