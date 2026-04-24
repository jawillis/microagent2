## Context

The context-manager assembles the messages array that flows from gateway → main-agent → broker → llama.cpp. Today `internal/context/assembler.go` does:

```go
systemContent := a.systemPrompt
if len(memories) > 0 {
    systemContent += "\n\n" + formatMemories(memories)   // mutates system per turn
}
assembled = [ {system: systemContent}, ...history, userMessage ]
```

Two consequences, both confirmed in live testing against the project's llama.cpp server (http://192.168.10.75:9876, model `unsloth`):

1. **Prefix cache poisoning.** The `system` message's byte content changes every turn (different memories recalled), so any prefix-caching LLM server cannot reuse the `[system, ...history]` prefix across turns. The current server reports `cached_tokens: 0` even for identical back-to-back requests (cache-reuse config is out of scope here), but the format change removes the structural barrier regardless.
2. **Storage risk.** If anything downstream ever reads the *assembled* messages as the canonical history for a session (rather than `resp.Input` / `resp.Output`), decorated content would bleed into future turns. Today nothing does this, but the invariant is implicit; the new spec makes it explicit.

A second design option — emitting memories as a *second* `system`-role message immediately before the user turn — was also tested live and rejected:

| Variant tested | Query: "What should I order for dinner?" (memories: allergic to shellfish, loves spicy) | Used facts? |
|---|---|---|
| Second `system` message before user | *"What kind of food do you feel like eating?"* | ❌ |
| Memories folded into user (markdown heading) | *"Order a spicy dish that excludes shellfish."* | ✅ |
| Memories folded into user (XML `<context>`) | *"Order a spicy dish that contains no shellfish."* | ✅ |
| Memories folded into user (XML + concept prefix) | *"Order a spicy dish that strictly avoids shellfish."* | ✅ |
| Prose lead-in ("Things I remember about you:") | *"Since I do not know if you are Jason or if you share his preferences, please clarify..."* | ❌ |

The unsloth/gpt-oss chat template appears to flatten or drop a second `system` message — a common failure mode across chat templates. Prose lead-ins caused the model to dissociate from the speaker identity. XML-delimited folding into the user turn is the only variant that reliably worked.

Primary stakeholders: the context-manager (direct change) and the LLM-serving path (behavioral beneficiary). Gateway, retro-agent, broker, and main-agent see no change — they read `resp.Input` / `resp.Output` which are unaffected.

## Goals / Non-Goals

**Goals:**
- The assembled `system` message is byte-identical across turns for a given config. Any change to its contents must come from a config change, never from recall results.
- Recalled memories appear to the LLM on every turn they are recalled — the behavior the user was missing — but are placed at the tail of the user turn, cache-friendly and template-friendly.
- The format is unambiguous, simple to generate, and leaves room to extend without breaking (XML tags accept attributes later).
- The stored `resp.Input` is never decorated — history reconstruction from the response store remains clean across turns.
- Empty-recall behavior is explicit: no phantom tags, no empty sections.

**Non-Goals:**
- Enabling llama.cpp `--cache-reuse` or any server-side prefix-cache configuration. That is a separate operational change; this one only removes the structural barrier.
- Changes to recall (muninn) — the query is already `userMsg.Content` on every turn and the recall logic is unchanged.
- Changes to how `resp.Input` is computed on the gateway side — already correct (`currentTurnInput` stores the raw last user item).
- Changes to retro-agent, dashboard, chain-walker, or any consumer of stored history.
- Escape-handling for memories whose `Content` contains a literal `</context>` token. Memory content today comes from Muninn and is expected to be plain text. Treated as a known simplification; if it becomes a problem, a later change can add escaping without altering the wire shape.
- A configurable format. The format is fixed by the spec so its behavior is predictable and so consumers (log readers, trace viewers) can rely on it.

## Decisions

### 1. Placement: fold into user turn

**Decision:** The decoration is applied to the `Content` of the final `user`-role message in the assembled output. Shape:

```
<context>
- {memory[0].Content}
- {memory[1].Content}
...
</context>

{userMessage.Content}
```

**Rationale:** Live testing (table above) shows this is the only variant that both (a) produces correct model behavior on this template and (b) keeps the system prompt byte-stable.

**Alternatives considered:**
- *Leave memories in the system prompt (current behavior).* Rejected — destroys prefix cache reuse and is the direct cause of the user's observed issue.
- *Second `system`-role message before the user turn.* Rejected — test showed the model ignored it.
- *Prose lead-in inside the user turn.* Rejected — test showed the model dissociated from the speaker.
- *Separate tool-role or assistant-role injection.* Chat Completions does not define a "context" role; tool role has strict schema. Rejected as more surface for less benefit.

### 2. Format: XML tags, content only, score-ordered

**Decision:** `<context>` and `</context>` as the delimiters. Each memory is rendered as a single line: `- ` + `Memory.Content` + `\n`. Memories are sorted by `Score` descending before rendering. A single blank line separates `</context>` from the user's actual content.

**Rationale:**
- XML tags give an unambiguous delimiter the model recognizes as "not the user's words" (live-tested). They also accept attributes in a future change without breaking format (`<context source="muninn" recency="recent">`).
- Content-only minimizes tokens and avoids feeding the model raw scores (which it cannot calibrate to). `Concept` / `Category` / `Why` are provenance for the retro-agent, not the model.
- Score-descending ordering means if the LLM truncates, the most relevant memories survive.
- The blank line before the question is a small robustness nudge — tested cleanly.

