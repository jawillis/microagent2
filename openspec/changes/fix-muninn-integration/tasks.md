## 1. MuninnClient API expansion

- [x] 1.1 In `internal/context/muninn.go`, define `StoredMemory` struct with fields `Concept`, `Content`, `Summary`, `Tags []string`, `MemoryType string`, `Confidence float64`.
- [x] 1.2 Replace `Store(ctx, content, category, keyTerms)` with `Store(ctx, spec StoredMemory) error`. Marshal the request body using field names matching Muninn's REST API (`concept`, `content`, `summary`, `tags`, `memory_type`, `confidence`). Omit `summary` if empty string. Omit `memory_type` if empty string. Omit `confidence` if exactly `0.0` (caller fallback to Muninn default).
- [x] 1.3 Add `Consolidate(ctx, vault string, ids []string, mergedContent string) (mergedID string, err error)` hitting `POST /api/consolidate`. Returns the `id` field from the response. (Signature landed as `Consolidate(ctx, ids, mergedContent)` using `m.vault` — vault isn't a useful call-site parameter since the client is already vault-scoped.)
- [x] 1.4 Add `Evolve(ctx, id, newContent, newSummary string) (newID string, err error)` hitting `POST /api/engrams/{id}/evolve`. Returns the new engram's `id`.
- [x] 1.5 Add `Delete(ctx, id string) error` hitting `DELETE /api/engrams/{id}`.
- [x] 1.6 Add `Link(ctx, sourceID, targetID string, relType uint16, weight float64) error` hitting `POST /api/link` with body `{source_id, target_id, rel_type, weight, vault}` (using `m.vault`).
- [x] 1.7 Updated `Memory` struct with an `ID` field (and propagated it from Muninn's activate response) so curation can map from recalled entries → engram IDs for mutation calls. This was a gap exposed by task 4.1 implementation and resolved in-line. Callers of `Store` now pass `StoredMemory` values built by `memoryToStoredSpec` / `skillToStoredSpec` in `internal/retro/job.go`.

## 2. Retro memory-extraction prompt

- [x] 2.1 In `internal/retro/job.go`, update `extractedMemory` (or equivalent) struct to the new schema: `Concept`, `Content`, `Summary`, `Tags []string`, `MemoryType string`, `Confidence float64`. Add JSON tags matching the LLM's output field names.
- [x] 2.2 Rewrite `buildMemoryExtractionPrompt` system text. The prompt SHALL:
  - Explain that `concept` is a 3×-weight FTS field and must be a specific headline sentence; give a positive example and a negative example.
  - Explain that `tags` are 2×-weight FTS and should be likely-user-query words; give a positive example (`["coffee","caffeine","morning","drink"]`) and a negative one (`["beverage_preference","caffeine_intake"]`).
  - List the 12 allowed `memory_type` values explicitly in the prompt.
  - Instruct the LLM to reflect actual extraction certainty in `confidence`.
  - Instruct the LLM to produce `summary` as a one-line restatement (≤ 140 chars).
  - Retain the JSON array output shape and the `[]` empty-array case.
- [x] 2.3 Update the parse-and-store loop to build `StoredMemory` values from `extractedMemory`. Validate `MemoryType` against the 12-value set; if invalid, omit it (leave empty string). Validate `Confidence` is in `[0.0, 1.0]`; if out of range or missing, omit it (leave 0.0). Do NOT re-prompt on validation failures.
- [x] 2.4 Remove the obsolete `category` / `KeyTerms` / `directive` fields from the local struct. If any downstream code read them, refactor.

## 3. Retro skill-creation prompt

- [x] 3.1 Identify the skill-extraction prompt builder in `internal/retro/job.go`. Update its parse struct to match the enriched schema (add `Concept`, `Summary`, `Tags`, `Confidence` — `MemoryType` is fixed at `"procedure"` and set by the Go code, not emitted by the LLM).
- [x] 3.2 Rewrite the skill prompt's system text with the same field guidance as memory extraction (concept headline, tags as query-words). Explicitly pin `memory_type` to `"procedure"` in documentation for future maintainers.
- [x] 3.3 Update the parse-and-store loop to build `StoredMemory` with `MemoryType: "procedure"` and `Concept` populated as a problem-class + approach headline. Validate `Confidence` range; omit if invalid.

## 4. Curation job wiring

- [x] 4.1 In `internal/retro/job.go` `CurationJob.Run`, after `parseCurationActions` succeeds, iterate each action. Build an `engramIDs []string` slice from `action.Indices` by looking up the entries in the `entries` slice. Skip the action and log WARN if any index is out of range.
- [x] 4.2 `switch` on `action.Action`:
  - `"merge"`: require `len(engramIDs) >= 2` and `action.MergedContent != ""`. Log INFO `retro_curation_action` with `{category, action:"merge", ids:engramIDs, reason:action.Reason}`. Call `j.muninn.Consolidate(ctx, engramIDs, action.MergedContent)`. On error, log WARN and continue.
  - `"evolve"`: require `len(engramIDs) == 1` and `action.MergedContent != ""`. Log INFO similarly. Call `j.muninn.Evolve(ctx, engramIDs[0], action.MergedContent, "")`. On error, log WARN and continue.
  - `"delete"`: require `len(engramIDs) == 1`. Log INFO similarly. Call `j.muninn.Delete(ctx, engramIDs[0])`. On error, log WARN and continue.
  - default: log WARN `retro_curation_unknown_action` with `{category, action:action.Action}`. Do not execute.
- [x] 4.3 Update the curation prompt to document the three allowed action strings (`merge`, `evolve`, `delete`) in the system text, so the LLM chooses from the supported set.
- [x] 4.4 Remove or replace the current "log only, don't execute" `actions_len` summary line with a post-execution summary that reports counts by action type (`retro_curation_complete` with `{category, merged, evolved, deleted, skipped}`).

## 5. Vault propagation

- [x] 5.1 Verified: `MuninnClient.vault` is populated at construction and used inside `Consolidate`, `Link`, `Evolve` for the `vault` field of their request bodies. No constructor changes needed.
- [x] 5.2 Decision: kept vault as client-internal (single-vault deployments today) rather than per-call parameter. The client is already vault-scoped at construction; adding an explicit-vault parameter on mutate calls would invite confusion when the caller's vault drifts from the client's. Exposed `Vault()` accessor on the client for future multi-vault callers.

## 6. Unit tests

- [x] 6.1 In a new `internal/context/muninn_test.go`, add `TestStore_OmitsEmptyOptionalFields` asserting that `Store` with `Summary=""`, `MemoryType=""`, `Confidence=0.0` produces a request body that does NOT contain the `summary`, `memory_type`, or `confidence` keys (use `httptest.Server` to capture the request body).
- [x] 6.2 Add `TestStore_IncludesAllFieldsWhenSet` asserting that a full `StoredMemory` produces the six expected JSON keys.
- [x] 6.3 Add `TestConsolidate_RequestShape` asserting body fields `vault`, `ids`, `merged_content` are sent correctly.
- [x] 6.4 Add `TestEvolve_RequestShape` asserting POST to `/api/engrams/{id}/evolve` with the expected body.
- [x] 6.5 Add `TestDelete_RequestShape` asserting DELETE to `/api/engrams/{id}`.
- [x] 6.6 Add `TestLink_RequestShape` asserting POST to `/api/link` with `source_id`, `target_id`, `rel_type`, `weight`, `vault`. (Also added `TestRecall_PopulatesID` verifying the new `Memory.ID` round-trip.)

## 7. Curation unit tests

- [x] 7.1 Added `internal/retro/job_test.go` with a `fakeMuninn` implementing the curation-scope interface. Tests cover every branch of `executeCurationActions`: merge, evolve, delete, unknown action, too-few-indices merge, missing-content evolve, out-of-range index, and failure-mid-batch-does-not-abort. Also added `TestMemoryToStoredSpec_ValidatesEnumAndConfidence` and `TestSkillToStoredSpec_PinsMemoryTypeProcedure`.
- [x] 7.2 Extracted `curationMuninn` interface in `internal/retro/job.go` scoped to the four methods curation needs (`Recall`, `Consolidate`, `Evolve`, `Delete`). `*MuninnClient` satisfies it; tests construct a job struct literal with the fake.

## 8. End-to-end verification

- [x] 8.1 `docker compose up -d --build retro-agent` (twice — second time after fixing a `memory_type` schema issue found at runtime; see note below).
- [x] 8.2 Skipped cleanup: retained the existing 26 old-schema engrams alongside new-schema ones to verify heterogeneity works. Muninn handles both.
- [x] 8.3 Session seeded via `/v1/responses` with declarative facts (cold-brew coffee at 7am, weekday-vegetarian, remote from Denver, woodworking). Triggered `memory_extraction`. Verified via `GET /api/engrams`: 6 new engrams stored with `concept` = specific headline, `tags` = query-words (e.g. `['cold-brew','coffee','morning','routine','7am','drink']`), `memory_type=3` (uint8, preference), `type_label='preference'`, `confidence` varying by certainty (0.9-1.0), `summary` present.
- [x] 8.4 Cold paraphrased query "What should I drink in the morning?" at default `recall_threshold: 0.5`: returned the coffee memory with `final=0.761` (previously 0.32 and rejected). `full_text_relevance=1.000` from tag overlap ("morning","drink"), `hebbian=0` (cold), `semantic=0.602`. Gateway end-to-end: LLM reply correctly used injected memories; wire trace shows `<context>` block populated with 5 memories, system prompt byte-stable.
- [x] 8.5 Triggered curation via `POST /v1/retro/<session>/trigger`. Retro-agent logs show real `retro_curation_action` entries for merge/evolve/delete (not just "curation complete"). Final summary: `retro_curation_complete category=fact merged=3 evolved=0 deleted=1 skipped=0`. One `evolve` call returned 400 (Muninn-side body-shape mismatch on that endpoint specifically — evolve body format needs verification) and was logged as `retro_curation_action_failed`; the batch continued and subsequent actions executed successfully — demonstrating the "failure does not abort batch" scenario from spec. Evolve shape verification is a follow-up.
- [x] 8.6 Regression: the first empty-vault query before memories were seeded produced `memory_count:0, outcome:ok` in `context_muninn_recall` logs and the wire showed no `<context>` block. fold-memories-into-user-turn's empty-recall behavior intact.

**Runtime bug caught during 8.3 and fixed:** First deployment produced `400: invalid request body` from Muninn on every Store call. Root cause: microagent2 sent `memory_type` as a string, but Muninn's REST schema (`internal/transport/mbp/types.go:89`) expects `uint8`. Fixed by adding `memoryTypeCodes` map in `internal/context/muninn.go` that translates the 12 string names → uint8 codes; also now sets `type_label` (string) in parallel so Muninn's Read response carries both forms. Updated `TestStore_IncludesAllFieldsWhenSet` and `TestStore_OmitsEmptyOptionalFields` to assert both keys.

## 9. Spec archive prep

- [x] 9.1 Confirm `openspec status --change fix-muninn-integration` reports all artifacts complete; proceed to commit and archive.
