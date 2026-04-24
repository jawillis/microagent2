## Context

Slice 1 added the wire format for tool calling end-to-end: the model can emit `tool_calls`, the broker reassembles them atomically, the agent receives them, the gateway exposes them. But nothing actually gets executed. Main-agent logs `tool_calls_observed_by_main_agent` and returns.

This slice makes the system agentic: tools exist (`list_skills`, `read_skill`), a registry maps names to invokable implementations, and main-agent runs a bounded loop that alternates between LLM calls and tool execution until the model finalizes. It also adds the skills store — a Claude-Skills-format filesystem catalog — behind those two built-in tools.

Stakeholders: main-agent (tool loop owner), skills/ store (filesystem), tools/ registry. The broker and context-manager are unchanged. The gateway gains a storage-side extension (persisting tool results to the Output items) so next-turn history reconstruction sees the agentic trace.

## Goals / Non-Goals

**Goals:**
- A skill is a self-contained directory under `SKILLS_DIR` with `SKILL.md` following Claude-Skills convention. Operators author and deploy skills by dropping files.
- `list_skills` and `read_skill` are the only way the model interacts with the catalog — no system-prompt dumping of skill bodies.
- Main-agent runs a bounded tool-execution loop per user turn. Pure-text turns are regression-free (one iteration, same output).
- Slot fairness: slots are held only during active LLM calls; tool execution runs without holding a slot.
- System-prompt byte-stability from slice 1 is **preserved**. Skill manifest injection happens at the main-agent layer, not the context-manager layer.
- Stored responses carry the full agentic trace (tool_calls + tool_results + final text) so subsequent turns reconstruct history correctly.

**Non-Goals:**
- No `shell` built-in. No arbitrary command execution. Deferred indefinitely; the MCP slice will be the path for shell-like tools.
- No MCP client, no external tools. That's slice 3.
- No skills hot-reload. Restart to pick up changes.
- No `allowed-tools` enforcement on loaded skills — we parse the field but don't act on it yet.
- No dashboard UI for skills. Filesystem is the authoring surface.
- No retro-agent tool support.

## Decisions

### D1: Skill manifest injection lives in main-agent, not context-manager

Slice 1's spec says the context-manager SHALL produce a byte-stable system prompt. Injecting skill names/descriptions into that system prompt violates that — but only if the context-manager does it. Main-agent, which already knows about the registered tools and skills, is the natural owner.

Flow:

```
context-manager:  Assemble → ContextAssembledPayload{Messages: [system(stable), ...history, user(decorated)]}
                                                           │
                                                           ▼
main-agent receives payload, before calling broker:
     messages[0].Content += "\n\n<available_skills>\n- name: desc\n...\n</available_skills>"
     (only if registry is non-empty)
```

The mutation is local and idempotent across turns (skills list is stable between main-agent restarts). Context-manager's spec guarantee stands.

*Alternatives:* append skill manifest as a separate system message (spec forbids multiple); put it in the last user message (noisy for the user-visible transcript and confuses retro-agent summarization); make context-manager aware of the tool registry (cross-module coupling and splits responsibility).

### D2: Tool abstraction

```go
// internal/tools/tool.go
type Tool interface {
    Name() string
    Schema() messaging.ToolSchema
    Invoke(ctx context.Context, argsJSON string) (string, error)
}

type Registry struct {
    tools map[string]Tool
}

func (r *Registry) Register(t Tool) error            // name collision = error
func (r *Registry) Schemas() []messaging.ToolSchema  // stable order (registration order)
func (r *Registry) Invoke(ctx, name, args) (string, error)
func (r *Registry) Manifest() []struct{ Name, Desc string }  // for system-prompt injection
```

No locking — registry is populated at startup and read-only thereafter. Hot-reload would need a mutex; deferred.

*Alternatives:* generics-parameterized tools (over-engineered); JSON-schema-validated args at the registry boundary (defer; tools currently parse their own args and the LLM is the only caller).

