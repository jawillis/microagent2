## Why

Open WebUI — our dogfood client — operates the Responses API in "client-side state" mode: it never sends `previous_response_id`, even after we emit `response.created`. Instead it POSTs the full conversation in the `input` array every turn. Our gateway sees no chain hint, so it mints a new `session_id` per turn, stores the new response under that session, and that session is never touched again. After ~20 turns, the live Valkey store contained 23 sessions — 21 of them with exactly one response. Every user↔assistant exchange in Open WebUI becomes its own orphaned single-turn "session". The retro-agent's memory-extraction job sees fragments instead of conversations; skill-creation is effectively disabled because it gates on `min_history_turns > 1`; the dashboard's session list becomes unusable as one-entry-per-turn junk accumulates forever. The OpenAI Responses API spec allows both "server-side chain via `previous_response_id`" and "client-side full-history replay" — a spec-compliant server needs to make downstream storage coherent in both modes.

## What Changes

- New **content-prefix hash index** in Valkey: `session_hash:<hex>` → `session_id`, with TTL. The gateway uses this index to stitch consecutive client-side-state turns into a single growing session.
- Gateway `/v1/responses` gains a stitching path that fires only when `previous_response_id` is absent AND `input` contains more than one message (i.e. the client is replaying history).
- After each stored response, the gateway writes the hash of the new full conversation (`input + new assistant turn`) to the index so the next turn's prefix hash matches it.
- TTL on hash entries (default 24h, configurable via env `SESSION_HASH_TTL_HOURS`) so dormant conversations' stitching state does not leak forever; active conversations have their entry refreshed every turn.
- **BREAKING (behavioral, not API)**: Open WebUI traffic will now be stored as growing multi-turn sessions rather than one-turn fragments. Existing single-turn sessions are left as-is — no backfill.
- The `previous_response_id` chain walker already in place remains the authoritative path for clients that do thread properly. The stitching path produces the same canonical shape (a `session:*:responses` list growing over turns, each response linking to the previous via `previous_response_id`) so downstream consumers do not need to know which mode produced a given session.

## Capabilities

### New Capabilities
- None.

### Modified Capabilities
- `gateway-api`: `/v1/responses` gains conversation-stitching behavior for client-side-state requests.
- `response-chain`: adds the `session_hash:*` index contract — stable hash function, TTL, write points.
- `session-management`: documents that sessions may be extended via stitching in addition to explicit `previous_response_id`, and clarifies the session lifetime implications.

## Impact

- **Downstream coherence**: retro-agent, muninn memory-extraction, skill-creation, and the dashboard all start seeing real multi-turn conversations from Open WebUI. `skill_creation`, which was effectively disabled because no Open WebUI session ever cleared `min_history_turns`, becomes live again.
- **Valkey storage**: one additional key per active conversation, sized ~40 bytes (`session_hash:<64-hex>` → UUID). TTL bounds steady-state growth.
- **Gateway latency**: one `GET` + one `SET` (with `EX`) on the stitching path. Both are local Valkey, sub-millisecond. Untouched paths (explicit `previous_response_id` or single-message input) see no change.
- **Backwards compatibility**: clients that thread via `previous_response_id` see no behavioral change. Clients sending single-message `input` see no change. Only the "multi-message `input` with no chain hint" path shifts from "mint a fresh session" to "reuse a matching session if one exists".
- **Non-goals**: no semantic/fuzzy matching (exact hash over flattened content only); no retry of existing orphaned single-turn sessions (accept historical fragmentation); no changes to retro-agent logic; no client-side changes (Open WebUI stays as-is).
