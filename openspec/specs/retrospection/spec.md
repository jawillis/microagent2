## MODIFIED Requirements

### Requirement: Retrospection uses configurable policy settings
The retro agent SHALL read inactivity timeout, skill duplicate threshold, minimum history turns, and curation categories from the config store at startup, with env var and hardcoded fallbacks.

#### Scenario: Inactivity timeout from config
- **WHEN** the retro trigger monitors for inactivity
- **THEN** it SHALL use `inactivity_timeout_s` from `config:retro` (default 300) as the timeout duration

#### Scenario: Skill duplicate threshold from config
- **WHEN** the skill creation job checks for duplicate skills
- **THEN** it SHALL use `skill_dup_threshold` from `config:retro` (default 0.85) as the similarity cutoff

#### Scenario: Minimum history for skill creation from config
- **WHEN** the skill creation job checks whether to process a session
- **THEN** it SHALL require at least `min_history_turns` from `config:retro` (default 4) turns in the session history

#### Scenario: Curation categories from config
- **WHEN** the curation job iterates over memory categories
- **THEN** it SHALL use `curation_categories` from `config:retro` (default ["preference","fact","context","skill"])

## ADDED Requirements

### Requirement: Retro jobs support external triggering via stream
The retro agent SHALL consume a `stream:retro:triggers` stream for externally triggered job requests. Each message SHALL contain a session ID and job type. The retro agent SHALL execute the requested job as if triggered by an inactivity timeout.

#### Scenario: External trigger received
- **WHEN** a message is published to `stream:retro:triggers` with a session ID and job type
- **THEN** the retro agent SHALL execute the specified job for that session

### Requirement: Retro jobs acquire and release locks
Each retro job (whether triggered by inactivity, session end, or external trigger) SHALL acquire a lock key `retro:lock:<session>:<job_type>` via SET NX with a 300-second TTL before execution. The lock SHALL be deleted on job completion.

#### Scenario: Lock acquired
- **WHEN** a retro job starts and no lock exists
- **THEN** the job SHALL set the lock and proceed with execution

#### Scenario: Lock already held
- **WHEN** a retro job starts and a lock already exists for that session and job type
- **THEN** the job SHALL skip execution and log a warning

#### Scenario: Lock released on completion
- **WHEN** a retro job completes (success or error)
- **THEN** the lock key SHALL be deleted from Valkey

### Requirement: Structured extraction schema for stored memories
The memory-extraction job SHALL produce memories with the Muninn-shaped schema required for correct retrieval scoring. The retro agent SHALL pass the following fields per memory to `MuninnClient.Store`:

- `concept`: a specific-to-this-memory headline sentence (e.g. `"Jason prefers dark roast coffee"`). The field SHALL NOT be a coarse category label such as `"fact"`, `"preference"`, or `"skill"`.
- `content`: the full-sentence statement of the memory.
- `summary`: a one-line restatement (≤ 140 characters) of the memory, suitable for display.
- `tags`: 3-8 short strings, each a word the user is likely to type in a query where this memory should be recalled (e.g. `["coffee","caffeine","morning","drink"]`). Tags SHALL NOT be semantic-category words (`["beverage_preference"]`).
- `memory_type`: one of `"fact"`, `"decision"`, `"observation"`, `"preference"`, `"issue"`, `"task"`, `"procedure"`, `"event"`, `"goal"`, `"constraint"`, `"identity"`, `"reference"`.
- `confidence`: float in `[0.0, 1.0]` reflecting the extraction's self-reported certainty. The value SHALL NOT be hardcoded.

#### Scenario: Concept is a specific headline
- **WHEN** the memory-extraction job stores a memory about a user's food allergy
- **THEN** the `concept` field SHALL be a specific sentence like `"Jason is allergic to shellfish"`, NOT a category like `"fact"` or `"food_preference"`

