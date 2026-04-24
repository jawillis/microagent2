# Homegrown Memory System — Design Notes

> **⚠️ SUPERSEDED 2026-04-22.** The Postgres+pgvector approach described
> here was explored and then set aside after research into Hindsight
> (Vectorize) showed it covers the memory substrate with a cleaner
> customization surface. **See `docs/memory-system-design.md` for the
> current design.** The core requirements, P0/P1/P2 blind spot list,
> and write-time-phrasing insights in this document carry forward and
> are referenced from the new one. The specific schema and SQL-CTE
> retrieval approach are not being built.

Captured from an exploration session on 2026-04-21 that started as debugging a
retro-agent consolidation issue and ended in a decision to replace muninndb
with an in-house memory substrate. These notes are pre-proposal — they
capture the thinking so an OpenSpec change can be drafted cleanly when ready.

## 1. Why not muninndb

Issues observed in a single evening of use against a running stack:

- `ReadResponse` has no `vault` field — UI can't show vault on a memory
- UI renders numeric `state` (`uint8`) against a `<select>` of string options — every engram displays "planning" regardless of actual state
- `/api/consolidate` hardcodes the output engram's `concept` as the literal string `"Consolidated memory"`, wasting the 3× FTS weight on the highest-value field
- `/api/explain` returns `components: { ...all zero... }` with a non-zero `final_score` — scoring is opaque from the outside
- `AccessCount` is always 0 because no HTTP path calls `RecordAccess` — 5% of the score weight is dead
- `/api/engrams/{id}/evolve` requires `new_content` and `reason`; the shape the SDK/earlier docs implied was `content` and `summary` — every evolve call silently 400'd until this was fixed
- Link weight sent as `1.0` comes back from the server at ~0.05 — unexplained transformation
- No reverse-edge query; incoming-edge detection requires scanning the whole pool

Retrieval ranking produced surprises that couldn't be debugged due to the
observability bugs above. "coffee" surfaced woodworking-tool engrams at the
top of the result set; the actual coffee-ritual engram ranked #20.

The cognitive-architecture framing (ACT-R, Hebbian rescue, CGDN gating) is
distinctive, but it's an opinionated blob that's difficult to unpick when
individual pieces misbehave. The user's actual needs can be met without
buying the whole theory.

## 2. Core requirements (from user)

1. **Old useless memories fade.** Decay speed is topic-dependent (faster for
   observations, slower for identity-level facts, never for certain
   constraints).
2. **Episodic/associative linking.** "When I was a kid a seagull stole my
   hamburger, so when someone mentions seagulls I think about hamburgers."
   Co-occurrence and shared-entity edges carry information the embedding
   space does not.
3. **Pattern induction / inference.** Mine past conversations for patterns
   and make logical leaps. "Jason responded negatively to multiple cars
   whose only common attribute was they were all red; Jason might not like
   red cars."
4. **Confidence with topic-dependent fade.** Confidence is per-memory and
   decays over time at a rate that depends on the memory's subject/type.

Non-negotiable performance constraint: **no extra LLM calls on the user's
critical path.** Extraction, enrichment, and curation all run asynchronously
in the retro-agent. Hot-path retrieval is pure SQL and vector math.

## 3. Substrate: Postgres + pgvector + tsvector

Rejecting muninndb. Rolling our own on Postgres. Rationale:

- Every knob is ours to tune
- Full observability — it's just SQL
- No opaque weight transforms, no dead fields
- Mature tooling (indexes, explain plans, backup, migration)
- Two-year-old bugs found by thousands of other users, not us

Rough schema:

