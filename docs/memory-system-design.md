# Memory System Design

Current-state design for microagent2's memory layer. This supersedes the
earlier exploration in `docs/homegrown-memory-system.md`, which advocated
rolling our own on Postgres+pgvector. Research into Hindsight (Vectorize)
showed it covers the memory-substrate requirements with a clean
customization surface, leaving only the agent-behavior layer for us to
build.

Date: 2026-04-22 (supersedes 2026-04-21 homegrown-memory-system.md)
Revised: 2026-04-21 after API spike against Hindsight v0.5.0 and docs.

## 1. Decision: Hindsight as substrate

After the muninndb issues in the 2026-04-20 session and one day of
exploration on rolling our own, Hindsight (github.com/vectorize-io/hindsight,
v0.5.0) emerged as a better fit than either. Capability spike against
a reference instance at `http://192.168.10.125:8888`; microagent2 will
run its own Hindsight as a service in this repo's `docker-compose`.

What Hindsight gives us that we otherwise would have built:

| Requirement | Hindsight primitive |
|---|---|
| Vector + keyword + graph + temporal retrieval | TEMPR (parallel strategies + rerank) |
| Typed edges (entities, relationships, causal) | Built-in graph structures |
| Write-time phrasing via prompt control | `retain_mission` + `retain_custom_instructions` |
| Consolidation (merge/abstract/evolve) | `observations_mission` + background consolidation pipeline |
| Higher-order synthesis (our `abstract`) | Observations |
| Cached curated summaries (our re-abstraction) | Mental Models with auto-refresh triggers |
| Conflict-driven update without explicit Update op | Evidence-tracked observation refinement |
| Time-aware freshness signals | `stable`/`strengthening`/`weakening`/`stale` freshness trend |
| Temporal anchoring (event time vs. creation time) | `occurred_start` / `occurred_end` / `mentioned_at` |
| Hard policy rules | Directives (CRUD, tagged, prioritized, activatable) |
| Topic-scoped behavior | `observation_scopes: per_tag` at Retain time (see §4) |
| Reasoning style | Disposition trait vector |
| Event notifications | Webhooks |

The core operations are `Retain`, `Recall`, `Reflect` — small enough
surface to reason about, big enough to express the memory needs we've
mapped.

## 2. Why not muninndb, why not rolling our own

### Muninndb (rejected)

Material bugs and limitations in one session of use: `ReadResponse` has
no `vault` field; UI renders numeric `state` against string `<select>`
options; `/api/consolidate` hardcodes output concept as the literal
`"Consolidated memory"` (wastes 3× FTS weight); `/api/explain` returns
zero components with non-zero score; `AccessCount` always 0 (no HTTP
path calls `RecordAccess`); `/api/engrams/{id}/evolve` wire format was
wrong in the SDK; link weights transform unexplainedly at the server;
no reverse-edge query. The cognitive-architecture framing (ACT-R,
Hebbian, CGDN) is distinctive but the implementation makes retrieval
behavior unpredictable and hard to debug.

### Homegrown Postgres+pgvector (rejected after deeper research)

Rolling our own was the initial response to muninndb's problems.
Design notes in `docs/homegrown-memory-system.md`. Sound in principle —
Postgres+pgvector+tsvector+graph tables would have worked — but
Hindsight already implements most of that substrate, plus conflict-
driven observation refinement, plus a mission-based prompt
customization surface that's better than what we would have built. The
distinctive work (curiosity, research, proactive, correction handling)
is all above the memory layer regardless.

### Hindsight (adopted)

Benchmarked SOTA on LongMemEval (91.4%) and LoCoMo (89.6%), externally
reproduced. Active development. Python-heavy but REST-first, so our Go
stack can integrate via HTTP. Open source, self-hostable — we run our
own instance in this repo's `docker-compose`.

## 3. User requirements, mapped

1. **"Old useless memories fade, topic-dependent speed"**
   → Covered by freshness trends + conflict-driven update. Per-topic
   fade differentiation comes from `observation_scopes: per_tag` at
   Retain time (see §4); per-topic decay *thresholds* are not a
   Hindsight config knob at v0.5.0. Hard-pruning (actual deletion
   after long staleness) is deferred to a wrapper-layer background
   job on top.
2. **"Seagull-hamburger linking via episodic co-occurrence"**
   → Covered by TEMPR's graph retrieval strategy. Entities extracted
   at retain time; co-occurrence captured implicitly via shared
   entities.