**Alternatives considered:**
- *Markdown `## Relevant Context` heading.* Functionally equivalent in the test, but the boundary is fuzzier (model may attribute headings to the user) and it doesn't extend cleanly.
- *Fenced code block.* Some chat templates treat code fences specially; avoids the risk for no gain.
- *Include `Concept` as a prefix (`- [allergies] ...`).* Worked in testing but added tokens for no measurable benefit. Non-breaking addition if needed later.

### 3. Empty recall: pass user turn through unchanged

**Decision:** If `len(memories) == 0`, the user turn's `Content` is emitted unchanged. No `<context></context>` block.

**Rationale:** An empty tag wastes tokens and could mislead the model into thinking "the retrieval step ran and found nothing relevant" is meaningful signal.

**Alternative considered:** *Always emit the tag, even empty.* Simpler logic but more tokens and worse signal. Rejected.

### 4. Storage invariant: `resp.Input` is raw

**Decision:** The gateway stores the raw user turn on `Response.Input`. The context-manager's decoration is confined to `ContextAssembledPayload.Messages`. The two representations are not the same and must not be conflated.

**Rationale:** History reconstruction (via `chainToMessages` on stored `Response` objects, or `GetSessionHistory`) replays user/assistant pairs to the context-manager on future turns. If that history carried decorations from past turns, those (now-stale) memories would be re-presented alongside fresh memories every turn — exactly the cache-poisoning and staleness problem we're eliminating.

The invariant is already respected by the current gateway code (`currentTurnInput` in `internal/gateway/responses.go`). This change makes the invariant explicit in the spec so it can't silently regress.

**Alternative considered:** *Store the decorated form, strip decorations on read.* Adds a strip step to every reader (retro, dashboard, chain-walker). Rejected — more surface, more failure modes, zero benefit.

### 5. System prompt invariant: byte-stable

**Decision:** The `system` message in the assembled output equals `assembler.systemPrompt` exactly. No concatenation, no mutation, no per-turn content.

**Rationale:** Locks in the prefix-cache-friendly shape. Also makes the assembler trivially testable — the system message can be asserted equal to the input system prompt with a single `require.Equal`.

**Alternative considered:** *Allow a config-driven "append static block" option.* Scope creep; no current requirement. Rejected.

### 6. Ordering before rendering

**Decision:** Sort the `[]Memory` slice by `Score` descending in the assembler, not in the recall client. Stable sort, ties broken by input order.

**Rationale:** The assembler is the only place that needs to care about ordering for presentation. MuninnDB may or may not return ordered results depending on its implementation; relying on it is fragile. Sorting in the assembler makes the contract local and testable.

## Risks / Trade-offs

- [Memory `Content` contains a literal `</context>` string] → The tag delimiter could be broken by adversarial or weird content. *Mitigation:* Memories come from the project's own Muninn extraction; content is plain text. Not expected in practice. A future change can add escaping (e.g. replace `</` with `<\/`) without changing the external format. Documented as a known simplification.
- [This server's `cached_tokens` metric is zero regardless of what we do] → The measurable cache-hit improvement is not observable from this change alone; we are only removing the structural barrier. *Mitigation:* Call out in the proposal and expect a follow-up operational change to enable `--cache-reuse` or switch to a server that reports cached tokens.
- [Chat template that *also* dislikes XML tags in user content] → Theoretically possible with some obscure template. *Mitigation:* Not seen in our target template; test coverage in `assembler_test.go` pins the expected wire format, so any swap to a new template/server will surface differences fast.
- [`resp.Input` store contract depends on gateway discipline] → Any future change that sets `Response.Input = inputItems` (the full replay) instead of `currentTurnInput(inputItems)` would accidentally persist pre-decoration history. *Mitigation:* The spec delta documents the invariant. An assertion in a gateway test (that `resp.Input` after a stitched turn equals the raw last user item, not a decorated form) would be a reasonable belt-and-suspenders follow-up.
- [Score-order destabilizes memory presentation across identical runs] → If MuninnDB returns the same memories with slightly different scores on different runs, ordering could flap. *Mitigation:* Stable sort with ties in input order; acceptable wobble; LLM output is not expected to be bit-stable anyway.

## Migration Plan

1. Merge this change.
2. Rebuild and redeploy the context-manager: `docker compose up -d --build context-manager`.
3. Verify via logs: `context_published` log still fires on every turn; `assembled_count` is unchanged (system + history + user = same count before and after). The change is invisible in counts but visible in content.
4. Trace a sample turn end-to-end. Expected in the outbound messages to main-agent:
   - `messages[0].role == "system"`, `messages[0].content == configured_system_prompt` (byte-identical across turns).
   - `messages[-1].role == "user"`, `messages[-1].content` starts with `<context>\n- ...` if any memories were recalled, otherwise is the raw user turn.
5. Verify the end-to-end LLM response now uses recalled memories — the original symptom. Pick a session with known memories and a food-related query; confirm the response references the memories.

Rollback: revert the merge and redeploy. No data migration; no store changes.

## Open Questions

- Should the format later carry memory *recency* or *confidence* as attributes on the `<context>` tag (e.g. `<context recency="recent" threshold="0.7">`) so the model can weight them? Deferred — current evidence is that bare `<context>` works fine for our model. Non-breaking addition when we have data to justify it.
- Should we cap the number of memories rendered per turn independently of `recall_limit`, e.g. to guard against an oversized context block eating the context window? Today `recall_limit` already bounds it. Revisit only if we see pathological cases.