```sql
CREATE TABLE memories (
  id             UUID PRIMARY KEY,
  vault          TEXT NOT NULL,
  concept        TEXT NOT NULL,          -- 3× FTS weight
  content        TEXT NOT NULL,          -- 1× FTS weight
  summary        TEXT,
  tags           TEXT[],                 -- 2× FTS weight
  memory_type    TEXT NOT NULL,          -- enum: fact/preference/.../identity
  confidence     REAL NOT NULL,          -- 0.0-1.0
  salience       REAL NOT NULL DEFAULT 0.5,
  stability      REAL NOT NULL,          -- decay slowness factor
  provenance     TEXT NOT NULL,          -- 'explicit' | 'implicit' | 'inferred'
  created_at     TIMESTAMPTZ NOT NULL,   -- memory written
  event_at       TIMESTAMPTZ,            -- event described occurred (if applicable)
  last_access    TIMESTAMPTZ,
  access_count   INTEGER NOT NULL DEFAULT 0,
  embedding      VECTOR(768),
  fts_vector     TSVECTOR GENERATED ALWAYS AS (
                   setweight(to_tsvector('english', coalesce(concept,'')), 'A') ||
                   setweight(to_tsvector('english', array_to_string(tags,' ')), 'B') ||
                   setweight(to_tsvector('english', coalesce(content,'')), 'C')
                 ) STORED
);

CREATE INDEX ON memories USING gin(fts_vector);
CREATE INDEX ON memories USING hnsw(embedding vector_cosine_ops);
CREATE INDEX ON memories (vault, memory_type);

CREATE TABLE associations (
  from_id        UUID NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  to_id          UUID NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  rel_type       TEXT NOT NULL,          -- same_episode / mentions / derives_from / supersedes / contradicts / ...
  asserted_strength REAL NOT NULL,       -- 0.0-1.0, caller's confidence in the relationship
  co_count       INTEGER NOT NULL DEFAULT 1,  -- hebbian-like: times this edge has been reinforced
  last_reinforced_at TIMESTAMPTZ NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (from_id, to_id, rel_type)
);
```

Soft-delete via a `deleted_at TIMESTAMPTZ` column plus a partial index
excluding it; restore by clearing the field.

## 4. Retrieval reach solved at write time

The core retrieval insight: "i want something to drink" should surface
"Jason prefers dark roast coffee" *without* an extra LLM call to expand
the query. Fix at the write side of the asymmetry.

### Concept phrasing

Memory extraction prompt instructs the LLM to phrase `concept` so it
naturally contains a category word where one fits:

```
- Before: "Jason prefers dark roast coffee"
- After:  "Jason's preferred drink is dark roast coffee"
```

Now `"drink"` is in the 3× FTS field. The user's natural-language query
hits directly.

### Three-layer tag taxonomy

Tags split into three explicit layers; the extraction prompt asks for all
three and gives a multi-domain example:

```
1. specific     — exact nouns from the fact   (coffee, roast, Moonlander)
2. hypernym     — general class               (drink, keyboard, hobby)
3. activation   — query words someone would type when they'd want this
                  (thirsty, type faster, RSI)
```

### Optional static hypernym pass (no LLM)

For high-frequency domains, a deterministic post-processor expands tags:

```go
var hypernyms = map[string][]string{
  "coffee": {"drink", "beverage", "caffeine"},
  "tea":    {"drink", "beverage"},
  "wine":   {"drink", "alcohol"},
  // ...
}
```

Runs in the retro-agent right before `Store`. Floor of reach for known
categories; prompt handles the long tail.

## 5. Second-order / associative reach

"Beach" → "seagull stole my hamburger when I was 6" → "hamburgers are my
favorite food." The chain is not in the embedding space (beach and
hamburger are not semantic neighbors); it's in the **memory graph**.

### Approach: read-time graph traversal with hop decay

Chosen over write-time embedding blending. Three reasons:

- **Explainability.** The chain is literally visible at query time — we
  can log and display the path that surfaced a result. Embedding blending
  is opaque.
- **Specificity preservation.** Blending dilutes each memory's vector
  toward the vault mean. Every memory loses distinctness as the graph
  densifies. Traversal keeps each memory's identity pristine.
- **Pattern induction needs distinct nodes.** The "3 red cars → dislikes
  red cars" inference requires observations to remain clearly separable.
  Blended vectors smear data points together; traversal preserves them.

### Query shape

```sql
WITH RECURSIVE direct AS (
  SELECT id, 1.0 AS score, 0 AS hop, ARRAY[id] AS path
  FROM memories
  WHERE fts_vector @@ plainto_tsquery('english', $1)
     OR embedding <=> $2 < 0.5
),
traversed AS (
  SELECT * FROM direct
  UNION ALL
  SELECT m.id,
         t.score * a.asserted_strength * 0.6 AS score,    -- hop decay 0.6
         t.hop + 1,
         t.path || m.id
  FROM traversed t
  JOIN associations a ON a.from_id = t.id
  JOIN memories m     ON m.id = a.to_id AND m.deleted_at IS NULL
  WHERE t.hop < 2
    AND m.id <> ALL(t.path)
    AND t.score * a.asserted_strength * 0.6 > 0.15
)
SELECT id, MAX(score) * confidence * decay_factor AS final_score, path
FROM traversed JOIN memories USING (id)
GROUP BY id, confidence, decay_factor, path
ORDER BY final_score DESC
LIMIT 20;
```

