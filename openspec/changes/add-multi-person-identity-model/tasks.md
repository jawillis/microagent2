## 1. Config surface

- [x] 1.1 Add `primary_user_id` (string, optional) and `recall_default_speaker_scope` (enum: `any`|`primary`|`explicit`, default `any`) to `internal/config/memory.go` and the memory-service panel descriptor.
- [x] 1.2 Add `ValidRecallSpeakerScope` helper and validation in `internal/config/resolve.go`.
- [x] 1.3 Extend memory-service panel (`internal/memoryservice/panel.go`) with the two new fields; readonly bank_id unaffected.
- [x] 1.4 Add optional `identity_name_denylist` (string, comma-separated) to `config:memory` for the startup warning check; default empty.

## 2. Payloads and plumbing

- [x] 2.1 Extend `messaging.ContextAssembledPayload` with `SpeakerID string` (omitempty JSON).
- [x] 2.2 Extend `messaging.RetainRequest` (if typed) or the in-flight retain body shape to carry `metadata.speaker_id` and `metadata.fact_type`.
- [x] 2.3 Extend `messaging.RecallRequest` with optional `SpeakerID`, `Entities []string`, `FactTypes []string`.
- [x] 2.4 Extend session record (in `internal/response/store.go` or equivalent) with `LastSpeakerID string` (omitempty); add getter/setter; no migration of pre-existing keys.
- [x] 2.5 Extend stored response object with per-turn `SpeakerID`; persist on write, read on chain walk.

## 3. Gateway speaker resolution

- [x] 3.1 Add `speaker_id` to chat/completions and Responses request structs in `internal/gateway/*`.
- [x] 3.2 Add `resolveSpeakerID(req, prevResponse, cfg) string` helper implementing the 5-step precedence (body → header → previous-turn → primary_user_id → "unknown").
- [x] 3.3 Apply resolved speaker on the published gateway-requests stream payload and on the stored response object.
- [x] 3.4 Emit `X-Speaker-ID` response header (including `"unknown"` case) for streaming and non-streaming paths.
- [x] 3.5 Log WARN `gateway_speaker_unknown` when resolution lands on `"unknown"`, tagged with correlation_id.

## 4. Context manager

- [x] 4.1 Read `SpeakerID` from the inbound payload, thread into recall call per `recall_default_speaker_scope`.
- [x] 4.2 On scope `explicit` + unknown speaker, skip recall and log `context_recall_skipped_no_speaker`.
- [x] 4.3 Emit outbound `ContextAssembledPayload.SpeakerID`.
- [x] 4.4 Unit tests covering all three scope × speaker combinations.

## 5. Memory-service retain

- [x] 5.1 Add `resolveSpeakerID` on the retain path: body → `primary_user_id` → `"unknown"`.
- [x] 5.2 Validate `metadata.fact_type` against the four-value enum; HTTP 400 on invalid.
- [x] 5.3 Implement conservative default algorithm: `context_fact` on time-scoped content cues, `person_fact` when `entities` contains a non-class entity, `world_fact` otherwise.
- [x] 5.4 Increment an in-process counter on `speaker_id="unknown"` retains; expose via the memory-service panel status section.
- [x] 5.5 Unit tests for retain speaker/fact_type paths.

## 6. Memory-service recall

- [x] 6.1 Translate optional `speaker_id` to a Hindsight metadata filter on the recall request.
- [x] 6.2 Translate optional `entities` and `fact_types` to appropriate Hindsight filters.
- [x] 6.3 Apply `recall_default_speaker_scope` when `speaker_id` is omitted; return HTTP 400 on scope `explicit` + missing speaker_id.
- [x] 6.4 Unit tests for all filter paths.

## 7. Missions and directives

- [x] 7.1 Rewrite `deploy/memory/missions/retain_mission.yaml` in role-based language; remove all "Jason" references.
- [x] 7.2 Rewrite `deploy/memory/missions/observations_mission.yaml` same way.
- [x] 7.3 Rewrite `deploy/memory/missions/reflect_mission.yaml` same way.
- [x] 7.4 Rewrite `deploy/memory/directives/01-user-subjective-authority.yaml` using "the authoring speaker" phrasing.
- [x] 7.5 Rewrite `deploy/memory/directives/04-inferred-memories-require-ratification.yaml` same way.
- [x] 7.6 Verify startup `memory_service_bank_synced` log shows updated text against a fresh Hindsight bank.

## 8. Memory-service startup guardrail

- [x] 8.1 On startup YAML load, scan all mission and directive text for entries in `identity_name_denylist`; log WARN `identity_hardcoded_name_detected` per hit; do not block sync.

## 9. Retro-agent extraction

- [x] 9.1 Rewrite `extractionPrompt` in `internal/retro/*` using role-based phrasing; remove "Jason" literal.
- [x] 9.2 Thread the session's `speaker_id` into the prompt's user-message context.
- [x] 9.3 Extend the extraction JSON schema (or parsing) to accept `speaker_id` and `fact_type` per memory.
- [x] 9.4 Emit `metadata.speaker_id` and `metadata.fact_type` on every `/retain` call produced by the memory-extraction job.
- [x] 9.5 Emit `metadata.speaker_id` on skill retention; SKIP speaker filter on skill recall (skills are cross-speaker).
- [x] 9.6 Unit tests covering speaker-about-self, speaker-about-other, and world-fact extraction cases.

## 10. Session endpoints

- [x] 10.1 Include `last_speaker_id` in `GET /v1/sessions` when present.
- [x] 10.2 Include per-turn `speaker_id` in `GET /v1/sessions/:id` turns when present.
- [x] 10.3 Leave pre-change session records untouched (no backfill); tests verify the field is absent on legacy records.

## 11. Docs and dashboard

- [x] 11.1 Update `docs/memory-system-design.md` with a "Multi-speaker" section linking to the identity-model spec.
- [x] 11.2 Add a `Speaker ID` form field to the memory-service panel so operators can see the resolved `primary_user_id`.
- [x] 11.3 Surface the unknown-speaker counter in the memory-service panel status section.
- [x] 11.4 Save a memory note pointing at the three-axis retain shape for future sessions.

## 12. Integration tests and rollout

- [x] 12.1 End-to-end compose test: request with explicit `speaker_id="alice"` produces a retained observation with `metadata.speaker_id="alice"` in Hindsight.
- [x] 12.2 End-to-end test: recall with `speaker_id="alice"` returns Alice's facts and excludes Jason's.
- [x] 12.3 End-to-end test: request with no speaker_id and `primary_user_id` unset produces `speaker:unknown`-tagged retain; gateway returns `X-Speaker-ID: unknown`.
- [x] 12.4 End-to-end test: third-party attribution ("Jason: Alice likes green tea") lands as `speaker_id=jason`, `entities=[alice,...]`, `fact_type=person_fact`.
- [x] 12.5 Run `go test ./...` with all unit tests green.
- [x] 12.6 Update CLAUDE.md or project README with a pointer to the identity-model spec.
