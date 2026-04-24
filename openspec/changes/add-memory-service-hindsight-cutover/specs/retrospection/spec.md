## MODIFIED Requirements

### Requirement: Structured extraction schema for stored memories
The memory-extraction job SHALL produce memories shaped for memory-service's `/retain` endpoint, relying on Hindsight's entity extraction and fact-type classification rather than supplying those fields. The retro agent SHALL pass the following fields per memory to memory-service `/retain`:

- `content`: the full-sentence statement of the memory (e.g. `"Jason prefers dark roast coffee"`). Phrased specifically enough to be independently useful when surfaced.
- `tags`: 3-8 short strings, each a word the user is likely to type in a query where this memory should be recalled (e.g. `["coffee","caffeine","morning","drink"]`). Tags SHALL NOT be semantic-category words (`["beverage_preference"]`). Tags SHOULD include at least one taxonomy tag from the initial set (`identity`, `preferences`, `technical`, `home`, `corrections`, `inferred`, `ephemera`).
- `metadata.provenance`: one of `explicit` (user said directly), `implicit` (observed from behavior), `inferred` (agent-derived), or `researched` (web source). The retro agent SHALL emit `explicit` for directly-stated memories and `implicit` for behavior-observed memories.
- `metadata.confidence`: float in `[0.0, 1.0]` serialized as a string, reflecting the extraction's self-reported certainty. The value SHALL NOT be hardcoded.
- `context`: optional source description (e.g. `"retro extraction from session <id>"`).
- `observation_scopes`: `"per_tag"` so that consolidation produces per-tag observations.

The retro agent SHALL NOT supply `memory_type`, `vault`, `summary`, or any muninn-era fields; Hindsight's `retain_mission` performs the analogous classification internally and stores fact-type on the retained memory.

#### Scenario: Content is a specific headline
- **WHEN** the memory-extraction job stores a memory about a user's food allergy
- **THEN** the `content` field SHALL be a specific sentence like `"Jason is allergic to shellfish"`, NOT a category like `"fact"` or `"food_preference"`

#### Scenario: Tags are likely user-query words
- **WHEN** the memory-extraction job stores a memory about the user's coffee preference
- **THEN** the `tags` field SHALL contain words a user would naturally type in a related query (e.g. `["coffee","morning","caffeine","preferences"]`), NOT semantic-category labels

#### Scenario: Provenance tagged on every retain call
- **WHEN** the memory-extraction job calls memory-service `/retain`
- **THEN** the request `metadata.provenance` field SHALL be one of `explicit`, `implicit`, `inferred`, or `researched`

#### Scenario: Confidence reflects extraction certainty
- **WHEN** the memory-extraction job stores a memory with strong evidence in the conversation
- **THEN** the `metadata.confidence` field (string-encoded) SHALL be close to `"1.0"`; when the evidence is ambiguous or inferred, the value SHALL be lower

#### Scenario: Observation scopes set to per-tag
- **WHEN** the memory-extraction job calls memory-service `/retain`
- **THEN** the `observation_scopes` field SHALL be `"per_tag"` so that Hindsight produces one observation set per tag

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

## REMOVED Requirements

### Requirement: Curation actions execute against Muninn endpoints
**Reason**: Muninn is being removed. Explicit curation mutations (merge / evolve / delete) are subsumed by Hindsight's observation refinement, driven by the `observations_mission` text. Keeping the explicit-mutation requirement would duplicate Hindsight's built-in behavior and re-introduce the muninn-era failure modes this cutover exists to eliminate.

**Migration**: Remove all curation-action execution code from `internal/retro/job.go`. The curation LLM's action-emitting behavior is retired in favor of Hindsight's `observations_mission` driving consolidation implicitly. See the replacement requirement `Curation triggers Hindsight consolidation and refreshes mental models` above.
