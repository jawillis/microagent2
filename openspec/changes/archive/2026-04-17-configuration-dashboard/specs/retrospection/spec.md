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
