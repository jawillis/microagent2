## Context

The OpenAI Responses API supports two ways of maintaining conversation state across turns:

1. **Server-side chain** — client captures `response.id` from each turn and sends it as `previous_response_id` on the next turn. Gateway walks the chain to reconstruct history.
2. **Client-side history** — client holds the full conversation locally and resends it in the `input` array each turn. `previous_response_id` is not sent.

Our implementation today only accommodates mode (1) gracefully. Mode (2) works *functionally* (the right tokens come back to the LLM because context-manager now falls back to `payload.Messages[:-1]`), but the canonical history we store is incoherent: each turn creates a new `session_id`, the response is stored under that session, and nothing ever writes to that session again. Open WebUI is our canonical mode-(2) client and cannot be talked out of this behavior from the server side.

Live snapshot from the dogfood stack after ~20 Open WebUI turns:

```
session:*:responses count             → 23
sessions with exactly 1 response      → 21
sessions with 2 responses             → 2   (both from curl tests using previous_response_id)
sessions with ≥3 responses            → 0
```

Retro agent reads `session:<id>:responses` to drive memory extraction and skill creation. Memory extraction still runs (extracting what it can from a single-turn exchange — "user likes blue" is visible even without a follow-up) but at degraded quality. Skill creation is gated on `retroCfg.MinHistoryTurns > 1` and is therefore silently disabled for Open WebUI. The dashboard's session list grows one entry per turn.

The goal: make mode-(2) traffic land in the store in exactly the same canonical shape as mode-(1) traffic, with minimum server-side gymnastics and no client cooperation required.

Primary stakeholders: gateway, response store, retro-agent (as a downstream consumer only — it does not change).

## Goals / Non-Goals

**Goals:**
- A single Open WebUI conversation that spans N turns produces exactly one `session:<id>:responses` entry in Valkey with N items, regardless of whether Open WebUI uses `previous_response_id`.
- The new stitched sessions are indistinguishable downstream from sessions built via explicit `previous_response_id` chaining — `response.previous_response_id` is populated, `WalkChain` works, retro reads history the same way.
- Zero server-observable latency regression on the non-stitching paths (explicit `previous_response_id`, single-message input). Those paths get zero new work.
- Dormant stitching state ages out on a bounded TTL so Valkey doesn't leak.
- The stitching logic is localized to the gateway plus a small helper on the response store. No changes to retro-agent, context-manager, broker, or agent-runtime.

**Non-Goals:**
- Changing any client behavior. We cannot and will not demand that Open WebUI adopt `previous_response_id`.
- Semantic or fuzzy matching. The hash is byte-for-byte over the same canonical flattening we already do. If the client mutates a previously-committed turn (e.g. edits a past message) the hash drifts and a new session is minted. Acceptable — rare in practice, and old session ages out.
- Backfilling the ~20 orphaned single-turn sessions that are already in Valkey. Historical fragmentation stays.
- Supporting stitching for `/v1/chat/completions`. Chat-completions is stateless by design; we already mint a new session per request and that behavior is intended.
- Replacing the `previous_response_id` chain as the authoritative mechanism. Stitching is a fallback that produces the same shape, not a competitor.

## Decisions

### 1. Trigger: when to stitch

**Decision:** The gateway enters the stitching path **only** when all of the following hold on an incoming `/v1/responses` request:

- `previous_response_id` is absent or empty.
- The parsed `input` contains **more than one** message (`len(inputItems) > 1`).
- `store` is `true` (default). A `store: false` request does not write the hash index.

In every other case, the gateway uses its existing logic: either walk the chain (if `previous_response_id` is set) or mint a fresh session (if input is a single message — a genuinely new conversation).

**Rationale:** Single-message input is genuinely the start of a conversation; stitching against it would produce false merges. Client-side-state replays always have ≥ 2 messages because the client sends at least `[prior_assistant, new_user]` on any turn 2+.

**Alternatives considered:**
- *Stitch unconditionally whenever `previous_response_id` is empty.* Would falsely merge any new single-message request whose first turn happens to hash-collide with an existing session's prefix-of-one. Rejected.
- *Require the client to set a flag.* Defeats the purpose — we're trying to support unmodified Open WebUI.