### D3: Skills store

```go
// internal/skills/store.go
type Manifest struct {
    Name         string
    Description  string
    AllowedTools []string   // parsed but not enforced this slice
    Model        string     // parsed but not enforced this slice
    body         string     // not exported; read via Body(name)
    sourcePath   string     // for diagnostics
}

type Store struct {
    manifests map[string]*Manifest
    order     []string  // deterministic iteration order
}

func NewStore(root string) (*Store, error)           // scans SKILLS_DIR once at init
func (s *Store) List() []*Manifest                   // deterministic order
func (s *Store) Get(name string) (*Manifest, bool)
func (s *Store) Body(name string) (string, bool)
```

Scanner: walk one level deep under root; for each subdir, look for `SKILL.md`. Missing frontmatter or missing `name`/`description` fields → log WARN, skip that directory.

Frontmatter parsing: split on the `---` separator lines, YAML-unmarshal the frontmatter block, take everything after as the Markdown body (trimmed of leading whitespace). Use `gopkg.in/yaml.v3`.

*Alternatives:* fs.FS-backed (over-engineered for v1); eager body loading vs lazy (eager is fine — skill bodies are small, O(KB) each); watch the directory with fsnotify (adds a dependency for a feature we're explicitly deferring).

### D4: Built-in tools

`list_skills`:
```json
{
  "type": "function",
  "function": {
    "name": "list_skills",
    "description": "List all available skills. Returns a JSON array of objects with name and description fields. Call this to discover what skills can be loaded with read_skill.",
    "parameters": {"type": "object", "properties": {}, "required": []}
  }
}
```

Invocation: ignores args; marshals `store.List()` to `[{"name":..,"description":..}]` JSON.

`read_skill`:
```json
{
  "type": "function",
  "function": {
    "name": "read_skill",
    "description": "Load the full instructions for a named skill and apply them. Use this when the user's task matches a skill's description. The returned content is authoritative skill instructions.",
    "parameters": {
      "type": "object",
      "properties": {
        "name": {"type": "string", "description": "Exact skill name as returned by list_skills"}
      },
      "required": ["name"]
    }
  }
}
```

Invocation: unmarshals `{name}`; returns the skill's Markdown body, or a JSON error string `{"error": "skill not found: <name>"}` if missing.

*Alternative considered:* combine into a single `skill(name?)` tool — list when no name, read when named. Rejected: two tools match how operators conceptualize it and map cleanly onto Claude Code's Skill semantics.

### D5: Main-agent tool loop

```
handleRequest(msg):
    payload = decode(msg)
    messages = inject_skill_manifest(payload.Messages)  // D1
    tools   = registry.Schemas()
    var assembled_text string
    
    for iter := 0; iter < TOOL_LOOP_MAX_ITER; iter++ {
        slotID = RequestSlot()
        content, tool_calls, err = Execute(messages, tools, onToken, onToolCall)
        ReleaseSlot()
        
        if err == ErrPreempted: return   // include what we have
        if err != nil: log and return
        
        if len(tool_calls) == 0:
            // model finalized with text
            publish_chat_response(content, final=true)
            break
        
        // model wants to call tools
        messages = append(messages, assistant{content, tool_calls})
        for call := range tool_calls:
            out, err := registry.Invoke(call.Name, call.Arguments)
            if err != nil: out = {"error": err.Error()} marshaled
            messages = append(messages, tool{call_id, out})
            publish_tool_result_event(call, out)  // so gateway persists + UI renders
        // continue loop
    }
    
    if iter == TOOL_LOOP_MAX_ITER:
        log tool_loop_max_iter_hit; publish final with whatever assembled_text is
```

**Iteration cap:** `TOOL_LOOP_MAX_ITER` default 10. Caps runaway loops without hard-blocking long task chains.

**Slot behavior per iteration:** request slot, execute, release. This re-enters the broker's queue each round, which is the right fairness posture — a long tool-executing agent shouldn't starve other agents. The preemption machinery stays valid because each `Execute` call sets up its own preemption context.

**Token streaming during tool loop:** Only the `onToken` callback publishes to `channel:tokens:{session_id}`. The model may emit both text and tool_calls in the same iteration; text goes to the UI live, tool_calls arrive atomically at the end. The last iteration (the one with no tool_calls) produces the final user-visible answer and is streamed normally.

*Alternatives:* no cap (unbounded; bad default); single-iteration + client re-request (breaks the "run to completion" contract for agentic turns).

### D6: Storing the agentic trace

Main-agent needs to tell the gateway about tool invocations + results so the gateway can persist them as `function_call` / `function_call_output` output items. Two-part mechanism:

1. **Pub/sub (live, for UI):** main-agent publishes each `tool_call` (from `onToolCall`) AND each tool *result* to `channel:tool-calls:{session_id}`. Gateway relays as SSE events (slice 1 already does this for tool_calls; this slice adds result events and extends the gateway to collect both for storage).

2. **Reply stream (canonical, for persistence):** main-agent's final `ChatResponsePayload` on the reply stream gains `ToolResults []ToolResult{CallID, Output}` alongside `ToolCalls []ToolCall`. The gateway's non-streaming path reads the reply and writes matched `function_call` + `function_call_output` items into the stored response. For streaming, the gateway collects both types from the pub/sub channels and writes them at stream close.

Extending `ChatResponsePayload` once (here) keeps the reply self-sufficient for replay and debugging — streaming isn't the only consumer of this.

*Alternatives:* store tool_calls/results in a separate Valkey structure (scope creep); skip persistence entirely and trust the session's live transcript (breaks history reconstruction on next turn).

### D7: History reconstruction with agentic traces

`GetSessionMessages` and `chainToMessages` already handle `function_call_output` in `Input` (slice 1, for client-supplied tool results — the canonical OpenAI Responses API shape). Now that we also store them in `Output` (agent-generated), both helpers need to recognize `function_call_output` when iterating output items.

Decode rule for both helpers:

```
for _, out := range resp.Output:
    switch out.Type:
        case "function_call":        assistant message w/ tool_calls
        case "function_call_output": tool message (role=tool, content=Output, tool_call_id=CallID)
        case "message":              assistant text message (existing)
```

Ordering within a response's output items is preserved, so interleaved `function_call` and `function_call_output` entries reconstruct the original agentic sequence.

*Alternatives:* fold all tool_calls into a single synthetic assistant message at the end of the turn (loses ordering; makes the model's turn-by-turn reasoning opaque on next load).

### D8: Tool invocation error handling

A tool that errors returns a JSON-encoded error body to the model as the tool result:

```json
{"error": "skill not found: nonexistent"}
```

The model can then decide to retry, give up, or explain. No special handling in the loop — errored tools produce a tool-result message like any other.

Distinct from "tool not registered":

```json
{"error": "unknown tool: foobar"}
```

Same shape; registry returns this as an error-case output. The model sees it as a normal tool result and adjusts.

Panics inside an `Invoke()` are recovered by the loop, logged at ERROR with the tool name, and surfaced as `{"error": "tool panicked"}` to the model.

*Alternatives:* surface tool errors as top-level execution failures (blocks the user's turn); silently drop errored tool_calls (model gets no feedback; loop may infinite-retry).

### D9: Dependency choice — `gopkg.in/yaml.v3`

YAML frontmatter parsing needs a library. `gopkg.in/yaml.v3` is the de-facto Go YAML library: mature, well-maintained, ~6.5k stars, used by Kubernetes, Helm, etc. Adds ~500KB to binaries. No practical alternative worth considering for Claude-Skills-format frontmatter.

## Risks / Trade-offs

- **[Risk] Model ignores skill manifest in system prompt.** If the manifest is too terse or poorly framed, the model may not discover skills. → Mitigation: manifest section uses clear framing ("Available skills, load with read_skill:"). If real-world usage shows poor discovery, iterate on wording — cheap to change.

- **[Risk] Tool loop runs away.** A model that keeps calling tools without finalizing wastes compute and slots. → Mitigation: `TOOL_LOOP_MAX_ITER` cap (default 10). Log `tool_loop_max_iter_hit` at WARN so operators see it. After cap, return whatever text is accumulated with a synthetic `max iterations reached` note appended.

- **[Risk] Tool results blow up context.** `list_skills` could return a very long list in a large deployment; `read_skill` returns full bodies which could be KB-scale. → Mitigation: for v1 we trust operators to author reasonable skills. Bodies will be chunked/summarized by future work if it becomes a problem. Hard per-result size cap not introduced — would need model coordination.

- **[Risk] Iteration cap masks a bug.** If tools return nonsense and the model loops forever, the cap returns something but might be garbage. → Mitigation: log the final state clearly (`tool_loop_max_iter_hit` with `{correlation_id, iterations, final_state}`) so operators can debug from logs.

- **[Risk] Slot churn.** Per-iteration slot acquisition means N LLM calls per turn = N slot requests. Under contention each request could queue. → Accepted: fairness trade is deliberate. If latency becomes a problem, a future change can introduce a "reservation" mechanism.

- **[Trade-off] Skill bodies loaded eagerly at startup.** Memory scales with total skill size. At typical scale (tens of KB) this is negligible. → Accepted.

- **[Trade-off] No allowed-tools enforcement.** Parsed but not acted upon. A skill body could instruct the model to use tools not listed, and nothing stops it. → Accepted: this is an information-architecture concern that adds complexity for unclear return at slice-2 maturity. Revisit if someone needs it.

- **[Trade-off] ChatResponsePayload gains ToolResults.** Wire-format extension. Backward-compatible (omitempty), but another field. → Accepted: the alternative (a separate tool-results stream) is worse.

## Migration Plan

No data migration. Rolling restart is safe:

- New gateway, old main-agent: gateway subscribes to `channel:tool-calls:{session_id}` and relays events (already does per slice 1). Old main-agent doesn't emit tool results but existing behavior is unaffected.
- New main-agent, old gateway: main-agent invokes tools and emits results; old gateway doesn't know about tool-result events on the tool-calls channel (it only relays `response.tool_call` events per slice 1). Tool results are transient — they arrive, UI doesn't render them (yet), but main-agent proceeds and delivers the final text to the gateway via the existing reply path. No regression. Persistence of tool_results into `function_call_output` output items requires the new gateway; without it, stored responses only have `function_call` items (tool_results missing from history). Not a data-loss incident for in-flight conversations; subsequent turns on an old-gateway-stored session will have incomplete history.

Deployment order recommended: gateway first, then main-agent. This avoids the interregnum where tool_results are produced but not stored.

Rollback: revert binaries. Responses stored during the new-main-agent window carry `function_call_output` items in Output; old code's `GetSessionMessages` / `chainToMessages` will skip those items (they're not `message` / `function_call`), silently dropping tool-results from history. Unusual but not corrupting.

## Open Questions

1. **Should the skill manifest be cached per-turn in main-agent, or regenerated on every turn?** Registry is immutable post-startup; caching is simple. **Lean:** regenerate. It's cheap and keeps the code obvious.

2. **Should tool-result events flow on the same `channel:tool-calls:` channel as tool_call events, or a new `channel:tool-results:` channel?** Same channel is simpler, discriminated by message type. **Lean:** same channel, new payload wrapper.

3. **How should the dashboard's session view render tool-result items from stored history?** Slice 1 has a renderer for `role: "tool"` messages from the store. Should Just Work. **Lean:** test the path; no new UI needed.

4. **`allowed-tools` frontmatter: parse and keep in the Manifest, or ignore entirely?** Parse-and-keep costs nothing and signals intent. **Lean:** parse, store on Manifest, surface in diagnostics but don't enforce.
