## ADDED Requirements

### Requirement: System prompt byte-stability
The `system`-role message in the `ContextAssembledPayload.Messages` produced by the context manager SHALL be byte-identical to the configured system prompt (`config:chat.system_prompt`) on every request. The context manager SHALL NOT append, prepend, or otherwise mutate the system prompt based on recall results, session state, or any other per-turn input.

#### Scenario: System prompt unchanged when memories are recalled
- **WHEN** the context manager assembles a request for which `muninn.Recall` returned one or more memories
- **THEN** the `system`-role message in the published `ContextAssembledPayload.Messages` SHALL equal `config:chat.system_prompt` byte-for-byte, with no memory content or other per-turn content concatenated

#### Scenario: System prompt unchanged when no memories are recalled
- **WHEN** the context manager assembles a request for which `muninn.Recall` returned zero memories
- **THEN** the `system`-role message in the published `ContextAssembledPayload.Messages` SHALL equal `config:chat.system_prompt` byte-for-byte

#### Scenario: System prompt unchanged across consecutive turns of a session
- **WHEN** the context manager handles two consecutive requests for the same session
- **THEN** the `system`-role message contents of the two published `ContextAssembledPayload.Messages` SHALL be byte-identical

### Requirement: Recalled memories injected at the tail of the user turn
Recalled memories SHALL be injected into the `Content` of the final `user`-role message of the assembled output, using an XML-delimited `<context>` block preceding the user's original content. The context manager SHALL NOT emit recalled memories into the `system`-role message, and SHALL NOT emit a second `system`-role message containing recalled memories.

The exact serialization, when at least one memory is recalled, is:

```
<context>
- {memory[0].Content}
- {memory[1].Content}
...
</context>

{original userMessage.Content}
```

Rules:
- Memories SHALL be sorted by `Memory.Score` descending (stable sort; ties broken by input order) before rendering.
- Each memory line SHALL be `"- " + Memory.Content + "\n"`. No other fields of `Memory` (including `Concept`, `Category`, `Score`, `Why`) SHALL appear in the rendered block.
- A single blank line SHALL separate `</context>` from the original user content.

#### Scenario: Memories folded into user turn with XML delimiter
- **WHEN** `muninn.Recall` returns memories `[M1, M2]` (already sorted by score desc) for a user turn with content `"What should I eat?"`
- **THEN** the `user`-role message published to the target agent SHALL have `Content` equal to `"<context>\n- " + M1.Content + "\n- " + M2.Content + "\n</context>\n\nWhat should I eat?"`

#### Scenario: Memories sorted by score descending
- **WHEN** `muninn.Recall` returns memories with scores `[0.4, 0.9, 0.7]`
- **THEN** the rendered `<context>` block SHALL list them in score order `[0.9, 0.7, 0.4]`

#### Scenario: Only Memory.Content is rendered
- **WHEN** `muninn.Recall` returns a memory with `Content: "likes pizza"`, `Concept: "food"`, `Score: 0.8`, `Why: "extracted from turn 3"`
- **THEN** the rendered line for that memory SHALL be exactly `"- likes pizza\n"`, containing no other field values

#### Scenario: Empty recall leaves user content unchanged
- **WHEN** `muninn.Recall` returns zero memories for a user turn with content `"Hello"`
- **THEN** the `user`-role message published to the target agent SHALL have `Content` equal to `"Hello"`, with no `<context>` block emitted

#### Scenario: No second system message
- **WHEN** the context manager assembles any request
- **THEN** the published `ContextAssembledPayload.Messages` SHALL contain at most one message with role `"system"`, and that message SHALL be the first element of the array

### Requirement: Context-manager decorations are ephemeral
The `<context>` decoration applied by the context manager SHALL exist only on the in-flight `ContextAssembledPayload.Messages` bound for the target agent. The context manager SHALL NOT cause decorated content to be persisted to the response store, nor to be included in any message that is later consumed as canonical session history.

The raw user turn (prior to decoration) is what SHALL appear in any downstream history-reconstruction path, including but not limited to `response.Store.GetSessionMessages`, `response.Store.WalkChain`, and any gateway-side reconstruction of the `messages` array on future turns.

#### Scenario: Raw user turn persisted on the response store
- **WHEN** a request flows through the context manager with memories `[M1]` recalled, and the resulting LLM response is stored by the gateway
- **THEN** the `Response.Input` persisted in the response store SHALL contain the raw user turn content (e.g. `"What should I eat?"`), and SHALL NOT contain the `<context>` block or any memory content

#### Scenario: Subsequent-turn history is undecorated
- **WHEN** the context manager handles turn N+1 of a session whose turn N carried a decorated user message to the LLM
- **THEN** the `history` portion of the assembled `ContextAssembledPayload.Messages` for turn N+1 SHALL contain only the raw user/assistant content from turn N, with no `<context>` block and no memory content from turn N's recall
