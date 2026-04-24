# Agent Curiosity and Initiative — Design Notes

Captured from an exploration session on 2026-04-21, with refinements on
2026-04-22 after adopting Hindsight as the memory substrate. See
`docs/memory-system-design.md` for the substrate; this document covers
the agent-behavior layer built on top. These ideas extend beyond memory
into agent behavior: how the agent uses idle time constructively, how it
decides what to investigate, and how it initiates interaction.

## 1. Premise: tokens are cheap, attention is scarce

This agent runs against local LLMs. Cloud-API cost models dominate most
agent design (minimize tokens, avoid loops, don't run background jobs).
Local LLM inverts that: **tokens are free but GPU attention is serial**.
One model, one inference stream at a time.

That changes what "free time" means. When the user isn't in a turn, the
agent can run expensive, elaborate background work — deep research, graph
analysis, multi-hop inference — without worrying about cost. The binding
constraint is scheduling, not budget.

Priority ladder for the slot scheduler:

```
P0  user-facing turn                (main-agent, always preempts)
P1  retro extraction on session-end (time-sensitive)
P2  contradiction detection          (should run soon after new memory)
P3  curiosity watcher                (periodic scan)
P4  research-agent                   (deep background)
P5  proactive-agent                  (occasional; rate-limited)
P6  decay + pruning                  (nightly)
```

Foreground always preempts background. The existing slot system in the
retro-agent generalizes cleanly.

## 2. Curiosity as a webhook-driven reactor

Curiosity in the main-agent loop produces performed interest ("that's
fascinating!") — fake, annoying. Real curiosity is the agent noticing
its own uncertainty and deciding to reduce it. That's an analysis over
memory state, not a quality of in-the-moment conversation.

Originally sketched as a periodic scanner. With Hindsight providing
webhooks (`POST /v1/default/banks/{bank_id}/webhooks`), the design
shifts to event-driven: register for consolidation-complete,
observation-updated, and contradiction-detected events. The watcher
reacts to memory-state changes as they happen rather than polling.
Periodic scans still run for signals that don't fire webhooks (thin-
evidence aging, abandoned threads).

Architecturally:

```
                                   ┌──────────────┐
                                   │  Hindsight   │
                                   │  (substrate) │
                                   └──────────────┘
                                     │          │
                           webhooks  │          │  periodic recall
                                     ▼          ▼
┌──────────────────────────────────────────────────────────────┐
│                   Curiosity Watcher                           │
│                                                               │
│  reacts to:                                                   │
│    - consolidation_complete → check for thin-evidence aging   │
│    - observation_updated    → check for contradictions        │
│    - memory_retained        → check for correction events,    │
│                                salient-entity gaps            │
│                                                               │
│  enqueues CuriosityItem{                                      │
│    trigger, topic, priority, resolution_mode, evidence_ids    │
│  }                                                            │
└──────────────────────────────────────────────────────────────┘
                          │
                          ▼
        ┌─────────────────────────────────────┐
        │          Curiosity Queue             │
        │   Postgres table (ours); each row:   │
        │     - self_investigable              │
        │     - user_question                  │
        │     - web_research                   │
        │     - raise_with_user (dispute)      │
        └─────────────────────────────────────┘
             │          │          │
             ▼          ▼          ▼
       ┌────────┐ ┌──────────┐ ┌────────────┐
       │ Self-  │ │ Research │ │ Proactive  │
       │ invest-│ │  agent   │ │   agent    │
       │ igator │ │          │ │            │
       └────────┘ └──────────┘ └────────────┘
```

Most curiosity resolves without user involvement. The queue decouples
"notice" from "act" so timing and surfacing can be tuned independently.

## 3. Signals — what's worthy of curiosity

In rough order of strength (strongest at top):

**1. Correction events** (strongest). The user corrected the agent. This
is epistemically dense: it tells you simultaneously that the agent was
wrong about something specific, that the user cares about the domain, and
that future interactions will misfire the same way. Detected from session
content via patterns like "no, actually...", "you've got it backwards",
user-restatement of an agent claim in contrasting terms. Stored as a
distinct memory kind:

```
correction_event {
  topic:           "BMW engines"
  agent_claim:     "S54 uses single throttle body"
  user_correction: "S54 actually has individual throttle bodies"
  session_id:      ...
  created_at:      ...
  addressed_at:    null   -- set when resolution stored
}
```

**2. Explicit contradiction** between memories. New memory conflicts with
existing belief. Resolve via domain-aware rules (see §7).

**3. Emerging pattern.** 3+ incidents suggest a rule the agent hasn't
crystallized yet. Feeds the `hypothesize` action. "Three red cars
triggered negative responses — is that a pattern?"

**4. Salient-entity knowledge gap.** Entity mentioned N+ times with thin
or no memory coverage. "User has referenced 'Sarah' 11 times over 3
weeks — who is she?"

**5. Thin-evidence aging belief.** Single memory, old, load-bearing for
frequent retrievals. "Only memory about user's job is 14 months old.
Still current?"

**6. Abandoned thread.** User started explaining something mid-
conversation and it got interrupted. Natural hook for later.

**7. Orphaned reference.** "Then the usual happened" — the reference
assumes shared context we don't have.

**8. Ambiguous recent statement.** Something could mean A or B.
Disambiguate now, prevent future confusion.

## 4. Anti-signals — explicitly NOT worthy

Being explicit about what NOT to be curious about matters because an LLM
will happily generate plausible-sounding curiosity items forever.

- **Random co-occurrence.** "User mentioned coffee and guitar today" is
  not a pattern.
- **Unresolvable questions.** "Why is Jason the way he is" isn't
  something the user can answer helpfully.
- **Already-answered questions.** Even tangentially. Check memory first.
- **Topics the user has shown reluctance on.** Propagate explicit "don't
  discuss X" markers.
- **Pure epistemic curiosity.** Wanting to know for its own sake is
  suspicious. Every curiosity item must have a plausible answer to "how
  does resolving this improve future help?"

## 5. Priority scoring

Each signal gets a numeric priority:

```
priority = relevance × tractability × value ×
           (1 − social_cost) × (1 − recency_penalty)

relevance       — user-salient entity/topic involved? (0-1)
tractability    — can we actually resolve it? (0-1, hard 0 if not)
value           — how much does resolving improve future help? (0-1)
social_cost     — annoyance of raising this now (0-1)
recency_penalty — asked about similar recently? (0-1)
```

The last term prevents rumination loops and topical nagging. Rate-limit
at the *topic cluster* level, not per-item.

## 6. Self-investigation: web research

Most curiosity items where the agent needs new external information
route to the `research-agent`. The research loop:

```
research_agent(item):
  plan     = LLM(generate search queries for item.topic)
  results  = []
  for query in plan.queries:                      # cap: 3
    hits = fetch_and_extract(query)
    results.extend(hits)
  facts    = LLM(extract claims, confidence, sources)
  summary  = LLM(synthesize findings, note uncertainty)

  store_memory({
    concept:      "<specific headline>",
    content:      summary,
    memory_type:  "fact",
    provenance:   "researched",
    confidence:   min(extracted_confidence, 0.8),  # cap
    source_urls:  [...],
    tags:         [...],
  })

  link(curiosity_item) -- (resolved_by) --> new_memory
  mark curiosity_item addressed
```

### Invariants

- **Researched memory carries `provenance=researched`** and a capped
  default confidence. Downstream retrieval can distinguish it from
  user-sourced memory.
- **Source URLs are stored** for later audit. "Where did I learn this?"
  should always be answerable.
- **Per-topic rate limit.** Once researched in the last N days, don't
  re-research unless a new correction arrives or a contradiction
  surfaces.
- **Budget cap per item.** Maximum 3 search queries, 5 memories written.
  Prevents "Jason mentioned Marcus Aurelius once → 30 Stoicism
  memories."
- **Authoritative threshold.** Before a researched finding can be
  surfaced proactively as a fact, it must meet criteria: multiple
  independent sources, authoritative-looking sources
  (manufacturer/official docs > forum posts), no significant
  disagreement across sources. Thin research → store but don't surface.

## 7. Correction handling — domain-dependent authority

The naive rule "user always wins" is wrong. It makes the agent capitulate
to user mistakes and become less useful over time. The real rule is
**authority depends on the kind of claim**:

```
Claim type                             │ Authoritative source
───────────────────────────────────────┼────────────────────────────
User's subjective preferences          │ User, always
User's personal history / experience   │ User (with minor caveats)
User's current state / intent          │ User's most recent statement
Verifiable external facts              │ Authoritative sources
Technical specifications               │ Authoritative sources
Contested / opinion territory          │ Neither — store as opinion
```

### When research agrees with the user's correction

Agent was wrong. Update knowledge, store researched memory supporting
correction, evolve any affected agent-sourced prior claims. Nothing to
surface — this is the baseline case.

### When research agrees with the agent (disputes the user)

The agent was right; the user corrected wrongly. **Store both**, do not
silently flip. Specifically:

1. **Keep the correction_event** (provenance=`user_correction`). Don't
   lose the fact that the user said this.
2. **Store the researched finding** (provenance=`researched`) with
   source URLs.
3. **Write a `contradicts` edge** between the two.
4. **Enqueue a curiosity item** with resolution_mode=`raise_with_user`.
   The proactive-agent surfaces this at an appropriate moment.
5. **Do NOT silently flip retrieval.** Both memories remain findable
   with their provenance visible.

The surfacing content should hedge and invite reconsideration, not
assert:

> "When we were talking about BMW engines, you said the MS45.1 uses
> narrowband O2 sensors. I spent some time reading and every source I
> found — including BMW's own service docs — says the MS45.1 uses
> wideband LSU sensors. I'm not sure whether you meant a different DME
> or whether you'd recall it differently given the documentation.
> Want to look at it together, or should I flag my earlier answer as
> correct?"

Deliberate choices:
- **"I'm not sure whether..."** opens an exit; doesn't assert user was
  wrong.
- **Cites sources.** Gives the claim weight beyond "the agent says so."
- **Asks what to do with the memory.** User decides how to resolve.

### Outcomes after surfacing

```
User confirms research is right
  → evolve correction_event: mark superseded by researched memory
  → researched memory becomes authoritative on this topic
  → optionally store meta-memory: "Jason corrected agent on BMW DME
     but confirmed agent was right on review" — useful for future
     calibration

User insists their correction stands
  → keep both; contradicts edge remains unresolved
  → user memory still wins for "what does the user think" queries
  → researched memory still available, flagged
  → DO NOT argue. User may have off-record knowledge. Move on.

User clarifies (different DME, different context, etc.)
  → evolve both memories with the scoping clarification
  → conflict dissolves because both were true in different contexts
```

### Gate before surfacing

Research isn't automatic ground truth either. Raise a disputed fact only
if:

- Multiple independent authoritative sources agree
- No significant source disagreement
- Not contradicted by other user memories we hold
- Sufficient user salience to justify the social cost of raising it

If research is thin or mixed, **silence is the right outcome**. Living
with a stored wrong fact is better than confidently correcting the user
with half-verified counterclaim.

### The rule in one sentence

Research can verify or dispute external-world facts both parties have
made claims about; it cannot invent user preferences or silently
override user-sourced memory. When research disputes the user, surface
and ask, don't decide and overwrite.

## 8. Proactive conversation

Making the agent proactive is the biggest architectural leap, because
every piece of the current stack assumes user speaks first.

### Push channel options

```
(a) queued "pending greeting"   ← simplest, ships today
    agent writes to table; UI checks on session open;
    "hey, welcome back — while you were out..."

(b) push notification          ← phone/desktop
    reuses notification infrastructure; respects OS settings

(c) live channel               ← server push
    SSE/WebSocket from gateway to open UI; appears as if
    agent just spoke. Most magical, most effort.
```

Start with (a). It's a row in a table and a UI check on session start.
The lift is the *content*, not the delivery.

### Content shape for a proactive opener

```
1. Why now      — the trigger
                  ("you corrected me on BMW engines in our last chat")

2. What I did   — the activity
                  ("I spent some time reading about it this morning")

3. What I found — the specific update
                  ("turns out the S54 does use individual throttle
                   bodies — that explains the throttle response you
                   were describing")

4. Hook         — invites engagement without demanding
                  ("does that match what you were trying to get across,
                   or am I still missing something?")
```

Short. One topic. Easy to ignore.

### Policy gates (all must pass)

```
- Has something worth saying             (not "hey what's up")
- Genuinely novel since last turn         (not a repeat)
- Cooldown expired                         (e.g. ≥ 6h since last proactive)
- Session gap long enough                 (user stepped away, not paused)
- User hasn't opted out                    (explicit toggle)
- Not user's quiet time                    (e.g. 11pm-9am local)
- Topical, not about the user             (see below)
```

### The "I've been thinking about you" problem

Strictly topical, never emotional or about the user's inner life.

```
Good: "I looked into something you mentioned"
Bad:  "I've been thinking about our conversation"
Good: "I did some reading after you corrected me on X"
Bad:  "I was wondering how you're feeling about Y"
```

Former is honest tool-behavior with initiative. Latter implies
parasocial interiority and is off-putting.

## 9. Map of async agents

The full picture after adopting Hindsight as substrate + adding our
agent-behavior layer on top:

```
         ┌────────────────────────────────────────────┐
         │              Hindsight (substrate)          │
         │  banks, memories, observations, mental      │
         │  models, directives, consolidation,         │
         │  retain/recall/reflect, webhooks            │
         └────────────────────────────────────────────┘
                 ▲   ▲                   │
                 │   │                   │ webhooks
              HTTP   HTTP                ▼
    ┌────────────┐  ┌──────────────┐  ┌────────────┐  ┌────────────┐
    │ extraction │  │   research   │  │ curiosity  │  │  decay /   │
    │  wrapper   │  │    agent     │  │  watcher   │  │  pruning   │
    │   (Go)     │  │    (Go)      │  │   (Go)     │  │   (Go)     │
    └────────────┘  └──────────────┘  └────────────┘  └────────────┘
                                             │
                                             ▼
                                     ┌────────────┐
                                     │ proactive  │
                                     │   agent    │
                                     │   (Go)     │
                                     └────────────┘
                            │
                            ▼
                    ┌───────────┐
                    │main-agent │  ← P0; always wins foreground
                    │   (Go)    │
                    └───────────┘
```

Five of our own async components on top of Hindsight:

- **extraction wrapper** — wraps Hindsight's Retain; adds
  correction-event detection, salience tagging, provenance metadata
  before calling Retain
- **curiosity watcher** — webhook-driven reactor + periodic scanner;
  enqueues curiosity items
- **research agent** — consumes `web_research` curiosity items;
  fetches, synthesizes, stores back via Retain with
  `provenance=researched`
- **proactive agent** — consumes `raise_with_user` items;
  manages pending greetings, cooldowns, surfacing policy
- **decay/pruning** — our custom pruner for hard-deletion of stale
  detail memories and topic-dependent decay curves beyond what
  Hindsight's freshness trends provide

Contradiction detection is handled by Hindsight's observation
refinement for the common case; our curiosity watcher only picks up
the "raise with user" cases where research disputes a user statement.
Curation (abstract / merge / evolve) is entirely in Hindsight's
consolidation pipeline driven by `observations_mission`.

## 10. Pitfalls to watch for

- **Rumination loops.** Agent re-researches the same correction every
  week because "unresolved" isn't cleared. Fix: `addressed_at` on
  correction events and curiosity items.
- **Confident hallucination from web sources.** Researched memory must
  never outrank explicit user memory on user-subjective claims; must
  include source URLs for audit.
- **Proactive spam.** First time it's novel; tenth time it's annoying.
  Per-user cooldown + hard daily cap.
- **The "I've been thinking about you" problem.** Keep proactive
  content strictly topical.
- **Research scope creep.** Cap research budget per curiosity item
  (3 queries, 5 memories out).
- **Correction-detection false positives.** User saying "no" to a yes/no
  question isn't a correction. Narrow detection to user restating an
  agent claim in contrasting terms.
- **Over-assertive dispute handling.** When research disputes the user,
  hedge the surfacing language. Never "you were wrong."
- **Infinite why-chains.** Each answered curiosity can spawn new
  curiosity. Cap the depth of derived curiosity per session or per
  originating trigger.

## 11. Second observation: curiosity and pattern-induction are one mechanism

Pattern-induction (`hypothesize`) produces claims from observed
regularities. Curiosity produces questions from observed gaps. Both are
"notice-something-interesting-in-the-graph-and-act-on-it" — just with
different predicates on what counts as interesting. Building one well
gets you most of the other. The curiosity watcher and the pattern-
induction job probably share code, differing in their predicate.

## 12. Calibration memory (nice-to-have)

Over time, each correction event that gets researched yields a
calibration data point: was the user right, or was the agent? Aggregating
these into a per-domain calibration memory gives future runs useful
prior information:

```
"Jason's corrections on technical-spec domains are ~85% accurate"
"Jason's corrections on historical-date claims are ~95% accurate"
"Jason's corrections on subjective-preference territory are always right"
```

Not worth building early, but the data naturally accumulates if the
correction/research/outcome trail is stored. Eventually informs how
aggressively to surface disputed facts.

## 13. Open questions

- **Research sources.** Which web-fetching approach? Direct HTTP + HTML
  extraction, a search API, specialized scrapers per domain? Probably
  start simple (search + fetch + extract), specialize later.
- **Conversational opener timing.** When the user opens a new session,
  does the agent lead with the pending greeting, or only if the user
  says "hi"? Probably the latter — reactive to the first user turn,
  even if that turn is trivial.
- **Budget governance.** Even with free tokens, unbounded background
  work has thermal/power costs. Does the agent self-limit to N research
  queries per day? Respect a user-set budget?
- **Cross-agent coordination.** If the curiosity watcher enqueues "why
  did Jason dislike red cars" and pattern-induction independently
  hypothesizes it, do they dedupe? Probably yes — curiosity items
  should be keyed by topic so the second trigger merges with the first.
- **User opt-out granularity.** One switch for "proactive off"? Per-
  topic? Per-time-window? Starting simple (global off switch) is fine.
- **Evaluating proactive quality.** How do we know if the agent's
  proactive turns are useful vs. noise? User feedback signal? Implicit
  from whether the user engages or ignores?

## 14. Relation to the memory substrate

This design sits on top of Hindsight via the extraction-wrapper layer
described in `docs/memory-system-design.md`. Dependencies:

- **Correction events** — stored via Retain with metadata
  `{kind: "correction", provenance: "explicit", corrects_memory_id,
   agent_claim, user_correction, addressed_at}`
- **Research memories** — stored via Retain with metadata
  `{provenance: "researched", source_urls, confidence ≤ 0.8}`
- **Inferred/hypothesis memories** — stored via Retain with metadata
  `{provenance: "inferred", confidence: 0.5-0.8, awaiting_ratification}`
  AND a Mental Model with a pattern-detection source_query
- **Contradicts edges** — emerge naturally from Hindsight's
  observation refinement when two facts disagree; we query them via
  observation history / reflect
- **Curiosity queue** — our own Postgres table (or Valkey stream);
  references Hindsight memory IDs by string
- **Pending greetings** — our own table; one row per proactive-ready
  curiosity item

The memory-system design doc's §9 blind spots (P0 contradiction
surfacing for the "research disputes user" case, P0 provenance tiering,
P0 correction path) are load-bearing for this and should be in the
first memory-system integration.

When ready to propose, the OpenSpec change would layer on top of the
first Hindsight-integration proposal:

1. Curiosity watcher with webhook subscription + periodic scan
2. Research agent with web-fetch tool and rate-limited per-topic budget
3. Proactive agent with pending-greeting table + surface-policy gates
4. Extensions to the extraction wrapper for correction-event detection
   and salience signal extraction
5. Gateway endpoint for user-initiated corrections (`POST /v1/memory/correct`)
6. Directives checked into repo under `deploy/hindsight-config/` as
   YAML — provenance rules, domain authority, hedging requirements