### 2. Hash input: what we hash

**Decision:** We hash the result of `stitchHash(input[:-1])` where `stitchHash` is:

1. Flatten each item's `content` via the same `flattenContent` helper we use to feed the LLM. This collapses `input_text`/`output_text`/`text` content parts into single strings joined by single spaces.
2. Build a deterministic canonical byte form: for each item in order, emit `role + "\x00" + content + "\x1e"`. Role is lower-cased; roles without content are skipped.
3. SHA-256 the canonical bytes. Use the lowercase hex digest as the key suffix.

**Rationale:** We must hash the same information the LLM ultimately sees, so a client that sends `content: "hello"` and one that sends `content: [{type: "input_text", text: "hello"}]` hash to the same key (they represent the same user turn). Role-separation and the record-separator byte make the hash collision-resistant against pathological concatenations ("user:hi assistant:" vs "user:hi assistant"). SHA-256 is overkill for volume but it costs nothing and eliminates collision concerns for free.

**Alternatives considered:**
- *JSON-stringify the items and hash that.* Dependent on key order and on any incidental Unicode normalization differences. Rejected in favor of explicit canonicalization.
- *Hash the raw bytes of each content part.* Leaves us vulnerable to the `content: "hello"` vs `content: [{text:"hello"}]` ambiguity — same LLM prompt, different hash. Rejected.

### 3. Index key, value, TTL

**Decision:**
- Key format: `session_hash:<hex>` where `<hex>` is the 64-character lowercase hex SHA-256.
- Value: the `session_id` (UUID string).
- TTL: `SESSION_HASH_TTL_HOURS` (default 24h). Set on every write via `SET ... EX`. An active conversation's entry is rewritten on every turn — the TTL is an "inactivity timeout", not a hard conversation lifespan.

**Rationale:** One canonical key per "conversation as seen at turn N". TTL bounds the steady-state size of the index. 24 hours is a pragmatic default — long enough to cover "I'll finish this tomorrow morning", short enough that abandoned conversations don't pile up. Configurable for operators who want to tune it.

**Alternatives considered:**
- *No TTL, purge manually.* Adds operational burden and a new janitor process. Rejected.
- *Store more than `session_id` in the value (e.g. last_response_id, last_updated).* Simplifies step 5 below slightly but duplicates data already in `response:*` hashes. Rejected — keep the index lean.

### 4. Stitching flow

**Decision:** The gateway's `/v1/responses` handler, when the trigger in (1) fires:

1. Compute `prefixHash = stitchHash(inputItems[:-1])`.
2. `sessionID, ok := responses.LookupSessionByPrefixHash(ctx, prefixHash)`.
3. If `ok`:
   - Reuse `sessionID`.
   - `prevRespID, _ := responses.GetLastResponseID(ctx, sessionID)`. Use this to populate the new response's `PreviousResponseID`, so `WalkChain` from the new response returns the full stitched chain.
4. Else:
   - Mint a new session via `response.NewSessionID()` (current behavior).
   - `prevRespID = ""`.
5. Proceed with the rest of the existing flow (publish to stream, await reply, store response).
6. After the response is stored, compute `newHash = stitchHash(inputItems ++ newAssistantItem)` where `newAssistantItem` is the stored `OutputItem`. Write `session_hash:<newHash>` → `sessionID` with TTL. This is the hash the client's **next** turn's prefix will match.

Steps 2 and 6 each translate to exactly one Valkey round-trip.

**Rationale:** Localizing this to the gateway keeps the rest of the system ignorant of the stitching concept. The response store gains two small helpers (`LookupSessionByPrefixHash`, `StoreSessionPrefixHash`) and that is the extent of the cross-cutting surface.

### 5. Concurrency and collisions

**Decision:**
- Concurrent turns for the same `session_id`: extremely rare (user can only submit one turn at a time in any sane UI) but handled correctly — both requests would hash to the same prefix and race on the lookup. The first writer wins, the second sees its lookup hit the index and attaches to the same session. Order of responses in the session list follows XADD order, which Valkey serializes globally.
- Two genuinely different conversations hashing to the same prefix (e.g. both start with just "hi"): last-writer-wins. The newer conversation extends the session. This is an expected quirk of short, generic first prefixes; over a 24h window and with realistic content, collisions on substantial conversations are negligible. A future refinement could scope the index by user/auth identity.
- No locking. Valkey operations are individually atomic. A lost hash-index write (e.g. Valkey transient error) simply means the next turn mints a new session — degrades gracefully to pre-stitch behavior.

