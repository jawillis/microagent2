## MODIFIED Requirements

### Requirement: HTTP API for memory operations
The memory-service SHALL expose an HTTP API at a configurable address (`MEMORY_SERVICE_HTTP_ADDR`, default `:8083`) with the following endpoints: `POST /retain`, `POST /recall`, `POST /reflect`, `POST /forget`, `POST /hooks/hind/retain`, `POST /hooks/hind/consolidation`, and `GET /health`.

#### Scenario: Retain accepts a memory item
- **WHEN** a client POSTs a JSON body with `content`, optional `tags`, optional `metadata`, optional `context`, optional `entities`, optional `observation_scopes`, and optional `metadata.speaker_id` / `metadata.fact_type` to `/retain`
- **THEN** memory-service SHALL apply provenance defaulting, resolve speaker_id, validate fact_type, call Hindsight's retain endpoint, and return HTTP 200 with the Hindsight retain response body

#### Scenario: Recall returns memory summaries
- **WHEN** a client POSTs a JSON body with `query`, optional `limit`, optional `tags`, optional `types` (defaulting from config), optional `speaker_id`, optional `entities`, and optional `fact_types` to `/recall`
- **THEN** memory-service SHALL call Hindsight's recall endpoint with the appropriate tag/metadata filters derived from the request and return HTTP 200 with a JSON body containing `memories` (the results array translated to a memory-service shape) and, when observation-type results are present, `source_facts`

#### Scenario: Reflect performs synthesis
- **WHEN** a client POSTs a JSON body with `query` and optional `speaker_id` to `/reflect`
- **THEN** memory-service SHALL call Hindsight's reflect endpoint for the configured bank, scoping the reflection to the given `speaker_id` when present, and return the synthesized response

#### Scenario: Forget deletes a memory
- **WHEN** a client POSTs a JSON body with either `memory_id` or `query` (best-match) to `/forget`
- **THEN** memory-service SHALL resolve the target memory ID (by direct ID or by recall-best-match) and call Hindsight's DELETE /memories/{id}, returning HTTP 200 on success

#### Scenario: Health endpoint returns service state
- **WHEN** a client GETs `/health`
- **THEN** memory-service SHALL return HTTP 200 with a JSON body `{"status": "ok", "hindsight": "reachable", "bank": "<configured-bank-id>"}` when Hindsight is reachable; HTTP 503 with `hindsight: "unreachable"` otherwise

## ADDED Requirements

### Requirement: Speaker_id resolution and tagging on retain
The memory-service SHALL resolve `metadata.speaker_id` on every `/retain` request. The resolution order SHALL be: (1) the `metadata.speaker_id` in the request, (2) `config:memory.primary_user_id`, (3) the literal string `"unknown"`. The resolved value SHALL be forwarded to Hindsight as `metadata.speaker_id`.

#### Scenario: Caller-specified speaker_id preserved
- **WHEN** a `/retain` request has `metadata.speaker_id="alice"`
- **THEN** memory-service SHALL forward `metadata.speaker_id="alice"` to Hindsight unchanged

#### Scenario: Missing speaker_id with primary_user_id set
- **WHEN** a `/retain` request omits `metadata.speaker_id` and `config:memory.primary_user_id="jason"`
- **THEN** memory-service SHALL set `metadata.speaker_id="jason"` before forwarding

#### Scenario: Missing speaker_id without primary_user_id
- **WHEN** a `/retain` request omits `metadata.speaker_id` and `config:memory.primary_user_id` is unset
- **THEN** memory-service SHALL set `metadata.speaker_id="unknown"`, log a WARN with `msg="retain_speaker_unknown"`, and proceed

#### Scenario: Unknown-speaker counter incremented
- **WHEN** a `/retain` is resolved to `speaker_id="unknown"`
- **THEN** memory-service SHALL increment an internal counter surfaced in its dashboard panel status section

### Requirement: Fact_type validation on retain
The memory-service SHALL validate `metadata.fact_type` on every `/retain` request against the set `{person_fact, world_fact, context_fact, procedural_fact}`. Missing fact_type SHALL be auto-defaulted per the algorithm defined in the identity-model spec. Invalid fact_type SHALL result in HTTP 400.

#### Scenario: Valid fact_type preserved
- **WHEN** a `/retain` request has `metadata.fact_type="person_fact"`
- **THEN** memory-service SHALL forward the value unchanged

