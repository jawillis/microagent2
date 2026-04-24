## Why

Slice 1 landed the tool-calling protocol but shipped no tools and no execution loop — the agent receives `tool_calls` from the model and ignores them. This slice adds the **execution** half: a filesystem-backed Claude-Skills store, two built-in tools (`list_skills`, `read_skill`) that expose it to the model, and a tool-execution loop in main-agent that can complete multi-step agentic turns within a single user request. After this slice lands, a user can drop a `SKILL.md` into `./skills/my-skill/` and the model will discover and load it during a conversation.

## What Changes

- Introduce a filesystem-backed skills store. Each skill is a subdirectory under `SKILLS_DIR` (default `./skills/`, configurable via env) containing a `SKILL.md` file with YAML frontmatter (`name`, `description`, optional `allowed-tools`, optional `model`) and a Markdown body.
- Scan the skills directory at main-agent startup and cache parsed manifests in memory; restart is required to pick up new skills (hot-reload deferred).
- Define a `Tool` abstraction: `Name()`, `Schema() ToolSchema`, `Invoke(ctx, argsJSON) (string, error)`. Define a `Registry` that holds tools by name, exposes `Schemas()` for inclusion in LLM requests, and resolves `Invoke(name, args)` calls.
- Ship two built-in tools implementing this interface:
  - `list_skills`: returns `[{name, description}, ...]` — no arguments.
  - `read_skill`: given `{name}`, returns the full `SKILL.md` Markdown body; error if not found.
- Add a tool-execution loop to main-agent. Per user turn: request slot → `Execute` (with tool schemas) → release slot. If `tool_calls` returned, invoke each via registry, append assistant(`tool_calls`) and tool-result messages to the turn's history, and iterate. Bounded by `TOOL_LOOP_MAX_ITER` (default 10).
- Slot hold pattern: hold slot only during each `Execute` call, release between iterations while tools execute. This keeps slot utilization fair when tools run long.
- Inject a skill manifest into the system prompt at the main-agent layer (NOT the context-manager layer): a short bulleted list of `{name}: {description}` so the model knows what skills exist before needing to call `list_skills`. Context-manager's system-prompt byte-stability requirement remains intact — the mutation happens downstream.
- Persist tool_calls and tool results in the response store so next-turn history reconstruction correctly reassembles the agentic trace. Tool results stored as `function_call_output` output items; `GetSessionMessages` / `chainToMessages` extended to decode `function_call_output` from the output side.
- Structured logging for every tool invocation: `{correlation_id, tool_name, args_bytes, elapsed_ms, outcome, result_bytes}`.

Explicitly **out of scope**:
- `shell` built-in (deferred indefinitely per decision; will be an MCP server if ever).
- MCP client or any external tool protocol — slice 3.
- Skills management UI in the dashboard — operators drop files; no in-browser CRUD.
- Skills hot-reload — restart the main-agent to pick up changes.
- Retro-agent using tools — out of scope.
- `allowed-tools` enforcement when executing a loaded skill — parsed from frontmatter but not enforced in this slice (reserved for a future allowlist concern).

## Capabilities

### New Capabilities
- `skills-store`: filesystem-backed catalog of Claude-Skills-format skill directories; scans `SKILLS_DIR`, parses `SKILL.md` frontmatter + body, exposes a typed `Manifest` + `Body(name)` lookup.
- `tool-invocation`: the `Tool` / `Registry` abstraction, the `list_skills` and `read_skill` built-in tools, and the main-agent tool-execution loop that ties them together.

### Modified Capabilities
- `agent-runtime`: main-agent grows a tool-execution loop; slot acquisition moves inside the loop (per-iteration hold/release) rather than per-turn.
- `context-management`: no change to the context-manager itself — its system-prompt byte-stability requirement is **preserved**. The skill manifest injection happens in main-agent, downstream of the context-manager.
- `llm-broker`: no structural change — broker still proxies one request at a time. Only logging may note the higher request volume per turn.
- `response-chain`: output items of a response now routinely include `function_call` and `function_call_output` entries alongside the final `message` assistant turn; `GetSessionMessages` / `chainToMessages` extended to decode `function_call_output` from output items.

## Impact

**Code**
- `internal/skills/` (new package): `manifest.go` (frontmatter/body parser), `store.go` (directory scanner + in-memory cache), `store_test.go`.
- `internal/tools/` (new package): `tool.go` (interface + registry), `builtin_skills.go` (list_skills + read_skill), `registry_test.go`, `builtin_skills_test.go`.
- `internal/agent/runtime.go` — no signature change; the loop lives in main-agent, not runtime.
- `cmd/main-agent/main.go` — instantiate skills store + tool registry + manifest injector; rewrite `handleRequest` around a bounded tool loop with per-iteration slot acquisition.
- `internal/response/store.go` — `GetSessionMessages` extended to decode `function_call_output` from output items (in addition to input items, which slice 1 already handled).
- `internal/gateway/responses.go` — `chainToMessages` extended identically. `handleResponsesNonStreaming` and `handleResponsesStreaming` extended to persist tool_results that arrive on the `tool-calls` channel or the non-streaming reply (mechanism depends on how main-agent surfaces them).
- `internal/messaging/payloads.go` — `ChatResponsePayload` gains `ToolResults []ToolResult` (each `{CallID, Output}`) so the non-streaming reply can carry results back to the gateway for persistence. Alternatively, main-agent publishes tool results to a new channel; first option is simpler.
- `cmd/main-agent/main.go` — publishes tool invocations + results to `channel:tool-calls:{session_id}` and to the response reply for storage.

**Wire format**
- `ChatResponsePayload` gains optional `ToolResults []ToolResult` (omitempty).
- Stored responses may carry `function_call_output` items in their `Output` array (semantics were already accommodated; we're now using the path actively).

**Environment**
- New env: `SKILLS_DIR` (default `./skills/`), `TOOL_LOOP_MAX_ITER` (default 10).
- Docker compose: mount `./skills/` into the main-agent container.

**Dependencies**
- `gopkg.in/yaml.v3` for frontmatter parsing. Proven library, stdlib-like API, stable.

**Regression surface**
- Turns with no `tool_calls` continue to behave identically — the loop exits after one iteration, same message flow as today.
- Broker and context-manager behavior byte-identical to slice-1 baseline.

**Does not touch**
- Slot allocation / broker slot table.
- Context-manager code.
- Retro-agent.
- Gateway streaming text path (already handles `response.tool_call` events from slice 1).