**Rationale:** Locking introduces failure modes (held locks, stale locks) disproportionate to the risk being mitigated. The worst case from a race is a duplicate session, which is exactly what we have today without stitching — strictly no worse than the baseline.

### 6. Store=false handling

**Decision:** Requests with `store: false` bypass the stitching path entirely. No index lookup, no index write. Non-stored responses cannot be chained regardless of mechanism.

**Rationale:** Stitching is inherently a storage-coupled concept. If the operator has opted out of storing this exchange, we should not leave a hash-index entry pointing at a `session_id` whose `session:X:responses` list does not contain the response.

### 7. Instrumentation

**Decision:** Log at INFO on every stitching decision:

- `stitch_matched` — stitching triggered, hash lookup hit, reusing `session_id`. Fields: `correlation_id`, `session_id`, `prefix_hash` (first 8 hex chars), `previous_response_id`.
- `stitch_minted` — stitching triggered, hash lookup missed, minting new session. Fields: `correlation_id`, `session_id`, `prefix_hash` (first 8 hex chars).
- `stitch_index_write_failed` — post-storage index write returned an error. WARN. Fields: `correlation_id`, `session_id`, error.

**Rationale:** Without these logs, the stitching behavior is invisible. We want operators to be able to grep a single conversation's logs and see that turn 2 extended turn 1's session via hash `abc12345`.

## Risks / Trade-offs

- [Prefix collision between unrelated short conversations] → Two users' "hello" get merged into one session. *Mitigation:* unlikely to matter at the "hello" level (no meaningful retro signal in those sessions); acceptable trade. Long-term, scope the hash key by user identity once auth is in place.
- [Client mutates a prior turn] → Hash diverges, new session minted, old one ages out. *Mitigation:* accepted as rare and self-healing. No operator intervention required.
- [Valkey transient error on index write] → Next turn misses the lookup and mints a new session; the turn-1 session becomes orphaned. *Mitigation:* ERROR-level log so operators can see it happened. Exactly equivalent to today's behavior — strictly no regression.
- [TTL too short cuts a real conversation] → User comes back after 25 hours; new session is minted, older one orphaned. *Mitigation:* 24h default is generous; configurable. Trade-off between stitching fidelity and Valkey key-space cleanliness.
- [Large prefix hashing cost] → A 200-turn conversation means we hash a 200-message prefix on every request. *Mitigation:* SHA-256 on a few KB of text is microseconds; dwarfed by LLM latency. Not a concern at any realistic scale.

## Migration Plan

1. Merge this change.
2. Build new images for gateway and any service that links the response-store helpers. (Only gateway in practice — `responses.Store` is used by gateway directly.)
3. `docker compose up -d --build gateway`.
4. Verify: send two consecutive Open WebUI-shape requests with no `previous_response_id` but matching prefixes. Before: two separate sessions. After: one session with two responses. Confirm via `KEYS session:*:responses` + `LLEN session:<id>:responses == 2`.
5. Let Open WebUI traffic accumulate for a day; run retro's `skill_creation` job and verify it now fires on long sessions.

Rollback: revert the merge and redeploy. No data migration either direction. Existing `session_hash:*` keys harmlessly expire via their TTL.

## Open Questions

- Should the TTL refresh on every turn (current proposal) or be an absolute "conversation start + 24h" bound? Leaning refresh-on-turn; revisit if we see pathological "forever-active" conversations piling up index entries. Neither affects correctness — only steady-state index size.
- Should we expose a `conversation_id` concept to clients, so a well-behaved client can pin to a specific server-side session without the hash dance? Out of scope for this change; revisit if we build a first-party client that could use it.
- When scope-by-user-identity becomes viable (auth landing in a future change), the hash key will become `session_hash:<user_id>:<hex>` and prefix collisions between users disappear. Noted here so that future change's design doesn't forget.
