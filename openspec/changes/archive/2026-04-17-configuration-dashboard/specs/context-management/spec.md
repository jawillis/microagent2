## MODIFIED Requirements

### Requirement: Context assembly with configurable memory settings
The context manager SHALL assemble chat context by combining the system prompt, recalled memories, session history, and the current user message. The system prompt, recall limit, recall threshold, max hops, pre-warm limit, vault name, and store confidence SHALL be read from the config store at startup, with env var and hardcoded fallbacks.

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