#### Scenario: Tags are likely user-query words
- **WHEN** the memory-extraction job stores a memory about the user's coffee preference
- **THEN** the `tags` field SHALL contain words a user would naturally type in a related query (e.g. `["coffee","morning","caffeine"]`), NOT semantic-category labels

#### Scenario: Memory type drawn from Muninn's enum
- **WHEN** the memory-extraction job stores a memory
- **THEN** the `memory_type` field SHALL be one of the 12 values in Muninn's enum

#### Scenario: Unrecognized memory type falls back to Muninn auto-classify
- **WHEN** the extraction LLM produces a `memory_type` value not in the Muninn enum
- **THEN** the field SHALL be omitted from the `Store` call, and Muninn's auto-classification SHALL apply

#### Scenario: Confidence reflects extraction certainty
- **WHEN** the memory-extraction job stores a memory with strong evidence in the conversation
- **THEN** the `confidence` field SHALL be close to `1.0`; when the evidence is ambiguous or inferred, the value SHALL be lower

#### Scenario: Summary provided to skip Muninn's LLM summarization
- **WHEN** the memory-extraction job stores a memory
- **THEN** the `summary` field SHALL be populated, causing Muninn's background LLM summarization to be skipped under `inline_enrichment: "caller_preferred"` (Muninn's default)

### Requirement: Skill-creation uses the same enriched schema
The skill-creation job SHALL produce stored skills using the same Muninn-shaped schema as the memory-extraction job, with `memory_type` fixed to `"procedure"` and `concept` populated as a specific headline describing the approach.

#### Scenario: Skill stored as a procedure
- **WHEN** the skill-creation job stores a new skill
- **THEN** the `memory_type` field SHALL be `"procedure"` and the `concept` field SHALL be a headline describing the problem class and approach (e.g. `"Approach for diagnosing flaky CI tests by isolating shared fixtures"`)

### Requirement: Curation actions execute against Muninn endpoints
The curation job SHALL execute its parsed actions as calls to Muninn's mutation endpoints, rather than logging them without effect. Supported actions and their Muninn targets:

- `merge`: `POST /api/consolidate` with `{vault, ids, merged_content}`; requires ≥ 2 ids and non-empty `merged_content`.
- `evolve`: `POST /api/engrams/{id}/evolve` with new content; requires exactly 1 id and non-empty content. Creates a `supersedes` association.
- `delete`: `DELETE /api/engrams/{id}`; requires exactly 1 id.

Unknown action strings SHALL be logged at WARN and skipped, NOT executed. Action execution failures SHALL be logged at WARN and SHALL NOT abort the remaining actions in the batch.

#### Scenario: Merge action calls consolidate
- **WHEN** the curation LLM emits `{"action":"merge","indices":[0,1],"merged_content":"..."}`
- **THEN** the curation job SHALL call `POST /api/consolidate` with the corresponding engram IDs and the merged content

#### Scenario: Evolve action calls the evolve endpoint
- **WHEN** the curation LLM emits `{"action":"evolve","indices":[2],"merged_content":"refined content"}`
- **THEN** the curation job SHALL call `POST /api/engrams/{id}/evolve` for the engram at index 2 with the new content

#### Scenario: Delete action calls delete
- **WHEN** the curation LLM emits `{"action":"delete","indices":[3]}`
- **THEN** the curation job SHALL call `DELETE /api/engrams/{id}` for the engram at index 3

#### Scenario: Unknown action is skipped, not executed
- **WHEN** the curation LLM emits an action with a `type` not in `{merge, evolve, delete}`
- **THEN** the curation job SHALL log the action at WARN and continue with the next action

#### Scenario: Action failure does not abort the batch
- **WHEN** the curation job executes multiple actions and one fails (e.g. consolidate returns 500)
- **THEN** the failure SHALL be logged at WARN and subsequent actions SHALL still execute

#### Scenario: Every executed action is logged
- **WHEN** the curation job executes any action
- **THEN** an INFO log `retro_curation_action` SHALL be emitted containing `{category, action, ids, reason}` BEFORE the endpoint call is made
