## 1. Assembler restructure

- [x] 1.1 In `internal/context/assembler.go`, remove the `if len(memories) > 0 { systemContent += ... }` branch so `systemContent` is always exactly `a.systemPrompt`.
- [x] 1.2 Add a package-level helper `formatContextBlock(memories []Memory) string` that returns `"<context>\n- {m.Content}\n...\n</context>\n\n"` for non-empty input and `""` for empty input. Delete the old `formatMemories` helper (or replace its body).
- [x] 1.3 In `Assemble`, before rendering, sort a local copy of `memories` by `Score` descending using `sort.SliceStable` (ties break by input order).
- [x] 1.4 In `Assemble`, decorate the `userMessage`: build a local `decorated := userMessage` with `decorated.Content = formatContextBlock(sortedMemories) + userMessage.Content`. Append `decorated` (not the original `userMessage`) to `assembled`.
- [x] 1.5 Confirm by inspection that `Assemble` does not mutate its arguments: the input `userMessage` struct and `memories` slice are not modified in place.

## 2. Assembler unit tests

- [x] 2.1 Create (or expand) `internal/context/assembler_test.go` with a test `TestAssemble_SystemPromptByteStable` that calls `Assemble` twice with different memory sets and asserts the system message content is byte-identical both times and equal to the configured prompt.
- [x] 2.2 Add `TestAssemble_MemoriesFoldedIntoUserTurn` asserting the last message's role is `"user"` and its content equals `"<context>\n- A\n- B\n</context>\n\n" + original` for memories `[{Content:"A",Score:0.9},{Content:"B",Score:0.7}]` and a user content `original`.
- [x] 2.3 Add `TestAssemble_MemoriesSortedByScoreDesc` passing memories with scores `[0.4, 0.9, 0.7]` and asserting the rendered order is `[0.9, 0.7, 0.4]`.
- [x] 2.4 Add `TestAssemble_EmptyRecallPassesUserThrough` asserting that when `memories` is empty or nil, the last message's content equals the original user content exactly, with no `<context>` substring present.
- [x] 2.5 Add `TestAssemble_OnlyContentRendered` passing a memory with all fields populated (`Concept`, `Category`, `Score`, `Why`) and asserting that the rendered line is exactly `"- " + Content + "\n"` and that none of the other fields appear anywhere in the assembled output.
- [x] 2.6 Add `TestAssemble_AtMostOneSystemMessage` asserting the assembled output contains exactly one element with role `"system"` and that it is at index 0.
- [x] 2.7 Add `TestAssemble_DoesNotMutateInputs` asserting that the passed-in `userMessage` and `memories` values are unchanged after `Assemble` returns.

## 3. Manager wiring sanity check

- [x] 3.1 In `internal/context/manager.go`, confirm no code path relies on the prior behavior of memories appearing in the system prompt. If any log or downstream handoff inspected `assembled[0].Content` for memory substrings, remove or update it. (Verified: no hits — `rg "assembled\[0\]|Relevant Context|formatMemories"` in manager.go returns empty.)
- [x] 3.2 Confirm `m.muninn.Recall` is still called on every request with `userMsg.Content` as the query and `m.recallLimit` as the cap. (Verified at `internal/context/manager.go:101`.)

## 4. Storage invariant guard (non-code, spec-aligned)

- [x] 4.1 Verify by reading `internal/gateway/responses.go` that both `handleResponsesNonStreaming` and `handleResponsesStreaming` still set `resp.Input = currentTurnInput(inputItems)` — i.e. raw last user item only. (Verified at `internal/gateway/responses.go:155,158` non-streaming and `:261,264` streaming; both assign `turnInput := currentTurnInput(inputItems)` → `Input: turnInput`.)
- [x] 4.2 Optional assertion test skipped: `Assemble` never reaches gateway storage (the decoration lives only in `ContextAssembledPayload.Messages`, published on a different stream). Task 2.7 (`TestAssemble_DoesNotMutateInputs`) + task 4.1's read-check together cover the spec's "Raw user turn persisted" scenario; a gateway-side duplicate would exercise stitch-plumbing already covered by `TestWriteStitchIndex_StoresHashOfFullTurn`.

## 5. End-to-end verification

- [x] 5.1 `docker compose up -d --build context-manager` against the dogfood stack.
- [x] 5.2 Captured wire via `XREVRANGE stream:agent:main-agent:requests` on valkey (broker logs drowned by unrelated NOGROUP noise). Seeded 3 food memories into Muninn `default` vault; ran query `"What is something good to eat?"`.
- [x] 5.3 Verified on live stack: `messages[0].role=="system"`, `messages[0].content=="You are a helpful assistant."` (byte-equal to configured prompt, no `## Relevant Context`, no memory text). `messages[-1].role=="user"`, content starts with `<context>\n- Jason loves spicy Thai and Sichuan cuisine\n- ...`. LLM reply addressed Jason by name and recommended vegetarian Thai/Sichuan dishes — symptom resolved. (Note: required temporarily lowering `config:memory.recall_threshold` from 0.5 → 0.1 because Muninn's `final` score calibration on this dataset sits below 0.5; this is a Muninn scoring concern independent of this change. Threshold restored to 0.5 after verification.)
- [x] 5.4 Sent turn 2 with `previous_response_id` from turn 1. Wire trace: `messages[0].content` byte-identical to turn 1's system. `messages[1]` (raw user from turn 1) and `messages[2]` (assistant from turn 1) both have no `<context>` substring — history is raw. Only `messages[3]` (current user) carries the `<context>` block.
- [x] 5.5 Verified: prior to seeding memories, `context_muninn_recall` logs showed `memory_count:0, outcome:ok` and the wire `messages[-1].content` was the raw user turn with no `<context>` block.

## 6. Spec archive prep

- [x] 6.1 After implementation and verification, confirm `openspec status --change fold-memories-into-user-turn` reports all tasks complete; proceed to `/opsx:archive fold-memories-into-user-turn`.
