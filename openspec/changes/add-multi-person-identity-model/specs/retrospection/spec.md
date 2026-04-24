## MODIFIED Requirements

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

## ADDED Requirements

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
