## Why

The memory system today is implicitly single-user: missions, directives, extraction prompts, and reflection all hard-code "Jason" as the subject of every fact. This breaks the moment the assistant interacts with a second person (household member, group chat, email thread) — facts attach to the wrong entity, preferences collide, and the lexical entity resolver cannot disambiguate "the user" across speakers. We need a model that keeps track of *who said something*, *who a fact is about*, and *what kind of fact it is*, without requiring a single named operator.

## What Changes

- **BREAKING**: Missions and directives no longer reference "Jason" by name. They are rewritten around speaker and entity roles (`the speaker`, `a person`, `the primary household`). Existing retained memories are unaffected; new retentions adopt the people-centric phrasing.
- Sessions gain an optional `speaker_id` association. The gateway accepts a `speaker_id` hint on chat/completions and Responses requests; session-management persists it on the session record.
- Chat requests propagate `speaker_id` end-to-end: gateway → context-manager → main-agent → retro-agent. Retro-agent includes it in its extraction prompt so the LLM knows which turns came from whom.
- Retain payloads gain a three-axis fact model carried in metadata:
  - `metadata.speaker_id` — who uttered the source turn (whose mouth the claim came out of).
  - `metadata.fact_type` — one of `person_fact`, `world_fact`, `context_fact`, `procedural_fact`.
  - `entities` (existing) — who/what the fact is about.
  Memory-service validates these on `/retain` and forwards to Hindsight.
- Retro extraction prompt is updated to emit the three axes and to avoid conflating *speaker* with *subject* (the speaker-about-self case stays the most common, but speaker-about-other and world-facts become first-class).
- Recall gains an optional `speaker_id` filter and an `entities` filter so callers can scope retrieval to one person's facts without relying on tag conventions.
- Reflect mission stops assuming a single operator; it consults the `speaker_id` context passed by the caller to frame its answer.
- A single `primary_user_id` is configurable (`config:memory` → `primary_user_id`, default unset). When set, the gateway treats anonymous sessions as originating from that user; when unset, sessions require an explicit `speaker_id` or are tagged `speaker:unknown`. This keeps a single-person deployment trivial while removing the hard-coded name.

## Capabilities

### New Capabilities
- `identity-model`: The normative model for speakers, entity attribution, and fact types. Defines `speaker_id`, `fact_type`, the canonicalization rules memory-service applies on `/retain`, and the invariants retro-agent extraction must satisfy. One source of truth referenced by memory-service, retrospection, and gateway-api specs.

### Modified Capabilities
- `memory-service`: `/retain` validates and stores the new metadata axes (`speaker_id`, `fact_type`); `/recall` accepts `speaker_id` and `entities` filters; startup sync uses the people-centric missions/directives.
- `retrospection`: Extraction prompt, skill-creation job, and curation job all carry `speaker_id` through to memory-service and emit the three-axis metadata.
- `session-management`: Sessions store an optional `speaker_id`; `GET /v1/sessions/:id` and `GET /v1/sessions` surface it; chat/completions and Responses requests accept `speaker_id` on input.
- `gateway-api`: Request schema (chat/completions, Responses) accepts `speaker_id`; `X-Speaker-ID` header is honored as a fallback; responses echo the resolved speaker.
- `context-management`: Context payload passed to main-agent includes `speaker_id`; memory recall called during context assembly scopes to that speaker when `primary_user_id` is unset.

## Impact

- Code: `internal/memoryservice/*`, `internal/retro/*`, `internal/gateway/*`, `internal/contextmgr/*`, `internal/messaging/payloads.go` (new fields on `ContextAssembledPayload`, `RetainRequest`, `RecallRequest`, session records).
- Config: `config:memory.primary_user_id` (new), `config:memory.recall_default_speaker_scope` (new: `any` | `primary` | `explicit`).
- Deploy: all files under `deploy/memory/missions/` and `deploy/memory/directives/` rewritten; migration note added to the change design.
- APIs: chat/completions request body, Responses request body, session list/detail responses — all gain optional `speaker_id`. Existing clients without the field keep working (falls back to `primary_user_id` when set; otherwise `speaker:unknown`).
- No existing retained memories are migrated. The transition is forward-only: old observations remain queryable but lack the new metadata. Retro-agent does not backfill.
- Out of scope (documented as future work in design.md): class/instance inference ("Fred is a cat → Fred has fur"), an identity registry with operator-visible merge UI, source adapters (Discord, SMS, email).