3. **"Pattern induction: 3 red cars → probably dislikes red cars"**
   → Our `hypothesize` action. Implementation: a Mental Model with a
   pattern-detection `source_query` and `refresh_after_consolidation:
   true`, plus individual inferred facts stored with
   `metadata={provenance: "inferred", confidence: 0.7,
   awaiting_ratification: true}`.
4. **"Confidence with topic-dependent fade"**
   → Freshness trend + observation refinement handles it at the
   substrate level. We may add our own salience axis via metadata for
   the "emotional importance ≠ factual certainty" distinction.

## 4. Bank strategy: single bank, per-tag observation scopes

Decision: one bank (`microagent2`), per-tag observation scopes via
`observation_scopes: "per_tag"` at Retain time. Tags carry the
topic-scoping behavior that multi-bank would have provided.

Why not multi-bank:

- Per-bank freshness / decay thresholds are **not exposed** through
  `PATCH /banks/{id}/config` (spike 2026-04-22). Freshness trends
  (`stable` / `weakening` / `stale`) are computed by Hindsight from
  internal thresholds, not configurable per bank. So multi-bank never
  would have bought us topic-dependent decay at this Hindsight version.
- `observation_scopes: "per_tag"` produces one consolidation pass per
  tag — each tag gets its own observation set, which is the
  differentiation we actually wanted.
- Single bank keeps missions and directives unified, which is better
  for a single user with overlapping topics (preferences ↔ identity ↔
  technical facts).

Initial tag taxonomy (seeds for per-tag scopes):

```
identity        biographical facts, household, relationships
preferences     likes/dislikes/habits/opinions
technical       verifiable external facts, tools, integrations
home            smart-home state, entity mappings, routines
corrections     explicit user corrections (high priority)
inferred        agent-derived patterns (provenance=inferred)
ephemera        transient mood / current task context
```

Tags are attached at Retain time. `observation_scopes: "per_tag"`
means consolidation runs separately per tag, so observations for
`preferences` don't get merged with observations for `technical`.

Decay-speed differences across tags (the original multi-bank
motivation) live in the `observations_mission` phrasing and, if
needed later, in a wrapper-layer hard-pruning job keyed on tag +
`stale` freshness trend.

## 5. Extraction customization (retain_mission)

Our accumulated extraction-prompt intelligence from the microagent2
retro-agent work ports directly:

- **Concept phrasing with category word** when naturally available
  (`"Jason's preferred drink is dark roast coffee"` not
  `"Jason prefers dark roast coffee"`) — enables query reach without
  query-time expansion
- **Three-layer tag taxonomy**: specific nouns / hypernym-category /
  activation-words-a-user-would-type
- **Memory-type constraints** matched to Hindsight's fact-type enum
  (world / experience / observation)
- **Confidence from extraction certainty**, not hardcoded

Implementation: use `retain_extraction_mode: custom` with
`retain_custom_instructions` containing the full prompt, OR use
`retain_mission` as additive guidance. Start with `retain_mission` —
it's additive over Hindsight's built-in extraction logic, which is
probably already good. If the built-in conflicts with our shape, swap
to custom.

## 6. Consolidation customization (observations_mission)

The curation prompt intelligence from the
`fix-curation-over-consolidation` OpenSpec change ports here:

- Distinguish **merge** (restate same fact), **abstract** (synthesize
  over distinct related facts), **evolve** (refine single entry)
- Protect abstracts from being sources in merge or evolve (the
  `[ABSTRACT]` annotation rule)
- Require evidence-phrase citation in merge reasons
- Minimum-3-sources rule for abstraction

Hindsight's consolidation pipeline conceptually does all of this
already via its observation refinement. Our mission text constrains
the specific behavior when ambiguity exists.

## 7. Directives — the policy surface

Directives are first-class Hindsight resources (CRUD, priority, tags,
active/inactive). This is where domain-dependent correction authority
lives:

```
Directive: "User-subjective claims are authoritative"
  content: "Memories about user preferences, opinions, feelings,
            and personal history are authoritative when stated
            explicitly by the user. Never infer or override them
            from research or pattern detection."
  tags: [preferences, subjective]
  priority: 90

Directive: "External facts require authoritative sources"
  content: "Claims about verifiable external facts (technical
            specs, historical dates, published data) require
            authoritative sources. When a user's claim conflicts
            with multiple independent authoritative sources,
            surface the conflict for user resolution rather than
            silently accepting either."
  tags: [technical, external-facts]
  priority: 80

Directive: "Researched claims must cite sources"
  content: "Any claim originating from web research must include
            source URLs. Never present researched findings as
            user-stated facts."
  tags: [research, provenance]
  priority: 95

Directive: "Inferred memories require ratification"
  content: "Memories with provenance=inferred are provisional
            until the user explicitly confirms them. Surface with
            hedging language ('I think', 'it seems') rather than
            asserting."
  tags: [inferred, hypothesize]
  priority: 85
```

This replaces a significant amount of what we'd have hardcoded in a
homegrown system. Directives are tunable at runtime without code
changes.

## 8. `hypothesize` implementation

Pattern induction (the "3 red cars → probably dislikes red cars"
thing) is not a Hindsight primitive, but it maps cleanly onto the
Mental Model + Retain combination:

```
Mental Model: "Jason's preference inferences"
  source_query: "What preferences can be inferred from Jason's
                 reactions across recent sessions? For each pattern
                 with at least three supporting observations, emit
                 a hypothesis with confidence 0.5-0.8 and cite
                 evidence."
  trigger.refresh_after_consolidation: true
  tags: [preferences, inferred]

Per-inference fact storage:
  Retain({
    content: "Jason might not like red cars (based on three
              negative reactions to red vehicles across sessions
              in the past 30 days)",
    metadata: {
      provenance: "inferred",
      confidence: 0.65,
      awaiting_ratification: true,
      evidence_memory_ids: [...],
    },
    tags: ["preferences", "inferred", "cars", "colors"],
  })
```

When the user ratifies ("yeah, I don't love red cars"), the
proactive-agent stores a confirming fact which reinforces the
observation naturally. When contradicted, the observation weakens.

## 9. Blind spots — still our responsibility

After the 2026-04-22 spike, much of the prior P0 list collapses into
Hindsight's observation/recall/history surface. What remains is
narrower and more pragmatic.

### P0 — prerequisites for a trustworthy long-lived system

#### P0.1 Provenance metadata convention

Every Retain call carries `metadata.provenance`, one of: `explicit`
(user said directly) / `implicit` (observed from behavior) /
`inferred` (agent-derived pattern) / `researched` (web source).
`metadata` is `Map<string,string>`, so numeric fields like
`confidence` and `salience` serialize as strings.

Semantics across observations (when observations merge memories of
mixed provenance) are enforced by `observations_mission` phrasing:
"When consolidating facts of mixed provenance, reflect the least-
confident provenance in the observation." Source-fact provenance
remains visible via the `source_facts` map returned by
`POST /memories/recall` with `types:["observation"]`, so downstream
readers can inspect the mix if they need to.

This is the only first-proposal work item from the old P0 list.
Fifteen-minute convention, not a scope-gating feature.

### Moved to the curiosity / proactive follow-up

The following were labeled P0 in the prior draft but are **behavioral
layer concerns, not memory-layer concerns**. They belong in the
curiosity-and-initiative work, not the Hindsight integration:

- **Contradiction surfacing** — Hindsight's observation refinement
  handles the *handling*. Surfacing conflicts to the user ("you said
  X, research says Y, which is right?") is a proactive-agent UX
  decision and lives in `docs/curiosity-and-initiative.md`.
- **Correction path (`POST /v1/memory/correct`)** — subsumed. With
  observation refinement, a user correction is just another Retain
  call with contradicting content. No special endpoint needed. If a
  client-facing "correct this specific memory" ergonomic later earns
  its weight, it can land in the memory-service HTTP surface as a
  thin Retain wrapper.

### P1 — substantial UX impact, first major iteration

#### P1.1 Temporal anchoring (event_at vs created_at)

**Covered** by Hindsight's `occurred_start` / `occurred_end` /
`mentioned_at`. Our extraction mission instructs the LLM to pull
event-time from content where stated.

#### P1.2 Salience, separate from confidence

**Not covered** by Hindsight. Add via metadata: `salience ∈ [0,1]`
on each memory. Our Recall-wrapper boosts retrieval scoring by
salience. Extraction mission identifies salience signals (emotional
language, explicit user emphasis, repetition across sessions).

#### P1.3 Abstraction pruning

**Partially covered** — Hindsight's observation layer deduplicates
and consolidates, which is the compression side. Actual hard-deletion
of obsolete detail memories after an abstract has stabilized is a
custom background job: scan for detail memories with `confidence <
0.7` and no independent access in N days when an observation covers
them; soft-delete (keep history for audit).

#### P1.4 Redaction / "forget this"

**Not covered.** Add via gateway endpoint `POST /v1/memory/forget`
that takes a description, finds the matching memory, and deletes it
from Hindsight. Any observations referencing it get re-consolidated;
any mental models refreshed.

### P2 — iterative

#### P2.1 Bootstrapping asymmetry

Mission text can hint at extraction density ("extract aggressively
when bank is young; favor novelty when mature"), but really this
needs extraction-time awareness of bank size. Our wrapper around
Retain can query bank stats and adjust the prompt per-call.

#### P2.2 Meta-memory / periodic user audit

Our proactive-agent's job. Periodically summarizes inferred and
low-confidence memories and asks the user to ratify or correct.

## 10. Architecture: memory-service as the single boundary

Today, `MuninnClient` is instantiated directly by `context-manager`,
`retro-agent`, and the integration tests. Every service that wants
memory access grows its own substrate client and its own mission /
directive code paths. That worked for muninn; it doesn't scale to the
curiosity / research / proactive agents on the roadmap, which are
webhook-driven and need a single HTTP termination point.

New shape: a dedicated `cmd/memory-service` fronts Hindsight. All
other services talk to it over HTTP. Webhooks (`retain.completed`,
`consolidation.completed`) terminate here. Write-time policy
(provenance tagging, salience, directive enforcement) lives here.

```
                        ┌──────────────────┐
                        │  memory-service  │
                        │  (cmd/memory-    │
                        │     service)     │
                        ├──────────────────┤
                        │  HTTP API:       │
                        │   POST /retain   │
                        │   POST /recall   │
                        │   POST /reflect  │
                        │   POST /forget   │
                        │  Webhook sink:   │
                        │   /hooks/hind/*  │
                        │  Policy layer:   │
                        │   • missions     │
                        │   • directives   │
                        │   • provenance   │
                        │   • salience     │
                        │  Startup:        │
                        │   bank+mission   │
                        │   YAML sync      │
                        └────────┬─────────┘
                                 │ HTTP + webhooks
                                 ▼
                           ┌───────────┐
                           │ Hindsight │         ┌──────────────┐
                           │ (docker-  │────────▶│  llm-proxy   │
                           │ compose)  │ OpenAI- │ (slot-aware  │
                           └───────────┘  compat │  HTTP front) │
                                                 └──────┬───────┘
                                                        │
           ▲       ▲         ▲                          │
           │       │         │                          ▼
  ┌────────┴──┐ ┌──┴────┐ ┌──┴────────────┐      ┌─────────────┐
  │context-mgr│ │retro  │ │future:        │      │ llama-server│
  │           │ │agent  │ │ curiosity     │      │  (N slots,  │
  └───────────┘ └───────┘ │ research      │      │   agent +   │
                          │ proactive     │      │   hindsight │
                          └───────────────┘      │   classes)  │
                                                 └──────▲──────┘
                                                        │
                                 ┌──────────────────────┴──────┐
                                 │         llm-broker          │
                                 │   (SlotTable, agent slots,  │
                                 │    coordinates with         │
                                 │    llm-proxy via messaging) │
                                 └─────────────────────────────┘
```

Transport: HTTP/JSON to match Hindsight's own style and keep the
webhook sink uniform. Hindsight runs in this repo's `docker-compose`
alongside memory-service; it is **not** the shared `192.168.10.125`
instance (that's reference-only).

The API surface is rich rather than thin: `Retain` auto-tags
`provenance=explicit` by default and lets callers override, bank
routing is baked in (single bank today, but the surface is ready for
bank-per-tenant later), salience heuristics apply at write time.
That's the point of the boundary.

Bank + mission + directive configuration lives as YAML in the repo
and is synced to Hindsight by memory-service at startup (idempotent:
PATCH if differs, create if missing). That makes config reviewable in
git and reproducible across environments.

All of microagent2's agent code stays in Go. Memory-service is the
one process that speaks to Hindsight; everything else speaks to
memory-service.

### LLM slot coordination via `cmd/llm-proxy`

Hindsight makes LLM calls for extraction (`retain_mission`),
consolidation (`observations_mission`), and synthesis
(`reflect_mission`). If Hindsight hits llama-server directly, those
calls land on whichever slot llama-server picks — likely evicting a
hot agent KV cache, because Hindsight's prompts are unrelated to any
agent's working context.

To avoid that, Hindsight does not talk to llama-server directly. It
points its OpenAI-compatible LLM provider at a new
`cmd/llm-proxy` service, which:

- Exposes an OpenAI-compatible HTTP endpoint
- Coordinates with `llm-broker` over messaging to reserve a dedicated
  **hindsight slot class** (pinned slots that agents never touch)
- Keeps Hindsight's stable mission prompts warm in those slots
  without thrashing the agent slot pool

Indicative slot budget (tunable in broker config):

```
SLOT_BUDGET = 6
  slots[0..3]   agent class        (preemptable, priority-driven)
  slots[4]      hindsight-retain   (pinned; extraction prompt)
  slots[5]      hindsight-reflect  (pinned; synthesis prompt)
```

Consolidation reuses the retain slot (similar prompt family) or runs
on the reflect slot when idle.

**Open: reflect priority for user-facing recalls.** When memory-
service issues a recall that triggers synthesis in-band for a user
turn, the call wants user-priority latency, not background. Two
shapes worth spiking:
- Hindsight forwards provider-passthrough headers (if supported) so
  memory-service can signal priority to llm-proxy per-call.
- Memory-service pre-synthesizes against llm-proxy directly using the
  user-priority class, bypassing Hindsight's reflect for the
  user-facing path.

This scope is a **prerequisite proposal** ahead of the Hindsight
cutover — see §12.

## 11. Open questions

Resolved this session:
- ✅ Does Hindsight have plugins? No, but missions+directives cover it.
- ✅ Does Hindsight have banks for topic scoping? Yes, first-class.
- ✅ Does Hindsight expose Opinion API? Not directly — we implement
  hypothesis via Mental Models + inferred-provenance facts.
- ✅ Does Hindsight do decay? Yes, freshness trends + refinement.
- ✅ Does Hindsight have webhooks? Yes — two event types:
  `retain.completed` (per-document, fires for each retained item)
  and `consolidation.completed` (after observation refinement runs).
- ✅ Observations queryable? Yes: `POST /memories/recall` with
  `types:["observation"]`. Response carries `source_facts` keyed by
  fact ID as a sibling map, so source memory traceability is
  preserved (not inline on each observation item).
- ✅ Memory audit trail? Yes: `GET /memories/{id}/history` returns
  a change log with source facts resolved to text.
- ✅ Embedding model swappable? Yes — per-bank configurable via
  `PATCH /banks/{id}/config`. Generic-embedding risk from muninndb
  does not transfer.
- ✅ Single-bank vs. multi-bank? Single. See §4.

Resolved with constraints:
- ⚠️ **Topic-dependent decay thresholds are not exposed.** The
  `/banks/{id}/config` surface does not include freshness thresholds.
  Per-topic decay differentiation has to come from `observations_mission`
  phrasing or a wrapper-layer hard-pruning job keyed on tag + `stale`
  trend. This retires the multi-bank motivation entirely.

Still open:
- **Hard-pruning of stale observations.** Hindsight's history is
  append-only for audit. If we want to actually delete old detail
  memories after decade-plus of use, we'll need a custom background
  job in memory-service (soft-delete, keep history for audit). Not
  in the first proposal — defer until the bank has enough history to
  matter.
- **Hindsight operational concerns.** Issue #996 in their repo notes
  Python process memory growing unbounded (~1GB in <1 hour) in
  containers. **Mitigation for first proposal**: container memory
  limit + restart policy in docker-compose; alert on container
  restart frequency in memory-service health endpoint. Not a design-
  level blocker given we control the deployment.

## 12. Proposal sequence

Two proposals, in order. The llm-proxy prerequisite must land before
the Hindsight cutover so we never run the thrashing state.

### Proposal 1 (prerequisite): `cmd/llm-proxy` + slot classes

1. New `cmd/llm-proxy` — OpenAI-compatible HTTP front for
   llama-server.
2. Extend `llm-broker` SlotTable with slot classes (agent /
   hindsight-retain / hindsight-reflect). Classes reserve specific
   slot indices; agents never receive hindsight-class slots.
3. Messaging contract between llm-proxy and llm-broker for slot
   reservation + release.
4. No Hindsight integration yet — llm-proxy just has to work and
   route a test OpenAI client through the broker to llama-server.

### Proposal 2: memory-service + Hindsight cutover

An OpenSpec proposal to stand up `memory-service` and cut over from
muninndb. Tightly scoped.

### In scope

1. **Hindsight in `docker-compose.yml`** — container, volume,
   memory limit, restart policy. Not the reference instance at
   `192.168.10.125`; a fresh deployment local to this repo.
   Hindsight's `llm_provider` points at `llm-proxy` (from Proposal 1),
   not directly at llama-server.
2. **`cmd/memory-service`** — Go HTTP service. API surface:
   `POST /retain`, `POST /recall`, `POST /reflect`, `POST /forget`.
   Webhook sinks at `/hooks/hind/retain` and `/hooks/hind/consolidation`
   (register the endpoints; route handlers can be stubs that just
   log until the curiosity/proactive proposals land).
3. **`internal/hindsight`** — Go client for Hindsight's REST API.
   Methods mirror the endpoints we use (Retain, Recall, Reflect,
   Directives, MentalModels, BankConfig, Webhooks, MemoryHistory).
4. **Bank + mission + directive seed** — YAML checked into the repo
   at `deploy/memory/`. Memory-service syncs to Hindsight at
   startup (idempotent PATCH).
5. **Cutover** — remove `MuninnClient` call-sites from
   `context-manager`, `retro-agent`, and `tests/integration_test.go`;
   route them through memory-service HTTP.
6. **Provenance metadata convention** — every Retain call emits
   `metadata.provenance`; extraction missions reference it. Cheap.
7. **Delete muninndb code + container** after cutover is green.

### Migration: `MuninnClient` surface → Hindsight equivalent

| MuninnClient method | Hindsight equivalent | Notes |
|---|---|---|
| `Recall(query, limit)` | `POST /memories/recall` | Types default to `world`+`experience`. `types:["observation"]` for higher-order synthesis. |
| `Store(spec)` | `POST /memories` (RetainRequest) | Metadata carries provenance/confidence/salience. |
| `Consolidate(ids, merged)` | **Subsumed** by observation refinement | Observations consolidate automatically; explicit consolidate call becomes an anti-pattern. |
| `Evolve(id, new, reason)` | **Subsumed** by observation refinement | Update = Retain a refined statement; observation weakens the old and strengthens the new. |
| `Delete(id)` | `DELETE /memories/{id}` | Direct mapping. Also used for `/forget`. |
| `GetOutgoingLinks(id)` | `GET /memories/{id}/history` + recall with entity filter | No 1:1 edge query; use history (source_facts) + entity graph. |
| `Link(source, target, relType, weight)` | **Dropped** | Hindsight builds graph from entities + consolidation; no explicit link call. |

The curation job in `internal/retro/job.go` (Consolidate / Evolve /
Link / GetOutgoingLinks) is the heaviest cutover: most of that logic
moves *into Hindsight* via the `observations_mission`. The job
becomes a trigger for consolidation + a periodic "refresh mental
models" call.

### Explicitly out of scope (follow-ups)

- Curiosity watcher (webhook handlers beyond stubs)
- Research agent
- Proactive agent (incl. pending-greeting table)
- `hypothesize` Mental Model + inferred-fact retain loop
- Salience metadata *scoring* beyond the convention that the field
  exists (scoring heuristic lands with the proactive agent)
- Hard-pruning background job
- Contradiction surfacing UX (lives in curiosity/proactive)
- `POST /v1/memory/correct` (subsumed; see §9)

### Risk / mitigation

- **Hindsight memory leak (#996)**: container memory limit + restart
  policy; memory-service health endpoint surfaces Hindsight restart
  count.
- **Observation consolidation timing**: observations refine
  asynchronously. First-proposal reads default to `world`+`experience`
  so we're not blocked on consolidation lag.
- **Muninndb data**: not migrated. Fresh bank, start clean. This is
  defensible because muninndb's retrieval behavior was unreliable
  enough that porting its contents would pollute the new substrate.

## Appendix A: Prior thinking

See `docs/homegrown-memory-system.md` for the Postgres+pgvector path
we explored before discovering Hindsight's customization surface. The
core requirements and P0/P1/P2 blind spot list carry forward; the
Postgres schema and recursive-CTE retrieval approach are superseded.
