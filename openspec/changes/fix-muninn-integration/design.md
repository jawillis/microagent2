## Context

After the `fold-memories-into-user-turn` change shipped, the intended symptom (memories not appearing per-turn) was resolved at the placement level — the assembler correctly folds recalled memories into the user turn. Live verification exposed a deeper problem: Muninn was returning zero memories for the obvious food query until we dropped `recall_threshold` from 0.5 to 0.1. That smelled like a Muninn calibration issue; a sub-agent review of the muninndb source at `/Users/jasonwillis/Projects/muninndb` corrected that assumption. Muninn's scoring is working as documented. microagent2 has been using Muninn's storage contract in conflict with that documentation, and the "missing recall" is the downstream consequence.

Key verified facts from the muninndb repo:

| Field | Muninn contract | microagent2 use today | Effect |
|---|---|---|---|
| `concept` | 3×-weight FTS field; should be a specific headline sentence | Hardcoded coarse labels: `"fact"`, `"preference"`, `"food_preference"`, `"skill"` | All engrams of a type share one FTS token. Queries matching real content lose to queries matching "fact"/"preference" by 3:1 weight. 92% orphan graph. |
| `tags` | 2×-weight FTS field; curated signals meant to be words a caller expects queries to contain | Retro prompt asks the LLM for "key_terms for embedding similarity search" — a category mistake. Output is typically `["food","diet"]`. | Tags contribute zero FTS on typical paraphrased queries. |
| `confidence` | Multiplier: `final = raw × confidence` (engine.go:1458-1471) | Hardcoded `0.9` on every Store call | Every score artificially discounted 10%. Uncertainty signal never transmitted. |
| `memory_type` | Enum 0-11 (fact, decision, observation, preference, issue, task, procedure, event, goal, constraint, identity, reference). Caller-settable; Muninn auto-classifies if omitted | Never set | Muninn runs a background LLM classify pass we could skip. |
| `summary` | Caller-settable; under `inline_enrichment: "caller_preferred"` (default) skips Muninn's LLM summarization | Never set | Muninn runs a background LLM summary pass we could skip. Also, retro already generates an ideal summary-shaped string. |
| `/api/consolidate` | Merges N engrams into one new one; soft-deletes originals (7-day recovery). Required body: `{vault, ids:[...], merged_content}` | Not called. Curation job generates `{action, indices, merged_content, reason}` plans and logs them. | Duplicates never consolidate; LLM work is wasted. |
| `/api/engrams/{id}/evolve` | Writes refined content and creates a `supersedes` association to the prior form | Not called | Single-predecessor refinements mis-handled if forced through consolidate (which doesn't create supersedes). |
| `/api/link` | Creates graph edge: `{source_id, target_id, rel_type (uint16), weight, vault}` | Not called; prior probes used wrong field names (`source`/`target`) and 500'd | Graph stays empty. `max_hops: 2` in config is a no-op. |
| `/api/activate` + `AccessCount` | `AccessCount` feeds ACT-R base-level (`n := AccessCount+1`) | `activate` does not increment `AccessCount` (Muninn-side bug); `RecordAccess` exists but no handler calls it | ACT-R degenerates to pure age decay. Not fixable from our side. |

The cold-start we demonstrated live:

```
Query: "What is something good to eat?"
Memory: "Jason loves spicy Thai and Sichuan cuisine"

FTS contribution (concept 3× + tags 2× + content 1×):
  concept "food_preference" × 3.0 × overlap(query, "food_preference")  = 0
  tags ["food","diet"]      × 2.0 × overlap(query, ["food","diet"])    = 0
  content                   × 1.0 × overlap(query, content)            = 0
  → full_text_relevance = 0

Semantic: 0.594
Hebbian (cold): 0
Confidence: 0.9
→ final ≈ 0.32, below threshold 0.5

If tags had been ["eat","dinner","meal","hungry","menu","restaurant"],
"eat" would hit with 2× weight → FTS normalized ≈ 0.5 → final > 0.7, crushes threshold.
```

Primary stakeholders: retrospection (the producer of stored memories), and by downstream consequence the context-manager recall path (unchanged, but now sees matching memories where before it saw none).

## Goals / Non-Goals

**Goals:**
- Retro-extraction produces Muninn-shaped engrams: concept is a headline, tags are query-words, memory_type is set from the enum, confidence is real, summary is provided.
- Cold memories retrieve on the first user query (no warm-up required) at the default `recall_threshold: 0.5`.
- Curation job's merge/evolve/delete actions execute against Muninn; the feature transitions from "LLM theater" to actually pruning the vault.
- `MuninnClient` provides typed helpers for every write/mutate endpoint we use, so no call site has to hand-marshal JSON.
- Spec captures the storage contract so future changes can't silently regress it (per-field, not just per-call-site).

**Non-Goals:**
- Changing `recall_threshold` or any recall-side config knob. With correct inputs, 0.5 is appropriate.
- Client-side re-ranking or semantic-only filter (earlier brainstorming option). Unnecessary.
- Warm-at-store self-recall (earlier brainstorming option). Unnecessary.
- Fixing the Muninn-side `AccessCount` dead-signal. We can document and escalate separately; can't patch from microagent2.
- Changes to context-manager, assembler, or any downstream recall consumer. The `fold-memories-into-user-turn` change stands.
- Back-populating the 26 existing engrams with the new schema. They were written under the old contract; Muninn's schema is additive, so they continue to work; they just have suboptimal tags/concept. Retro curation can clean them up over time via evolve.
- A live-data migration path. Muninn is additive; nothing to migrate.

## Decisions

### 1. Retro extraction output schema

**Decision:** The `memory_extraction` LLM prompt produces an array of objects:

```json
{
  "concept":     "string — specific sentence describing what the memory is about (headline)",
  "content":     "string — full statement, typically a declarative sentence",
  "summary":     "string — one-line restatement (≤ 140 chars), provided so Muninn skips its own LLM summarization",
  "tags":        ["string", ...],   // 3-8 words a user is likely to type when this memory would be relevant
  "memory_type": "one of: fact|decision|observation|preference|issue|task|procedure|event|goal|constraint|identity|reference",
  "confidence":  0.0-1.0
}
```

The prompt's instruction text explicitly explains the FTS weight of concept and tags, gives concrete examples of good vs. bad values for each field, and states the 12 valid `memory_type` values. When the LLM outputs an unrecognized `memory_type` or a missing/out-of-range `confidence`, the field is omitted from the write call (Muninn defaults apply: auto-classify, confidence=1.0).

**Rationale:** Matches Muninn's documented contract (`docs/engram.md`, `docs/feature-reference.md`). `memory_type` as a string in the prompt output is easier for the LLM than a numeric enum; we translate to Muninn's numeric/string form in the Go client.

**Alternatives considered:**
- *Keep `category` and pass it as `concept`.* Rejected — perpetuates the collision problem. The whole point is that concept needs to be specific per memory.
- *Send `category` as `memory_type` (string pass-through).* Close but the value space is wrong (`"context"` isn't in Muninn's enum); mapping is ambiguous.
- *Two LLM calls, one for content-extraction and one for tagging.* Doubles cost for no clear win; single prompt with a schema is sufficient.

### 2. Skill-creation output schema — parallel

**Decision:** Skills stored via the skill-creation job use the same enriched schema, with `memory_type: "procedure"` fixed and `concept` populated as a specific headline (e.g. `"Approach for resolving flaky CI tests by isolating shared fixtures"`).

**Rationale:** Skills ARE procedures in Muninn's taxonomy. Uniform treatment means the curation job can consolidate/evolve skills the same way it does facts and preferences.

### 3. MuninnClient signature

**Decision:** `Store` signature:

```go
func (m *MuninnClient) Store(ctx context.Context, spec StoredMemory) error

type StoredMemory struct {
    Concept    string
    Content    string
    Summary    string    // optional; empty string → not sent
    Tags       []string
    MemoryType string    // optional; "" → not sent
    Confidence float64   // 0.0 → fallback to pkg-level default, anything else passed through
}
```

New methods on `MuninnClient`: `Consolidate(ctx, ids []string, mergedContent string) (mergedID string, err error)`; `Evolve(ctx, id, newContent, newSummary string) (newID string, err error)`; `Delete(ctx, id string) error`; `Link(ctx, sourceID, targetID string, relType uint16, weight float64) error`.

**Rationale:** Struct argument keeps the call site readable when new optional fields are added later (entities, relationships, etc. per Muninn). The separate methods for mutation endpoints mirror Muninn's REST shape and give curation a clean call surface.

**Alternatives considered:**
- *Builder / options-func pattern.* Ergonomic but adds complexity. A struct with zero-values-are-defaults is enough for our usage.
- *Single generic `Mutate` method taking an action enum.* Obscures which endpoint is called from the call site; tests and logs become less readable.

### 4. Curation action execution

**Decision:** `CurationJob.Run` parses actions as today, validates each entry's `action` against the allowed set, then executes:

- `merge`: requires ≥ 2 indices and non-empty `merged_content`. Calls `Consolidate(ctx, engramIDs, merged_content)`.
- `evolve`: requires exactly 1 index and non-empty `merged_content` (reused field). Calls `Evolve(ctx, engramID, merged_content, "")`.
- `delete`: requires exactly 1 index. Calls `Delete(ctx, engramID)`.

Each action is logged at INFO before execution (`retro_curation_action` with fields `{category, action, ids, reason}`). Failures are logged at WARN but do not abort the remaining actions in the batch — one bad merge shouldn't prevent a subsequent delete.

Invalid / unknown actions are logged at WARN and skipped.

**Rationale:** Wire up the existing half-built feature minimally. The LLM-produced `reason` field already in the parse struct goes into logs so post-hoc auditing is possible.

**Alternatives considered:**
- *Dry-run mode gated by config.* Useful for confidence-building on a real vault, but the curation job already runs rarely and is log-observable. Adding config surface for a one-off rollout caution is scope creep; if we want that, gate externally (disable the retro trigger).
- *Require a `preview` step before executing.* Doubles the LLM roundtrips; curation's rarity makes that overkill.

### 5. Handling LLM validation failures

**Decision:** If the LLM's output for a single memory fails validation (e.g. `memory_type` not in enum, `confidence` out of range, `concept` empty), the ill-formed field is dropped (omitted on the Muninn call) or defaulted, and the rest of the memory is stored. We do not re-prompt.

If the entire output is unparseable JSON, the whole extraction batch is logged and dropped (unchanged from current behavior).

**Rationale:** Re-prompting doubles latency on rare edge cases; dropping a single field is strictly better than dropping the whole memory. The LLM's base quality on structured output is good; validation failures should be rare.

### 6. Supersedes association via evolve

**Decision:** When curation determines that one memory is a refined supersession of another (e.g. "Jason likes coffee" → "Jason likes dark roast coffee"), the action is `evolve` — which calls `POST /api/engrams/{old_id}/evolve` with the new content. Muninn writes a fresh engram and creates a `supersedes` association from the new to the old. The old engram remains discoverable via traversal but is not returned in default activation results.

**Rationale:** `consolidate` is for N→1 merges; `evolve` is for 1→1 refinements. Using consolidate for refinements loses the supersedes link and is a documented anti-pattern in `docs/feature-reference.md`.

### 7. Use Muninn's `summary` in assembled context (deferred)

**Considered but not in scope:** The sub-agent investigation noted that Muninn returns a `summary` field on recall that is often a better phrasing than `content` for LLM consumption. Rewriting the assembler to fold `summary` into the `<context>` block instead of `content` would be a useful follow-up, but:
- It requires also updating Muninn's activate response parsing in `internal/context/muninn.go`.
- Behavior risk is non-trivial (summary may be empty on some engrams depending on Muninn's enrichment queue).
- The `fold-memories-into-user-turn` change just landed and should stabilize before another assembler-adjacent change.

Flagged in design; revisit separately.

## Risks / Trade-offs

- [LLM produces tags that don't match user query patterns] → FTS underperforms vs. expectation. *Mitigation:* prompt explicitly says "words the user is likely to type"; include 2-3 worked examples in the prompt (good: `["coffee","caffeine","morning"]`; bad: `["beverage_preference","caffeine_intake"]`). Monitor by sampling `full_text_relevance` in `context_muninn_recall` logs post-rollout.
- [Curation deletes/merges something useful] → Vault loses information. *Mitigation:* consolidate soft-deletes (7-day recovery in Muninn); delete hard-deletes. Evaluate after rollout whether `delete` action should map to Muninn's soft-delete (Forget with `Hard: false`) instead. Log every action's `reason`.
- [`confidence` from LLM is noise] → Confidence distribution ends up narrow (0.85-0.95) in practice, defeating the multiplicative. *Mitigation:* accept as a small regression; in practice any variance > 0 is better than hardcoded 0.9. Revisit if ranking quality issues surface.
- [`memory_type` enum drift between Muninn versions] → Muninn adds a new type and our prompt's list goes stale. *Mitigation:* the list is a hardcoded constant in `internal/retro/`; a one-liner change. Validate against a test that references `../muninndb/docs/feature-reference.md` or (better) the Muninn proto/enum file.
- [Evolve via curation on unrelated memories] → Creates a false supersedes link. *Mitigation:* curation's LLM prompt already clusters by category; the `evolve` action requires exactly one index from the same category's recalled batch. Cross-category evolve is not representable in the current action format.
- [Muninn-side `AccessCount` dead-signal obscures performance measurement] → Can't use Muninn's own frequency stats for A/B comparison. *Mitigation:* log `memory_count` in `context_muninn_recall` (already done) and compute a rolling "hit rate at threshold 0.5" client-side from those logs.

## Migration Plan

1. Merge this change.
2. `docker compose up -d --build retro-agent context-manager`. The MuninnClient lives in `internal/context/` and is linked into both services (retro-agent writes via Store/mutate methods; context-manager reads via Recall — unchanged path).
3. Verify a fresh retro extraction produces the new schema: tail retro-agent logs for `memory_extracted` events; confirm `concept`, `tags`, `memory_type` fields are populated.
4. Verify recall: send a cold-topic user query (a subject whose extracted memories have never been recalled) and confirm `context_muninn_recall` logs non-zero `memory_count` and the assembled `<context>` block is populated. Symptom resolved at default threshold 0.5.
5. Trigger a curation run via `POST /v1/retro/<session>/trigger` with `job_type: "curation"`. Confirm `retro_curation_action` logs show real endpoint calls (not just "curation complete").

Rollback: revert the merge, redeploy. No data changes to roll back — new-schema engrams written after this change continue to work under old-schema expectations (additive schema); the old schema continues to work too. Worst case is degraded retrieval quality on engrams written during the brief forward-window, which self-heals over time.

## Open Questions

- Should we offer a one-off backfill job to re-tag the existing 26 engrams (old-schema) under the new schema? Against: Muninn handles heterogeneity fine; natural curation will refine them. For: operator might want a clean vault to evaluate the change. Defer unless requested.
- Should `delete` action in curation become soft-delete by default (use Forget with `Hard: false`) instead of hard DELETE? Lower blast radius; Muninn exposes this as a knob. Initial implementation uses hard DELETE to match the current endpoint we already discovered; switch via a one-line change if desired.
- Once Muninn's `RecordAccess` is reachable from HTTP (or the `activate` handler starts incrementing `AccessCount`), should microagent2 add explicit access-recording on its side as a transitional step? Probably yes; out of scope here.
