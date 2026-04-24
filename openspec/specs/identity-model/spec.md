### Requirement: Speaker identity axis
Every turn processed by the system SHALL carry an explicit `speaker_id` that identifies the person who uttered the turn. The `speaker_id` SHALL be a short, stable, case-insensitive string. It SHALL NOT be mutated after a turn is persisted.

#### Scenario: Request with explicit speaker_id
- **WHEN** a request arrives with `speaker_id="jason"` in the body
- **THEN** the system SHALL use `"jason"` as the speaker for that turn and for every downstream operation (context assembly, agent invocation, retro extraction, retain)

#### Scenario: Request without speaker_id and primary_user_id set
- **WHEN** a request arrives with no `speaker_id` and `config:memory.primary_user_id` is set to `"jason"`
- **THEN** the system SHALL resolve the speaker to `"jason"`

#### Scenario: Request without speaker_id and primary_user_id unset
- **WHEN** a request arrives with no `speaker_id` and `config:memory.primary_user_id` is unset
- **THEN** the system SHALL resolve the speaker to `"unknown"`, log a WARN-level event, and proceed

#### Scenario: Session inherits speaker_id from previous turn
- **WHEN** a request has `previous_response_id` pointing to a turn with `speaker_id="alice"` and the new request omits `speaker_id`
- **THEN** the system SHALL resolve the new turn's speaker to `"alice"`

#### Scenario: Explicit speaker_id overrides inheritance
- **WHEN** a request has `previous_response_id` from a turn with `speaker_id="alice"` and the new request sets `speaker_id="bob"`
- **THEN** the system SHALL use `"bob"` for the new turn (session-level speakers can change per turn)

### Requirement: Fact-type axis
Retained memories SHALL carry a `metadata.fact_type` classifying the nature of the fact. Valid values SHALL be `person_fact`, `world_fact`, `context_fact`, or `procedural_fact`. The system SHALL reject retain requests whose `fact_type` is outside this set.

#### Scenario: Valid fact_type accepted
- **WHEN** a `/retain` request includes `metadata.fact_type="person_fact"`
- **THEN** memory-service SHALL forward the value unchanged to Hindsight

#### Scenario: Invalid fact_type rejected
- **WHEN** a `/retain` request includes `metadata.fact_type="opinion"` (not one of the four valid values)
- **THEN** memory-service SHALL return HTTP 400 with a structured error body and SHALL NOT call Hindsight

#### Scenario: Missing fact_type defaulted
- **WHEN** a `/retain` request omits `metadata.fact_type`
- **THEN** memory-service SHALL apply a conservative default (`world_fact` unless content contains clear time-scoped cues or the `entities` array contains a non-class entity, in which case `context_fact` or `person_fact` is chosen respectively) and SHALL record the applied default in the request log

### Requirement: Three-axis separation
The system SHALL keep the three axes `speaker_id` (who said it), `entities` (who/what it is about), and `fact_type` (what kind of fact) independent. A single retained memory SHALL be able to carry any combination of the three, including `speaker_id` differing from the `entities` it references.

#### Scenario: Speaker talks about a third party
- **WHEN** Jason says "Alice prefers green tea" and the retro-agent extracts a memory
- **THEN** the retained memory SHALL have `metadata.speaker_id="jason"`, `entities=["alice","green tea"]`, and `metadata.fact_type="person_fact"`

#### Scenario: World fact has no speaker-specific subject
- **WHEN** the retro-agent extracts "Cats have fur" from a session
- **THEN** the retained memory SHALL have `metadata.fact_type="world_fact"`, `entities=["class:cat","fur"]` or similar, and `metadata.speaker_id` SHALL reflect the session's speaker (the axis remains populated even though the fact itself is speaker-agnostic for recall purposes)

### Requirement: Speaker resolution is request-time, not session-time
The `speaker_id` resolution precedence SHALL be evaluated at every request, in this order: (1) request body `speaker_id`, (2) `X-Speaker-ID` header, (3) session's stored speaker from the previous turn in the same chain, (4) `config:memory.primary_user_id`, (5) `"unknown"`.

#### Scenario: Body beats header
- **WHEN** a request has both `speaker_id="alice"` in the body and `X-Speaker-ID: bob` in headers
- **THEN** the resolved speaker SHALL be `"alice"`

#### Scenario: Header beats session inheritance
- **WHEN** a request has `X-Speaker-ID: alice`, `previous_response_id` pointing to a `speaker_id="bob"` turn, and no body field
- **THEN** the resolved speaker SHALL be `"alice"`

#### Scenario: primary_user_id is the final real fallback
- **WHEN** no body, header, or previous-turn speaker is available and `primary_user_id="jason"`
- **THEN** the resolved speaker SHALL be `"jason"`

### Requirement: Entities array supports class markers
The `entities` array on retain payloads SHALL accept strings prefixed with `class:` to denote class-level references (e.g. `class:cat`) as distinct from instance-level references (e.g. `fred`). Memory-service SHALL treat `class:`-prefixed entries as tags for class-membership recall and SHALL NOT attempt to resolve them as Hindsight entities.

#### Scenario: Class marker preserved
- **WHEN** a retain request includes `entities=["class:cat","fred"]`
- **THEN** memory-service SHALL forward both values, and the `class:cat` value SHALL be queryable via a `entities` recall filter matching `class:cat`

### Requirement: Missions and directives use role-based language
Mission text and directive text stored in Hindsight SHALL refer to speakers by role (`the speaker`, `the person being served`, `a household member`) rather than by proper name. No deployment-shipped mission or directive SHALL hard-code a specific person's name.

#### Scenario: Retain mission free of hard-coded names
- **WHEN** `deploy/memory/missions/retain_mission.yaml` is loaded
- **THEN** the `text` field SHALL NOT contain any proper-noun reference to a specific person (e.g. "Jason")

#### Scenario: Directives free of hard-coded names
- **WHEN** any file under `deploy/memory/directives/` is loaded
- **THEN** its `content` field SHALL NOT contain any proper-noun reference to a specific person

### Requirement: Identity model future-scope boundary
The identity model SHALL explicitly defer the following capabilities to separate future changes: semantic entity canonicalization (merging `User` with `Jason`), an operator-visible identity registry, source-adapter-specific speaker derivation, and class/instance inference at retain or reflect time.

#### Scenario: No entity merging in this change
- **WHEN** the identity model is implemented
- **THEN** memory-service SHALL NOT attempt to merge or alias entity IDs in Hindsight; separate entities for `User` and `Jason` SHALL remain separate unless manually merged by an operator through Hindsight's native UI
