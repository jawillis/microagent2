## Why

Recalled memories are currently concatenated onto the static system prompt each turn, which (a) mutates the system byte-stream and destroys any KV prefix cache benefit the LLM server could offer, and (b) lets stale context bleed into future turns if anything downstream reads assembled messages as history. Live testing on the project's llama.cpp server also confirmed that placing memories as a *second* `system` role message is effectively dropped by the active chat template — the model ignored injected facts entirely. Folding memories into the user turn with an XML delimiter produces correct model behavior and keeps the system prompt byte-stable.

## What Changes

- The context-manager's assembler SHALL build the outbound `ContextAssembledPayload.Messages` as `[ {role:system, content:<static system prompt, unchanged>}, ...history, {role:user, content:<decorated user turn>} ]`.
- Recalled memories SHALL be serialized as an XML-delimited block prepended to the user turn content: `<context>\n- {m.Content}\n...\n</context>\n\n{userMessage.Content}`.
- Memories SHALL be sorted by `Score` descending before rendering, so truncation preserves the most relevant.
- Only `Memory.Content` reaches the LLM. `Concept`, `Category`, `Score`, `Why` remain available to other consumers but are not emitted into the assembled prompt.
- When recall returns zero memories, the user turn is passed through unmodified — no empty `<context></context>` block.
- **BREAKING (behavioral, not API)**: the assembled prompt shape changes. The wire between context-manager and main-agent carries a different `Messages` array. No client-visible change; no change to `resp.Input` storage; no change to history reconstruction.
- **Storage invariant** (reinforced, not new): `resp.Input` continues to store the RAW user turn. The `<context>` decoration lives only on the in-flight messages to the agent/broker/LLM. This prevents history replay from leaking old memories into future turns.
- **System prompt invariant** (new): the `system` message in the assembled output is byte-identical to the configured system prompt. No per-turn mutation permitted.

## Capabilities

### New Capabilities
- None.

### Modified Capabilities
- `context-management`: add requirements governing where recalled memories are injected, the byte-stability of the system prompt, and the raw-user-turn storage invariant. Existing recall / config / logging requirements are unchanged.

## Impact

- **Code touched**: `internal/context/assembler.go` (restructure `Assemble`), `internal/context/manager.go` (optional score sort if not done in assembler), new/expanded `internal/context/assembler_test.go`.
- **Cache behavior**: with a byte-stable system prompt, the llama.cpp server's prefix cache can hit the `[system, history...]` prefix across turns; only the decorated-user tail is new each turn. Exact savings depend on server `--cache-reuse` configuration (the current server reports zeroed cache metrics in testing, so measurable gains will land only after that is enabled separately — out of scope here).
- **Model behavior**: matches observed correct behavior from live A/B testing (XML-delimited context in user turn). Rejects the observed-bad prose-lead-in and second-system-message patterns.
- **Downstream consumers**: retro-agent, dashboard, and the chain-walker read history from `resp.Input` / `resp.Output`, which is unchanged. They see raw user/assistant pairs exactly as today.
- **Non-goals**: no changes to muninn recall path; no changes to how the gateway stores `resp.Input`; no changes to retro-agent / broker / main-agent; no new config knobs (format fixed by spec); no escape-handling for memory content that contains a literal `</context>` (treated as a known simplification given current memory provenance).