#### Scenario: Invalid fact_type rejected
- **WHEN** a `/retain` request has `metadata.fact_type="opinion"`
- **THEN** memory-service SHALL return HTTP 400 with a structured error body and SHALL NOT call Hindsight

#### Scenario: Missing fact_type defaulted and logged
- **WHEN** a `/retain` request omits `metadata.fact_type`
- **THEN** memory-service SHALL apply a default and SHALL log the applied default at DEBUG with `msg="retain_fact_type_defaulted"` and the resolved value

### Requirement: Recall filters for speaker_id, entities, and fact_types
The memory-service SHALL accept optional `speaker_id`, `entities`, and `fact_types` fields on `/recall` requests and SHALL translate them into Hindsight-compatible filters before querying the bank.

#### Scenario: speaker_id scopes results
- **WHEN** a `/recall` request has `speaker_id="jason"`
- **THEN** memory-service SHALL pass a filter to Hindsight that restricts results to memories whose `metadata.speaker_id` equals `"jason"`

#### Scenario: entities filter matches entity references
- **WHEN** a `/recall` request has `entities=["fred"]`
- **THEN** memory-service SHALL pass a filter to Hindsight that restricts results to memories whose `entities` array contains `"fred"`

#### Scenario: fact_types filter
- **WHEN** a `/recall` request has `fact_types=["person_fact","context_fact"]`
- **THEN** memory-service SHALL pass a filter to Hindsight that restricts results to memories whose `metadata.fact_type` is one of the listed values

#### Scenario: All filters omitted
- **WHEN** a `/recall` request omits all new filters
- **THEN** memory-service SHALL apply `config:memory.recall_default_speaker_scope` (see separate requirement) and otherwise behave as it did before this change

### Requirement: recall_default_speaker_scope config
The memory-service SHALL read `config:memory.recall_default_speaker_scope` at request time. Valid values SHALL be `any`, `primary`, or `explicit`. The default SHALL be `any` (no implicit scoping).

#### Scenario: Scope `any` applies no default filter
- **WHEN** `recall_default_speaker_scope="any"` and a `/recall` request omits `speaker_id`
- **THEN** memory-service SHALL NOT add a speaker filter

#### Scenario: Scope `primary` applies primary_user_id filter
- **WHEN** `recall_default_speaker_scope="primary"`, `primary_user_id="jason"`, and a `/recall` request omits `speaker_id`
- **THEN** memory-service SHALL add a filter restricting results to `metadata.speaker_id="jason"`

#### Scenario: Scope `explicit` requires caller to specify
- **WHEN** `recall_default_speaker_scope="explicit"` and a `/recall` request omits `speaker_id`
- **THEN** memory-service SHALL return HTTP 400 with a structured error body identifying the missing field

### Requirement: primary_user_id config
The memory-service SHALL read `config:memory.primary_user_id` at request time. The value SHALL be either a non-empty string (single-user deployment) or unset (multi-user or anonymous). No other values SHALL be valid.

#### Scenario: primary_user_id set
- **WHEN** `config:memory.primary_user_id="jason"`
- **THEN** memory-service SHALL use `"jason"` as the speaker_id fallback on retain and (when scope is `primary`) on recall

#### Scenario: primary_user_id unset
- **WHEN** `config:memory.primary_user_id` is unset or an empty string
- **THEN** memory-service SHALL NOT substitute a fallback speaker_id and SHALL rely on the `"unknown"` tag path

### Requirement: Missions and directives reference the identity-model capability
The memory-service startup sync SHALL load mission and directive YAML files that conform to the identity-model requirement "Missions and directives use role-based language". The service SHALL surface a startup error if any loaded YAML contains a hard-coded proper-noun person reference detected by the configured name-check list.

#### Scenario: Clean missions pass startup
- **WHEN** all files under `deploy/memory/missions/` and `deploy/memory/directives/` use role-based language
- **THEN** startup SHALL complete and Hindsight SHALL receive the synced content

#### Scenario: Hard-coded name triggers startup warning
- **WHEN** any mission or directive text contains an entry from the operator-configured "hardcoded name denylist" (optional, via `config:memory.identity_name_denylist`; default empty)
- **THEN** memory-service SHALL log at WARN with `msg="identity_hardcoded_name_detected"` and the offending file, but SHALL proceed with sync
