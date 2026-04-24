## Context

Today the memory substrate (Hindsight) and every piece of tooling that calls it assumes a single human operator. "Jason" appears explicitly in `deploy/memory/missions/retain_mission.yaml`, `observations_mission.yaml`, `reflect_mission.yaml`, and two of four directives. The retro-agent's in-code extraction prompt carries the same assumption. There is no place in the request path to declare *who is speaking*; sessions are identified only by a UUID and chained by `previous_response_id`.

Hindsight resolves entities lexically (SequenceMatcher weight 0.5, co-occurrence 0.3, temporal 0.2) — "User" and "Jason" were confirmed to be stored as separate entities during the last round of testing. There is no semantic merge. This is fine for a single-person deployment but fails the moment a second person shows up, because the system has no way to record *which person* an utterance or observation is about.

The forward plan is for the assistant to eventually interact across source adapters (Discord, email, SMS, room microphones, group chats) — each of which can carry multiple concurrent speakers. The identity model needs to scale from "one person at a terminal" to "N people across M channels" without a schema rewrite later.

Three architectural invariants are already proven and we will keep them:
1. Hindsight owns entity storage and resolution. We do not build a parallel entity graph.
2. Memory-service is the only service that talks to Hindsight. Retro, context-manager, and gateway never do.
3. Sessions are UUIDs chained by `previous_response_id`. Multi-speaker does not mean multi-session — a group chat can be one session with multiple speakers.

## Goals / Non-Goals

**Goals:**
- Remove hard-coded "Jason" references from missions, directives, and retro prompts.
- Introduce a `speaker_id` that flows end-to-end from request → retain call → stored fact metadata.
- Introduce a `fact_type` metadata axis distinguishing person-facts, world-facts, context-facts, and procedural-facts — so a fact like "Cats have fur" doesn't have to be attached to any person.
- Keep the single-person deployment as low-friction as it is today via `primary_user_id`.
- Make multi-speaker recall possible via `speaker_id` and `entities` filters on `/recall`.
- Document class/instance reasoning and an identity registry as explicit *future work*, not as P0.

**Non-Goals:**
- No migration of existing memories. Old data keeps working; it just lacks the new axes.
- No new storage tier. Everything continues to live in Hindsight.
- No semantic entity merging. "User" and "Jason" will still be separate entities unless an operator manually canonicalizes them; we document this as a known limitation with an `identity-registry` future change.
- No class/instance inference ("Fred is a cat → Fred has fur"). The substrate supports the tags; the reasoning is deferred until use patterns demand it.
- No source adapters (Discord, email). Those land as their own changes; this one only establishes the speaker axis they will fill in.

## Decisions

### D1: Three-axis fact model in metadata (speaker_id, fact_type, entities)

Retain payloads gain two new metadata fields and keep the existing `entities` array:

```json
{
  "content": "Dark roast coffee is preferred in the morning by Jason",
  "tags": ["coffee","morning","preferences"],
  "entities": ["Jason", "coffee"],
  "metadata": {
    "provenance": "explicit",
    "speaker_id": "jason",
    "fact_type": "person_fact",
    "confidence": "0.9"
  }
}
```

- `speaker_id` is a short, stable identifier for the person who uttered the source turn. It is NOT a full user profile; it is a key into whatever identity registry the deployment eventually builds.
- `fact_type` is one of:
  - `person_fact` — "Jason prefers dark roast." The `entities` array carries the subject.
  - `world_fact` — "Cats have fur." Speaker-agnostic; `entities` may be a class label.
  - `context_fact` — "It is raining in Seattle right now." Time-scoped; should decay.
  - `procedural_fact` — "To restart the gateway, run `docker compose restart gateway`." Skill-adjacent.

**Rationale:** Three axes separate *source* from *subject* from *kind*. The single-axis model ("everything is about Jason") only works because there is only one person. Decoupling makes recall filtering trivial (`speaker_id=jason`, `entities=Fred`, `fact_type=person_fact`) and leaves room for the identity registry to canonicalize `speaker_id` → `entity_id` later without rewriting the fact shape.

**Alternatives considered:**
- **Single "subject" field** — collapses speaker and subject. Fails the "Jason says Alice likes jazz" case.
- **Separate tables per fact_type** — a second layer of schema to maintain. Metadata tags are cheaper and Hindsight already indexes them.
- **Encode speaker in tags (`speaker:jason`)** — works technically but collides with the tag-scope gotcha documented in `memory/reference_hindsight_tag_scope.md`: adding a tag changes consolidation scope. Metadata avoids that entirely.

