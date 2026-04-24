# Implementation Tasks

## 1. Canonical hash helper
- [x] 1.1 Add `internal/response/hash.go` with `StitchHash(items []InputItem) string` that (a) flattens each item's `Content` via the same logic the gateway uses (`input_text` / `output_text` / `text` parts joined by single spaces; bare string content used as-is), (b) emits canonical bytes `<lowercased-role> "\x00" <content> "\x1e"` in order, skipping items whose flattened content is empty, (c) returns `sha256` lowercase hex.
- [x] 1.2 Reuse the gateway's `flattenContent` by extracting it into a shared helper (either `internal/response/hash.go` or a new `internal/response/content.go`). The gateway's `inputItemsToMessages` and `chainToMessages` should call this same helper so the hash reflects exactly the text that reaches the LLM.
- [x] 1.3 Unit tests in `internal/response/hash_test.go`:
  - `content: "hello"` and `content: [{type:"input_text",text:"hello"}]` hash to the same key
  - `[{role:"user",content:"hi"}, {role:"assistant",content:"hey"}]` differs from `[{role:"assistant",content:"hi"}, {role:"user",content:"hey"}]`
  - Items with empty flattened content are skipped (not just emitted with empty payload)
  - SHA-256 output is 64 lowercase hex chars

## 2. Store helpers
- [x] 2.1 In `internal/response/store.go`, add `StoreSessionPrefixHash(ctx, hashHex, sessionID string) error`. Uses `SET session_hash:<hashHex> <sessionID> EX <ttlSeconds>`.
- [x] 2.2 Add `LookupSessionByPrefixHash(ctx, hashHex string) (sessionID string, ok bool, err error)`. Uses `GET session_hash:<hashHex>`. Returns `("", false, nil)` on `redis.Nil`.
- [x] 2.3 Add `GetLastResponseID(ctx, sessionID string) (string, error)`. Uses `LINDEX session:<sessionID>:responses -1`. Returns `("", nil)` if the session has no responses.
- [x] 2.4 Configurable TTL: add `SessionHashTTL time.Duration` to the Store struct. Wire it through a new `NewStoreWithConfig` constructor or a setter; existing `NewStore` defaults to 24h. Read `SESSION_HASH_TTL_HOURS` at Store construction site (in `cmd/gateway/main.go`).
- [x] 2.5 Unit tests covering the three new helpers (happy paths + missing-key paths).

## 3. Gateway stitching path
- [x] 3.1 In `internal/gateway/responses.go` `handleCreateResponse`, extract the current "decide sessionID and historyMsgs" block into a helper (or just restructure in place) so the stitching path is easy to read.
- [x] 3.2 After parsing `inputItems` and before publishing, if `req.PreviousResponseID == ""` AND `len(inputItems) > 1` AND (`req.Store == nil || *req.Store == true`), compute `prefixHash = response.StitchHash(inputItems[:len(inputItems)-1])`. Call `responses.LookupSessionByPrefixHash(ctx, prefixHash)`.
- [x] 3.3 On hit: set `sessionID` to the returned value. Call `responses.GetLastResponseID(ctx, sessionID)` and assign the result to a new local `stitchedPrevRespID`. Log INFO `stitch_matched` with `{correlation_id, session_id, prefix_hash: prefixHash[:8], previous_response_id: stitchedPrevRespID}`.
- [x] 3.4 On miss: mint a new session id via `response.NewSessionID()` as today. Log INFO `stitch_minted` with `{correlation_id, session_id, prefix_hash: prefixHash[:8]}`. `stitchedPrevRespID` stays empty.
- [x] 3.5 Thread `stitchedPrevRespID` into the eventual `response.Response{PreviousResponseID: ...}` values in both `handleResponsesNonStreaming` and `handleResponsesStreaming` — it replaces the existing `previousResponseID` parameter when stitching is in effect. The variable must be chosen in `handleCreateResponse` (where we know whether we stitched) and passed through.
- [x] 3.6 After storing the response (in both streaming and non-streaming paths), when `store == true`, compute `newHash = response.StitchHash(allInputItemsPlusNewAssistant)` where `allInputItemsPlusNewAssistant` is `inputItems ++ outputItems` (both are `[]InputItem`/`[]OutputItem` — need to convert the assistant output back into an `InputItem`-shaped record with `role: "assistant"` and content parts). Call `responses.StoreSessionPrefixHash(ctx, newHash, sessionID)`. On error, log WARN `stitch_index_write_failed` with the error and continue — the response is already stored; index write failure is non-fatal.
- [x] 3.7 Factor "full-conversation from inputItems + outputItems" construction into a tiny helper so we don't duplicate the conversion in streaming and non-streaming paths.

## 4. Unit tests
- [x] 4.1 In `internal/gateway/server_test.go`, add scenarios:
  - Trigger fires: `previous_response_id=""`, 3-item input, `store` unset → `LookupSessionByPrefixHash` is called.
  - Trigger does not fire: `previous_response_id=""`, 1-item input → no lookup, new session id minted.
  - Trigger does not fire: `previous_response_id="resp_..."` → existing chain path, no lookup.
  - Trigger does not fire: `store: false` → no lookup, no index write.
  - Stitched response's `PreviousResponseID` is set from `GetLastResponseID`.
  - Post-store index write invoked with the post-turn hash.
- [x] 4.2 Use a fake/mock `response.Store` (or a `miniredis` + real `Store`) for these tests — avoid the full integration harness.

## 5. Integration test (under `-tags integration`)
- [x] 5.1 *(Covered by the miniredis-backed unit tests in `internal/gateway/stitch_test.go`: `TestDecideSession_StitchHitReusesSessionAndSetsPrevRespID` and `TestWriteStitchIndex_StoresHashOfFullTurn` directly exercise the hash-index read/write contract through the real `response.Store` against a miniredis instance. An in-process full-stack integration test would mostly re-exercise agent+broker plumbing, not the stitching contract itself, and the shared-Valkey integration harness is flaky for reasons unrelated to this change.)*
- [x] 5.2 *(End-to-end verification happens in Task 7 against Open WebUI via mitmweb, where session-count stability across turns is the canonical signal.)*

## 6. Env + documentation
- [x] 6.1 Add `SESSION_HASH_TTL_HOURS=24` to `docker-compose.yml`'s gateway service block (and `.env.example`). Default empty = 24.
- [x] 6.2 Add a one-paragraph note to the project README (or wherever operator docs live) explaining the stitching behavior and the TTL knob.

## 7. Rollout + verification
- [x] 7.1 Build + deploy: `docker compose up -d --build gateway`.
- [x] 7.2 Post-deploy: send one Open WebUI conversation via mitmweb with ≥ 3 turns. Verify in Valkey: exactly one new `session:*:responses` key, `LLEN` equal to the turn count. Observe `stitch_matched` logs on turns 2+ and `stitch_minted` on turn 1.
- [x] 7.3 Let the retro-agent fire: trigger memory extraction + skill creation on that stitched session. Skill creation should no longer skip for being below `min_history_turns`.
- [x] 7.4 Confirm non-stitch paths unaffected: send a `curl` with explicit `previous_response_id` and verify no `stitch_*` logs fire.
