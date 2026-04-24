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
The memory-extraction job SHALL produce memories shaped for memory-service's `/retain` endpoint, relying on Hindsight's entity extraction and fact-type classification rather than supplying those fields. The retro agent SHALL pass the following fields per memory to memory-service `/retain`:

- `content`: the full-sentence statement of the memory, phrased specifically and using role-based language when the subject is the speaker (e.g. `"The speaker prefers dark roast coffee"` or `"Jason prefers dark roast coffee"` — either is acceptable, but the extraction prompt SHALL NOT hard-code any single person's name as the default subject). Phrased specifically enough to be independently useful when surfaced.
- `tags`: 3-8 short strings, each a word the user is likely to type in a query where this memory should be recalled (e.g. `["coffee","caffeine","morning","drink"]`). Tags SHALL NOT be semantic-category words (`["beverage_preference"]`). Tags SHOULD include at least one taxonomy tag from the initial set (`identity`, `preferences`, `technical`, `home`, `corrections`, `inferred`, `ephemera`).
- `metadata.provenance`: one of `explicit` (user said directly), `implicit` (observed from behavior), `inferred` (agent-derived), or `researched` (web source). The retro agent SHALL emit `explicit` for directly-stated memories and `implicit` for behavior-observed memories.
- `metadata.confidence`: float in `[0.0, 1.0]` serialized as a string, reflecting the extraction's self-reported certainty. The value SHALL NOT be hardcoded.
- `metadata.speaker_id`: the `speaker_id` resolved for the session turn from which the memory was extracted. When a session turn has multiple speakers (group-chat case), the retro-agent SHALL emit one memory per speaker with that speaker's `speaker_id`.
- `metadata.fact_type`: one of `person_fact`, `world_fact`, `context_fact`, `procedural_fact`. The retro-agent SHALL emit this field explicitly on every retained memory.
- `context`: optional source description (e.g. `"retro extraction from session <id>"`).
- `entities`: the list of entity references (person IDs, object references) the memory is about. Class-level references SHALL be prefixed with `class:` (e.g. `"class:cat"`).
- `observation_scopes`: `"combined"` (default) so that consolidation produces unified observations; `"per_tag"` only when per-tag scoping is explicitly desired.

The retro agent SHALL NOT supply `memory_type`, `vault`, `summary`, or any muninn-era fields; Hindsight's `retain_mission` performs the analogous classification internally and stores fact-type on the retained memory.

#### Scenario: Content is a specific headline
- **WHEN** the memory-extraction job stores a memory about a food allergy of the speaker
- **THEN** the `content` field SHALL be a specific sentence like `"The speaker is allergic to shellfish"` or `"Jason is allergic to shellfish"`, NOT a category like `"fact"` or `"food_preference"`

#### Scenario: Tags are likely user-query words
- **WHEN** the memory-extraction job stores a memory about the speaker's coffee preference
- **THEN** the `tags` field SHALL contain words a user would naturally type in a related query (e.g. `["coffee","morning","caffeine","preferences"]`), NOT semantic-category labels

#### Scenario: Provenance tagged on every retain call
- **WHEN** the memory-extraction job calls memory-service `/retain`
- **THEN** the request `metadata.provenance` field SHALL be one of `explicit`, `implicit`, `inferred`, or `researched`

#### Scenario: Confidence reflects extraction certainty
- **WHEN** the memory-extraction job stores a memory with strong evidence in the conversation
- **THEN** the `metadata.confidence` field (string-encoded) SHALL be close to `"1.0"`; when the evidence is ambiguous or inferred, the value SHALL be lower

#### Scenario: Speaker_id attached to every retain
- **WHEN** the memory-extraction job calls memory-service `/retain`
- **THEN** the request `metadata.speaker_id` SHALL be the speaker_id resolved for the originating session turn

#### Scenario: Fact_type attached to every retain
- **WHEN** the memory-extraction job calls memory-service `/retain`
- **THEN** the request `metadata.fact_type` SHALL be one of `person_fact`, `world_fact`, `context_fact`, or `procedural_fact`

#### Scenario: Speaker-about-other emits third-party entities
- **WHEN** the memory-extraction job processes a turn in which the speaker states a fact about another person
- **THEN** the emitted memory SHALL have `metadata.speaker_id` set to the speaker, `entities` set to the third-party subject, and `metadata.fact_type="person_fact"`

### Requirement: Skill-creation uses memory-service retain
The skill-creation job SHALL store new skills via memory-service `/retain`, with the `skill` tag added to `tags` and `metadata.provenance` set to `implicit` (a skill is observed from repeated patterns, not user-stated).

#### Scenario: Skill stored via memory-service
- **WHEN** the skill-creation job stores a new skill
- **THEN** it SHALL POST to memory-service `/retain` with `tags` including `"skill"` and a headline `content` (e.g. `"Approach for diagnosing flaky CI tests by isolating shared fixtures"`)

#### Scenario: Skill provenance is implicit
- **WHEN** the skill-creation job stores a new skill
- **THEN** `metadata.provenance` SHALL be `"implicit"`

### Requirement: Curation triggers Hindsight consolidation and refreshes mental models
The curation job SHALL no longer execute explicit merge / evolve / delete mutations. Observation refinement (consolidation of related facts, weakening of contradicted ones, strengthening of repeated ones) is performed by Hindsight's background pipeline driven by the `observations_mission` text configured at bank level. The curation job SHALL instead:

- Trigger a consolidation pass on its scheduled cadence when Hindsight has not already consolidated recent retains (via memory-service's consolidation-trigger endpoint, which calls Hindsight's `/consolidate`).
- Request Mental Model refreshes on a configured cadence.
- Emit structured logs of pre/post counts for observability.

#### Scenario: Curation triggers consolidation
- **WHEN** the curation job runs its scheduled cycle
- **THEN** it SHALL POST to memory-service (which proxies to Hindsight's consolidate endpoint) and log the operation ID

#### Scenario: Curation refreshes mental models
- **WHEN** the curation job runs a mental-model refresh cycle (configured cadence, default hourly)
- **THEN** it SHALL request a refresh for each configured Mental Model via memory-service, logging per-model outcome

#### Scenario: Curation does not issue merge / evolve / delete calls
- **WHEN** the curation job runs
- **THEN** it SHALL NOT issue merge, evolve, or delete calls to memory-service — those operations are performed by Hindsight internally via observation refinement

#### Scenario: Curation logs pre/post state
- **WHEN** the curation job completes a cycle
- **THEN** it SHALL log at INFO with `msg: "retro_curation_cycle"` and fields `{correlation_id, memories_before, observations_before, memories_after, observations_after, elapsed_ms}`

### Requirement: retro-agent registers with panel descriptor
retro-agent SHALL include a `dashboard_panel` descriptor in its existing registration payload. The descriptor SHALL have two sections: a form for retro policy (existing config_key `retro`) and an action section for manual job triggers.

#### Scenario: Panel descriptor sections
- **WHEN** retro-agent constructs its descriptor
- **THEN** the descriptor SHALL have `title: "Retro"`, `order: 320`, and sections:
  - form section with `config_key: "retro"` and fields:
    - `inactivity_timeout_s` (integer, min 10, default 300)
    - `skill_dup_threshold` (number, min 0, max 1, step 0.01, default 0.85)
    - `min_history_turns` (integer, min 1, default 4)
    - `curation_categories` (string, comma-separated, default `"identity,preferences,technical,home,ephemera"`)
    - `curation_recall_limit` (integer, min 1, default 15)
    - `mental_model_refresh_s` (integer, min 60, default 3600, description: "Cadence for Mental Model refresh; takes effect once mental-model support lands")
  - action section with three actions for manual triggers (see below)

#### Scenario: Action parameters for session ID
- **WHEN** the retro panel renders action buttons
- **THEN** each action SHALL include `params: [{name: "session_id", type: "string", required: true}]` so operators enter a session ID before triggering

### Requirement: Manual retro trigger actions
The retro panel's action section SHALL declare three actions targeting the existing `POST /v1/retro/{session}/trigger` endpoint with varying `job_type` values: `memory_extraction`, `skill_creation`, and `curation`.

#### Scenario: Memory extraction trigger
- **WHEN** the operator enters a session ID and clicks "Run Memory Extraction"
- **THEN** the dashboard SHALL POST `/v1/retro/{session_id}/trigger` with body `{"job_type": "memory_extraction"}`, display a success status on HTTP 200, and display the error on non-2xx

#### Scenario: Skill creation trigger
- **WHEN** the operator clicks "Run Skill Creation"
- **THEN** same as memory_extraction but with `job_type: "skill_creation"`

#### Scenario: Curation trigger
- **WHEN** the operator clicks "Run Curation"
- **THEN** same as above with `job_type: "curation"`

#### Scenario: Missing session ID blocks action
- **WHEN** the operator clicks a trigger button without entering a session ID
- **THEN** the dashboard SHALL disable the button until a non-empty session ID is entered (per the action's `params.required: true`)

### Requirement: Retro config extended with curation_recall_limit and mental_model_refresh_s
`RetroConfig` SHALL include `CurationRecallLimit` (int) and `MentalModelRefreshS` (int) fields. Defaults: 15 and 3600 respectively.

#### Scenario: Defaults applied
- **WHEN** `ResolveRetro` reads config and the new keys are absent
- **THEN** it SHALL apply the defaults

#### Scenario: Dashboard edits persist
- **WHEN** the operator saves the retro form with a new `curation_recall_limit`
- **THEN** `config:retro.curation_recall_limit` SHALL reflect the new value and subsequent retro-agent `ResolveRetro` reads SHALL see it

### Requirement: Extraction prompt is role-based
The retro-agent's in-code `extractionPrompt` SHALL refer to the subject of extraction using role-based phrasing (`the speaker`, `the person being served`). The prompt SHALL NOT hard-code any specific person's name as the default subject.

#### Scenario: Prompt free of hard-coded names
- **WHEN** the retro-agent is built from source
- **THEN** the `extractionPrompt` constant SHALL NOT contain a proper-noun reference to a specific person; references to the turn's subject SHALL use role terms

#### Scenario: Prompt receives speaker_id context
- **WHEN** the retro-agent invokes the LLM with the extraction prompt
- **THEN** the user message SHALL include the session's resolved `speaker_id` so the LLM can attribute facts correctly when multiple speakers appear in the same session

### Requirement: Skill creation carries speaker_id for audit
The skill-creation job SHALL emit `metadata.speaker_id` on retained skills, reflecting the session's speaker at skill-creation time. Skills SHALL continue to be recallable by any speaker (skills are operational knowledge, not personal facts), so the `speaker_id` metadata SHALL be used for audit/observability only and SHALL NOT be applied as an implicit recall filter.

#### Scenario: Skill retain includes speaker_id
- **WHEN** the skill-creation job stores a new skill
- **THEN** the retain request SHALL include `metadata.speaker_id`

#### Scenario: Skill recall ignores speaker scope
- **WHEN** context-manager or an agent recalls skills during context assembly
- **THEN** the recall query SHALL NOT filter by `speaker_id` (skills are shared across speakers)