### D2: `primary_user_id` as the single-person escape hatch

Most deployments are one person. Requiring every request to carry `speaker_id` would be friction for nothing. The memory-service config gains:

```yaml
primary_user_id: "jason"          # optional; when set, acts as fallback
recall_default_speaker_scope: any # any | primary | explicit
```

- When `primary_user_id` is set AND a request has no `speaker_id`, the gateway substitutes it.
- When `primary_user_id` is unset AND the request has no `speaker_id`, the record is tagged `speaker:unknown` and logged as a warning (not an error).
- `recall_default_speaker_scope` controls how recall treats missing filters: `any` = no scoping (today's behavior), `primary` = restrict to `primary_user_id` facts, `explicit` = require caller to specify.

**Rationale:** Zero-config for single-user installs; one-line config to opt into multi-user. The setting lives in `config:memory` so it is runtime-tunable via the memory-service panel.

**Alternatives considered:**
- **Always require explicit speaker_id** — breaks every current single-user deployment.
- **Auto-detect speaker from session turn content** — out of scope and fragile. Source adapters will do this explicitly.

### D3: Request-path plumbing via a new optional field + header

Two paths:

1. Request body field: `speaker_id` on chat/completions and Responses (identical to how `session_id` works today).
2. Header: `X-Speaker-ID` as a fallback for adapters that cannot rewrite the body.

The gateway resolves a canonical speaker in this order:
1. Request body `speaker_id`.
2. `X-Speaker-ID` header.
3. Session's stored `speaker_id` (inherited via `previous_response_id`).
4. `primary_user_id` from config.
5. `speaker:unknown`.

The resolved `speaker_id` is attached to the session record (first time only; subsequent turns inherit), propagated through `ContextAssembledPayload.SpeakerID`, and included in the retro-agent's extraction prompt context.

**Rationale:** Mirrors the existing `session_id` pattern. Adapters (Discord webhook, SMS gateway) will set the header; terminal users of the gateway don't need to.

**Alternatives considered:**
- **Only header** — inconsistent with the session_id pattern. Clients that set JSON would have to set a header too.
- **Only body** — excludes adapters that can't easily alter the body (some webhook formats).

### D4: Memory-service validates and forwards; does not canonicalize

Memory-service enforces:
- `speaker_id` is a non-empty string if present. Missing → `primary_user_id` if set, else dropped with `speaker:unknown` tag.
- `fact_type` is one of the four valid values if present. Invalid → HTTP 400. Missing → server-side default:
  - `person_fact` when the `entities` array contains a non-generic proper noun the speaker is not talking about themselves-only.
  - `context_fact` when content contains time-scoped cues (`right now`, `currently`, timestamps).
  - `world_fact` otherwise.
  Defaulting is conservative; callers should set `fact_type` explicitly.
- `entities` with a `class:` prefix (e.g. `class:cat`) is a tag marker, not a person. The recall filter handles these distinctly from entity IDs.

Memory-service does NOT try to merge `"User"` and `"Jason"` into one entity. That is the future `identity-registry` capability.

**Rationale:** Keeps memory-service thin. Hindsight owns entity storage; we own validation.

### D5: Missions and directives rewritten for role-based language

Every occurrence of "Jason" in `deploy/memory/missions/*.yaml` and `deploy/memory/directives/*.yaml` is replaced with role-based phrasing:

- `retain_mission`: "the speaker" for subjects of person-facts, "household members, pets, relationships" (plural), "the speaker's home" instead of "Jason's home".
- `observations_mission`: facet definitions become "the speaker's preference for X" — still one-per-facet, but the facet is scoped per `(speaker_id, topic)` pair.
- `reflect_mission`: "the person being served" rather than "Jason".
- Directives that mention "Jason" by name become "the authoring speaker".

The retro-agent's in-code `extractionPrompt` receives the same treatment and takes `speaker_id` as input context.

**Rationale:** The LLM doing extraction cares about roles, not names. Roles scale; names don't.

**Alternatives considered:**
- **Template the name at startup** — `{{user_name}}` substitution. Simpler but bakes in single-user. Also makes group-chat content (multiple speakers per session) awkward.

### D6: Recall gains `speaker_id` and `entities` filters

The `/recall` request body is extended:

```json
{
  "query": "coffee preferences",
  "speaker_id": "jason",
  "entities": ["jason"],
  "fact_types": ["person_fact"],
  "limit": 5
}
```

All new fields are optional; omitted fields mean "don't filter". Memory-service passes them through to Hindsight's recall as tag/metadata filters. Context-manager sets `speaker_id` to the session's speaker when assembling context.

**Rationale:** Without speaker-scoped recall, the assistant answering Jason's question gets Alice's coffee preferences mixed in.

## Risks / Trade-offs

- **Hindsight entity resolver is still lexical.** `speaker_id="jason"` does not tell Hindsight that the entity `"Jason"` it discovered via NER is the same person. Merging is manual. → **Mitigation:** Document as known limitation; propose `identity-registry` as a follow-on change that exposes Hindsight's merge API via memory-service and surfaces it in the dashboard.
- **`speaker:unknown` accumulates silently.** If `primary_user_id` is unset and no adapter sets `speaker_id`, facts pile up with no owner. → **Mitigation:** Log a warning on each retain with `speaker:unknown`, surface a count in the memory-service dashboard panel, and default `primary_user_id` to an obvious sentinel (`unset`) so operators notice.
- **Consolidation scope still depends on tags.** Per the tag-scope gotcha, adding the fact_type to tags changes scope. → **Mitigation:** Keep fact_type in metadata, not tags. Observations remain tag-scoped as today.
- **`fact_type` auto-default may misclassify.** Content cues (`right now`, proper nouns) are heuristic. → **Mitigation:** Default conservatively to `world_fact`; retro-agent's extraction prompt sets explicit `fact_type` on ~all emissions, so server defaulting only catches manual callers.
- **Group-chat case is under-specified.** One session, multiple speakers per turn. → **Mitigation:** Out of scope for this change; the speaker_id field is per-turn (carried on the request) not per-session, so a group-chat adapter can set it differently on consecutive turns without any further schema change.
- **Dashboard still assumes one user.** Panel descriptors don't render per-speaker views. → **Mitigation:** Leave dashboard alone for this change. `identity-registry` can add a speaker selector.

## Migration Plan

Forward-only. No backfill.

1. **Rewrite missions and directives** (files in `deploy/memory/`). Memory-service YAML-wins sync propagates to Hindsight on next startup.
2. **Ship metadata fields** (gateway, context-manager, retro-agent, memory-service). No existing call breaks; all new fields are optional.
3. **Update retro extraction prompt** to emit `speaker_id`, `fact_type`. When the LLM omits them, server defaults apply.
4. **Update reflect-time framing** — reflect_mission uses role language; `speaker_id` is passed in the query context.
5. **Soft-roll `primary_user_id`.** Default unset. Deployments that want single-user behavior add one line to their config.

**Rollback:** Revert the YAML changes (missions/directives) and the retro extraction prompt. The new metadata fields become silent no-ops because memory-service validation treats them as optional. Pre-change observations are untouched throughout.

## Future Work (explicitly out of scope)

- **`add-identity-registry`** — operator-visible entity merge via memory-service, surfaced in the dashboard. Solves "User and Jason are the same person" canonicalization.
- **`add-source-adapters`** — Discord / email / SMS webhooks that set `speaker_id` from their platform's identity primitive.
- **`add-class-instance-inference`** — a retain-time hypothesize pass that emits `class:cat has_property fur` and a recall-time union of instance facts with class facts. Requires stable class tagging conventions first.
- **Dashboard speaker selector** — multi-speaker filter on the memory panel and session list.

## Open Questions

- Should `speaker_id` live in Hindsight memory `metadata` (our current plan) or as a distinct Hindsight field? Current plan keeps it in metadata because Hindsight's metadata is already free-form `Map<string,string>` and we don't need server-side querying beyond tag/metadata match. Revisit if Hindsight adds a typed speaker field.
- Should `fact_type` also be emitted as a tag for consolidation-scope purposes? Current plan: no, for the tag-scope gotcha reason. Revisit if we want per-fact-type consolidation behavior.
- How does `speaker_id` interact with the retro-agent's skill creation job? Current plan: skills stay speaker-agnostic (skills are operational knowledge, not personal facts), but the job may want to scope skill candidacy to "this speaker's sessions" to avoid one speaker's quirks polluting another's skill set. Defer.