### Edge weight decomposition

Edges carry multiple independent weights; combine at read time:

```
weight ::= f(asserted_strength, co_count, recency_factor)

asserted_strength   — how sure we are the relationship is real
co_count            — how often this pair has been reinforced (hebbian)
recency_factor      — exponential decay from last_reinforced_at
```

### Edge types produced by retro-agent at extraction

- `same_episode`  — memories extracted from the same session or sharing a
  temporal anchor
- `mentions(<entity>)` — memories that reference the same extracted entity
  (seagull, Moonlander, Denver, etc.)
- `derives_from`  — provenance for abstracts (already implemented in the
  fix-curation-over-consolidation change)
- `supersedes`    — evolved memory replaces older version
- `contradicts`   — flagged by contradiction detector
- `supports`      — one memory backs up another's claim
- `causes` / `preceded_by` / `followed_by` — temporal/causal

The retro-agent emits typed edges during extraction; autoassoc based on
pure cosine is **not** done (it's redundant with embedding retrieval
itself and pollutes the graph with noise).

## 6. Pattern induction (`hypothesize` action)

Currently the retro-agent has `abstract` (synthesis over distinct related
facts). Add a sibling `hypothesize` action with different semantics:

- Scans existing memories for clusters with a common latent attribute
- Proposes a pattern with **low confidence** and **provenance=inferred**
- Writes a `supports` edge from each piece of evidence to the hypothesis
- Marks the hypothesis as needing **user ratification** before confidence
  can harden

When the user later confirms ("yes, I don't like red cars") the
hypothesis gets promoted to `explicit` provenance and confidence is
boosted. When contradicted, it gets superseded.

## 7. Blind spots — priority-ranked

Priorities are relative to "things you need to get right before a
long-lived vault stays trustworthy vs. things that can be iteratively
added."

### P0 — prerequisites for a trustworthy long-lived system

#### P0.1 Contradiction detection and resolution

Memories drift into inconsistency. "Jason uses Vim" (2024) → "Jason
switched to Zed" (2026) both live in the store if nothing reconciles
them; retrieval becomes nondeterministic.

**Detector.** On write, compute embedding similarity vs. existing
memories in the vault. When similarity is high AND content disagrees
(e.g., via LLM contradiction check in the retro-agent — still async),
flag the pair.

**Resolver.** Options:
- Automatic: newer wins if both are `explicit`; evolve the older as
  superseded
- User-prompted: when confidence is split or both are recent, ask
- Via explicit `contradicts` edge, keep both but mark the conflict so
  retrieval can surface the newer one by default

#### P0.2 Provenance tier

Three classes with different confidence and decay behavior:

```
explicit  — user stated directly             ("I'm allergic to shellfish")
implicit  — observed from behavior            ("ordered Thai 3× this month")
inferred  — agent concluded from pattern      ("probably dislikes red cars")
```

Stored on every memory. Affects:
- Decay rate (explicit decays slowest)
- Contradiction resolution (explicit beats inferred)
- Surfacing policy (inferred gets lower default ranking until ratified)
- UI treatment (inferred shown with "I think..." hedge)

Without this, the agent will confidently present its own guesses as
user-stated facts. Very bad trust dynamic.

#### P0.3 Correction path

"No, I said light roast not dark" must be a first-class operation:

