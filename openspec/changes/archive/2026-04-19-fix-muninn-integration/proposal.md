## Why

microagent2's integration with Muninn was authored against assumed semantics that differ from Muninn's actual documented behavior. Reading the muninndb source at `../muninndb` verified: `concept` is a 3×-weight FTS field (not a category anchor), `tags` drive 2×-weight FTS (not semantic key-term storage), `confidence` is a multiplicative factor on the final retrieval score (hardcoded at 0.9 in microagent2), `memory_type` is a caller-settable enum we never set, `summary` can be passed to skip Muninn's LLM summarization (we ignore it), and the `/api/consolidate` and `/api/engrams/{id}/evolve` endpoints exist for the curation job that today generates action plans and silently drops them. The cold-start recall failure we observed live was a compounding consequence of misusing these fields — scoring is working as designed; our inputs are wrong.

## What Changes

- **Retro memory-extraction prompt (`internal/retro/job.go`)**: output schema changes from `{content, category, key_terms, directive}` to `{concept, content, summary, tags, memory_type, confidence}`.
  - `concept` is a specific sentence headline (e.g. `"Jason prefers dark roast coffee"`), not a category label like `"preference"`.
  - `tags` are likely-user-query words (`["coffee","caffeine","morning","drink"]`), not semantic key-terms for embedding match.
  - `memory_type` is one of the 12 values in Muninn's enum: `"fact"`, `"decision"`, `"observation"`, `"preference"`, `"issue"`, `"task"`, `"procedure"`, `"event"`, `"goal"`, `"constraint"`, `"identity"`, `"reference"`. When the LLM outputs an unrecognized value, the field is omitted from the write call and Muninn auto-classifies.
  - `confidence` is a float in [0.0, 1.0] reflecting the extraction's own certainty. No longer hardcoded.
  - `summary` is a one-line restatement of the memory; Muninn uses it in place of its own background LLM summarization.
- **Skill-creation prompt**: parallel update. Stored skills get the same enriched schema, with `memory_type: "procedure"` and concept as a headline like `"Approach for diagnosing flaky CI tests by isolating shared fixtures"`.
- **`internal/context/muninn.go`**: `MuninnClient.Store` signature expanded to accept `(concept, content, summary, tags, memoryType, confidence)`. New methods `Consolidate`, `Evolve`, `Delete`, `Link` added on the same client.
- **Curation job (`internal/retro/job.go` `CurationJob.Run`)**: parsed action entries are now executed against Muninn instead of logged and discarded.
  - `action: "merge"` → `POST /api/consolidate` with `{vault, ids:[...], merged_content}`. Originals are soft-deleted by Muninn (7-day recovery window).
  - `action: "evolve"` (new) → `POST /api/engrams/{id}/evolve` when one memory is a refined supersession of another. Muninn writes a `supersedes` association.
  - `action: "delete"` → `DELETE /api/engrams/{id}`.
  - Unknown or malformed actions are logged and skipped, not executed.
- **BREAKING (wire format to Muninn)**: new stores carry the enriched fields. Existing stored memories are unaffected (Muninn's schema is additive). No data migration required.

## Capabilities

### New Capabilities
- None.

### Modified Capabilities
- `retrospection`: adds requirements governing the structured extraction schema microagent2 sends to Muninn, the use of Muninn's `memory_type` enum, how `confidence` is populated, and that curation actions are executed against Muninn endpoints (not just logged).

## Impact

- **Cold-start recall**: the live-demonstrated "memories don't match food query at threshold 0.5" regression is resolved at its root. With correctly shaped tags and concept, `full_text_relevance` becomes non-zero on natural user queries, and `final` clears the default 0.5 threshold without gymnastics. This obsoletes the interim mitigations we'd considered (client-side semantic-only filter, warm-at-store self-recall).
- **Retrieval ranking quality**: `confidence` becomes a real multiplier on final score instead of a flat 0.9 handicap. `memory_type` gives Muninn a per-engram classification hook it can act on; retrieval-ranking behavior over time may shift (generally for the better — that's Muninn doing its job).
- **Curation becomes live**: the retro-agent's LLM-generated merge/delete/evolve actions start having real effects. Duplicate and stale memories begin to decline instead of accumulating forever.
- **Muninn-side LLM work**: passing `summary` skips Muninn's background summarization; passing `memory_type` skips its type classification. Net fewer LLM calls on the Muninn side per new memory.
- **Downstream consumers unaffected**: context-manager and assembler (both recently updated in `fold-memories-into-user-turn`) see no change — they still consume `Memory.Content` and related fields; the decoration is unchanged.
- **Known limitation, not fixed here**: Muninn's `AccessCount` remains 0 across all activations (Muninn's `RecordAccess` method exists on the engine adapter but no HTTP handler calls it). This flattens ACT-R's frequency weighting for microagent2. Not a microagent2 bug; documented in design.md as a Muninn-side follow-up.
- **Non-goals**: no change to `recall_threshold` default (stays 0.5 — it works correctly once inputs are fixed); no change to the context-manager's recall call or assembler (recent change stands); no client-side workarounds for Muninn scoring (unnecessary once inputs are correct); no escalation of the `AccessCount` dead-signal to Muninn maintainers as part of this change.