1. Find the specific memory (embedding match + recency scoring)
2. Evolve it with corrected content; mark correction reason
3. **Do not** let the correction itself become a memory ("User said 'you
   got that wrong'" is noise, not signal)

Needs an explicit API in the retro-agent or gateway, not just "wait for
the next extraction to overwrite the wrong thing."

### P1 — substantial UX impact, build during first major iteration

#### P1.1 Temporal anchoring (`event_at` vs `created_at`)

`created_at` = when the memory was written. `event_at` = when the event
it describes occurred. These are often very different.

- "I had surgery in 2019" written in 2026: 7 years old in event-time,
  0 days in memory-time
- Enables episodic reconstruction: "what happened in college?"
- Enables temporal reasoning: "before I had the surgery" needs to
  resolve surgery's event-time
- Decay sensitivity: some memory types should decay on event-time, not
  on memory-time (old events don't become newer just because someone
  mentioned them)

Memory extraction must attempt to pull event-time from the content
(with explicit "unknown" allowed).

#### P1.2 Salience separate from confidence

`confidence` = "is this true?" `salience` = "does it matter?"

- "I was embarrassed at my wedding" — low frequency, high salience
- "I prefer the left side of a booth" — high confidence, low salience

Salience should:
- Slow decay independently of confidence
- Bias associative traversal (salient nodes pull harder on neighbors)
- Come from signals: emotional language, repetition across sessions,
  explicit user emphasis ("this matters")

ACT-R's "arousal" term; almost nothing in the market models it
explicitly. Worth doing.

#### P1.3 Abstraction pruning

Current design has `abstract` (additive synthesis). The complement is
missing: *when do detail memories get discarded once an abstract covers
them?*

Human memory prunes aggressively: "I went to Italy in 2019" survives;
"I ordered spaghetti carbonara on July 23rd in Rome" fades. Without
this, the vault grows linearly forever and abstracts are parallel
layers rather than compression.

Policy sketch:
- Detail memories with `confidence < 0.7` AND covered by an abstract
  AND no independent access in N days → soft-delete
- Never prune `identity` or `constraint` types
- Aggressively prune `observation` types
- Decay-triggered pruning runs in the decay-agent

#### P1.4 Redaction / "forget this" + write-only-for-period

"Forget I told you about the Smith deal" — clean removal including:
- Soft-delete the memory
- Find and re-generate any abstract that derived from it
- Break DERIVES_FROM edges pointing at it

"I'm planning a surprise party; don't surface this around my wife"
requires a visibility context on memories that retrieval respects.
Feature muninndb doesn't have. Rare but ugly when it's missing.

### P2 — important but can come later

#### P2.1 Bootstrapping asymmetry

Fresh vault and mature vault benefit from different extraction
policies:

- New vault: extract aggressively, accept low-confidence novelty
- Mature vault: extract only what's novel against the existing vault
  (pre-extraction recall to check for duplicates)

Turns extraction from dumb-harvesting into incremental update. Reduces
the "50 restatements of Jason likes coffee" problem we saw live.

#### P2.2 Meta-memory / periodic user audit

The agent should be able to signal the user: "here's what I think I
know about you; any of this wrong?" A structured periodic check-in
rather than waiting for the user to stumble into an error.

Without this, inferred memories compound silently and the user loses
trust when they eventually discover the mistakes.

## 8. Open questions / not yet decided

- **Embedding model.** Generic (OpenAI/Anthropic) vs. a small domain-tuned
  bi-encoder? Generic is easy and probably good enough to start; revisit
  if retrieval quality is the bottleneck.
- **Decay curves per memory_type.** Concrete parameters haven't been
  picked. Needs experimentation once a corpus exists.
- **Vault model.** Single-vault-per-user, multiple vaults per user (for
  scoping), or shared-across-users (for team contexts)? Current muninndb
  usage is single-vault.
- **Which Postgres?** Assume latest stable (16+) with pgvector 0.5+.
  Managed (RDS, Supabase, Neon) or self-hosted in docker-compose?
- **Retro-agent changes.** How big is the delta from the current
  retro-agent implementation to one that produces entity edges, temporal
  anchors, provenance tiers, etc.? To be estimated in the proposal.

## 9. Next step

When ready to implement, draft an OpenSpec proposal that covers:

1. Adding the Postgres schema and a new `internal/memory` package that
   implements the same interface as `appcontext.MuninnClient`
2. Dual-write period (optional) or clean cutover
3. Retro-agent changes to produce typed edges, entity extraction,
   temporal anchors, provenance, salience
4. Retrieval path changes: recursive CTE + confidence/decay multiplier
5. Migration of existing muninndb data (if worth doing — much of it is
   "Consolidated memory" boilerplate anyway)
6. Decay-agent (new background worker)
7. Contradiction detector (in retro-agent or separate)
8. Correction API (gateway endpoint + retro-agent handler)

The P0 blind spots (contradiction, provenance, correction) should be in
the first proposal. P1 items (temporal anchoring, salience, pruning,
redaction) can be a second proposal after the substrate is stable. P2
items iterate from there.
