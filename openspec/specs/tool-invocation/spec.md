## ADDED Requirements

### Requirement: Tool interface
A tool SHALL implement a `Tool` interface exposing a `Name()` method returning the tool's stable identifier, a `Schema()` method returning a `messaging.ToolSchema` wire-compatible with the OpenAI chat-completions tools field, and an `Invoke(ctx, argsJSON)` method returning a string result or an error. Tool implementations SHALL be registered with a `Registry` at main-agent startup.

#### Scenario: Name uniqueness
- **WHEN** `Registry.Register(t)` is invoked with a tool whose `Name()` matches an already-registered tool
- **THEN** `Register` SHALL return a non-nil error and SHALL NOT add the duplicate

#### Scenario: Schema is wire-ready
- **WHEN** `Registry.Schemas()` is invoked
- **THEN** it SHALL return a slice of `ToolSchema` that can be passed directly as `LLMRequestPayload.Tools` without further transformation

### Requirement: Registry deterministic Schemas order
`Registry.Schemas()` SHALL return tool schemas in a deterministic order (insertion order) so that LLM request bodies are stable across identical turns.

#### Scenario: Order matches registration
- **WHEN** tools `list_skills` and `read_skill` are registered in that order
- **THEN** `Schemas()` SHALL return them in the same order on every call

### Requirement: Registry invoke resolution
`Registry.Invoke(ctx, name, argsJSON)` SHALL look up the tool by name and call its `Invoke`. When the tool is not registered, the registry SHALL return a result string `{"error": "unknown tool: <name>"}` (JSON-encoded) and a nil error, so the agent's tool loop can feed the result back to the model without treating it as a fatal failure.

#### Scenario: Unknown tool returns structured error
- **WHEN** `Invoke(ctx, "nonexistent", "{}")` is called on a registry with no matching tool
- **THEN** the result string SHALL be `{"error":"unknown tool: nonexistent"}` and the error return SHALL be nil

#### Scenario: Tool errors surface as structured errors
- **WHEN** a registered tool's `Invoke` returns a non-nil error
- **THEN** the registry SHALL return a result string containing a JSON object `{"error": "<err.Error()>"}` and a nil error, preserving the loop invariant that tool outputs are always strings

#### Scenario: Tool panic recovery
- **WHEN** a registered tool's `Invoke` panics
- **THEN** the registry SHALL recover, log at ERROR with `msg: "tool_panic"` and fields `{tool_name, panic}`, and return `{"error":"tool panicked"}` as the result

### Requirement: Built-in list_skills tool
A built-in tool named `list_skills` SHALL be registered at main-agent startup. Its schema SHALL describe a function that takes no arguments and returns the catalog. Its `Invoke` SHALL return a JSON-encoded array of `{name, description}` objects derived from `skills.Store.List()` in the store's deterministic order.

#### Scenario: Empty catalog
- **WHEN** `list_skills` is invoked against an empty skills store
- **THEN** the result SHALL be `[]` (an empty JSON array)

#### Scenario: Populated catalog
- **WHEN** the store contains skills `a` (desc `A`) and `b` (desc `B`)
- **THEN** `list_skills` SHALL return `[{"name":"a","description":"A"},{"name":"b","description":"B"}]`

#### Scenario: Extra arguments tolerated
- **WHEN** `list_skills` is invoked with a non-empty arguments object
- **THEN** it SHALL ignore the arguments and return the full catalog

### Requirement: Built-in read_skill tool
A built-in tool named `read_skill` SHALL be registered at main-agent startup. Its schema SHALL describe a function that takes a required `name` string parameter. Its `Invoke` SHALL return the skill's Markdown body when found, or a JSON error object when not found or when the arguments are invalid.

#### Scenario: Hit returns body
- **WHEN** `read_skill` is invoked with arguments `{"name": "estimate-tokens"}` for an existing skill
- **THEN** the result SHALL be the exact Markdown body bytes stored by the skills store

#### Scenario: Miss returns structured error
- **WHEN** `read_skill` is invoked with `{"name": "nonexistent"}`
- **THEN** the result SHALL be `{"error":"skill not found: nonexistent"}`

#### Scenario: Missing name argument
- **WHEN** `read_skill` is invoked with `{}` or `{"name": ""}`
- **THEN** the result SHALL be `{"error":"name argument is required"}`

#### Scenario: Malformed JSON arguments
- **WHEN** `read_skill` is invoked with `argsJSON` that does not parse as a JSON object with a string `name` field
- **THEN** the result SHALL be `{"error":"invalid arguments: <detail>"}` with `<detail>` describing the parse failure

### Requirement: Skill manifest injection
Main-agent SHALL, before publishing each LLM request during a user turn, append a skill manifest section to the first `system`-role message's content when the tool registry holds at least one skill-facing tool. The manifest section SHALL be demarcated by XML-style tags and list each skill as a bullet of `- <name>: <description>`.

#### Scenario: Manifest appended when skills exist
- **WHEN** main-agent prepares the `messages` array for a turn and the skills store has at least one skill (`a: A`, `b: B`)
- **THEN** the `system`-role message's `Content` SHALL be suffixed with `"\n\n<available_skills>\n- a: A\n- b: B\n</available_skills>"` (single blank line separator, trailing newline inside the closing tag)

#### Scenario: No manifest when catalog empty
- **WHEN** the skills store is empty
- **THEN** no `<available_skills>` block SHALL be appended, and the `system`-role message's `Content` SHALL be byte-identical to what the context-manager produced

#### Scenario: Injection is downstream of context-manager
- **WHEN** the context-manager produces a `ContextAssembledPayload` with a byte-stable system prompt
- **THEN** main-agent SHALL inject the manifest on its own copy of the messages before calling the broker; the context-manager's output SHALL remain unchanged

#### Scenario: Regenerated per turn
- **WHEN** main-agent handles two consecutive turns for the same session while the skills store's contents have not changed
- **THEN** each turn SHALL produce the same injected manifest content (the registry is immutable post-startup; no caching is required to achieve this)

### Requirement: Bounded tool-execution loop
Main-agent SHALL run a bounded tool-execution loop per user turn. Each iteration SHALL (a) acquire a slot, (b) invoke the runtime's `Execute` with the current `messages` and `Registry.Schemas()`, (c) release the slot, (d) if `Execute` returned tool_calls, invoke each through the registry and append the assistant-with-tool_calls and corresponding tool-result messages to `messages`, then continue. The loop SHALL terminate when `Execute` returns zero tool_calls, returns `ErrPreempted`, returns any other error, or the iteration cap is reached.

#### Scenario: Single-iteration pure-text turn
- **WHEN** a user turn produces zero tool_calls from the model
- **THEN** the loop SHALL run exactly one iteration, publish the final `ChatResponsePayload` with the assistant text, and exit

#### Scenario: Two-iteration agentic turn
- **WHEN** the model emits one tool_call in iteration 1 and zero tool_calls in iteration 2
- **THEN** the loop SHALL invoke the tool between iterations, append the assistant tool_calls message and the tool-result message to `messages`, and terminate after iteration 2 with the iteration-2 assistant text

#### Scenario: Iteration cap reached
- **WHEN** the loop has run `TOOL_LOOP_MAX_ITER` iterations (default 10) and the most recent iteration still returned non-empty tool_calls
- **THEN** main-agent SHALL log at WARN with `msg: "tool_loop_max_iter_hit"` and fields `{correlation_id, iterations}`, SHALL publish a final `ChatResponsePayload` containing whatever assistant text was last produced appended with a newline and `"(max iterations reached)"`, and SHALL NOT invoke further tools

#### Scenario: Preemption mid-loop
- **WHEN** `Execute` returns `ErrPreempted` during an iteration
- **THEN** the loop SHALL exit; main-agent SHALL NOT acquire another slot or invoke further tools for this turn

#### Scenario: Configurable iteration cap
- **WHEN** main-agent starts
- **THEN** it SHALL read `TOOL_LOOP_MAX_ITER` from environment and use that value as the iteration cap, defaulting to 10 when unset

### Requirement: Slot acquisition per iteration
Main-agent SHALL acquire a slot only for the duration of each `Execute` call within the tool loop, releasing it before invoking tools. Tool invocations SHALL NOT hold a broker slot. This ensures slot fairness for concurrent agents when a tool takes non-trivial time.

#### Scenario: Slot held during Execute
- **WHEN** main-agent enters iteration N of the loop
- **THEN** it SHALL call `RequestSlot`, then `Execute`, then `ReleaseSlot`, in that order, before invoking any tools

#### Scenario: No slot held during tool invocation
- **WHEN** main-agent invokes a registered tool between iterations
- **THEN** the broker slot previously held for the prior `Execute` SHALL have been released before `Registry.Invoke` is called

### Requirement: Tool invocation logging
Main-agent SHALL emit a structured INFO log line for every tool invocation attempt, including gated calls that did not reach the registry.

#### Scenario: Invocation logged
- **WHEN** main-agent invokes a tool via `Registry.Invoke`
- **THEN** it SHALL log at INFO with `msg: "tool_invoked"` and fields `{correlation_id, tool_name, args_bytes, elapsed_ms, outcome, result_bytes, active_skill}` where `outcome` is one of `ok`, `error` (including unknown-tool and argument-parse errors), `panic`, or `gated`

#### Scenario: Gated calls logged
- **WHEN** main-agent gates a tool call without invoking the registry
- **THEN** it SHALL log at INFO with `msg: "tool_invoked"`, `outcome="gated"`, `tool_name=<name>`, `active_skill=<active>`, `elapsed_ms=0`, and `result_bytes` equal to the length of the emitted error envelope

### Requirement: Tool-call and tool-result pub/sub events
Main-agent SHALL publish each tool_call and each tool result to `channel:tool-calls:{session_id}` so the gateway can relay live SSE events and collect results for persistence. Tool-result events SHALL use a distinct message type that allows consumers to distinguish calls from results.

#### Scenario: Tool call event published
- **WHEN** the runtime's `onToolCall` callback is invoked with an assembled `ToolCall`
- **THEN** main-agent SHALL publish a `TypeToolCall` message with `ToolCallPayload{SessionID, Call}` to `channel:tool-calls:{session_id}`

#### Scenario: Tool result event published
- **WHEN** main-agent has invoked a tool via the registry and obtained its result string
- **THEN** main-agent SHALL publish a `TypeToolResult` message with `ToolResultPayload{SessionID, CallID, Output}` to `channel:tool-calls:{session_id}`

## ADDED Requirements

### Requirement: Registry accepts MCP-sourced tools
The `tools.Registry.Register` method SHALL accept MCP-sourced tool implementations using the same interface as built-in tools. The namespacing rule (names starting with `mcp__`) distinguishes MCP tools from built-ins, and the rule SHALL be enforced by rejecting built-in registrations whose name starts with `mcp__`.

#### Scenario: MCP tool registered
- **WHEN** an MCP-sourced tool with `Name() == "mcp__foo__bar"` is registered via `Registry.Register`
- **THEN** the registration SHALL succeed and the tool SHALL be invocable via `Registry.Invoke(ctx, "mcp__foo__bar", args)`

#### Scenario: Built-in with mcp__ prefix rejected
- **WHEN** a non-MCP tool (not sourced via the MCP manager) is registered with a name starting with `mcp__`
- **THEN** `Registry.Register` SHALL return a non-nil error, preserving the invariant that only MCP-managed tools occupy the `mcp__` namespace

### Requirement: MCP tool registration is additive to built-ins
Main-agent SHALL register built-in tools (`list_skills`, `read_skill`, `read_skill_file`, `current_time`, `run_skill_script`, `bash`) before MCP-sourced tools, so that `Registry.Schemas()` order places built-ins first. This preserves the invariant where the LLM's `tools` array always begins with the built-ins.

#### Scenario: Registration order
- **WHEN** main-agent initializes with built-ins and one or more MCP servers
- **THEN** the order of tools in `Registry.Schemas()` SHALL be: `list_skills`, `read_skill`, `read_skill_file`, `current_time`, `run_skill_script`, `bash`, then MCP tools in the order they were registered

#### Scenario: No MCP servers configured
- **WHEN** `config:mcp:servers` is empty or missing
- **THEN** main-agent SHALL still register all six built-ins and `Registry.Schemas()` SHALL return exactly those built-ins in order: `list_skills`, `read_skill`, `read_skill_file`, `current_time`, `run_skill_script`, `bash`

#### Scenario: Built-in registered before MCP
- **WHEN** main-agent starts with one MCP server that exposes tool `foo` and registers its built-ins first
- **THEN** `Registry.Schemas()` SHALL return `list_skills`, `read_skill`, `read_skill_file`, `current_time`, `run_skill_script`, `bash`, `mcp__<server>__foo` in that exact order

### Requirement: Built-in read_skill_file tool
A built-in tool named `read_skill_file` SHALL be registered at main-agent startup. Its schema SHALL describe a function that takes two required string parameters:
- `skill`: the exact skill name as returned by `list_skills`
- `path`: a relative path within the skill directory (e.g. `reference/best_practices.md`)

Its `Invoke` SHALL call `skills.Store.ReadFile(skill, path)` and translate the three-valued result into the existing tool-result string convention:

| Store result | Tool result |
|---|---|
| `(contents, true, nil)` | `contents` (the raw bytes as-is) |
| `("", false, nil)` | `{"error":"skill not found: <skill>"}` |
| `("", true, err)` | `{"error":"<err.Error()>"}` |

The tool SHALL also return structured errors for argument-parsing failures, consistent with the existing `read_skill` tool.

#### Scenario: Successful read returns file contents
- **WHEN** `read_skill_file` is invoked with `{"skill":"code-review","path":"language-notes.md"}` and the file exists and is within the cap
- **THEN** the result SHALL be the exact file contents, byte-for-byte

#### Scenario: Unknown skill returns structured error
- **WHEN** `read_skill_file` is invoked with `{"skill":"nonexistent","path":"x.md"}`
- **THEN** the result SHALL be `{"error":"skill not found: nonexistent"}`

#### Scenario: Path rejection surfaces store error message
- **WHEN** `read_skill_file` is invoked with `{"skill":"code-review","path":"../../etc/passwd"}`
- **THEN** the result SHALL be a JSON error object whose `error` field contains the store's rejection message (e.g. mentioning "outside the skill root" or "escapes")
- **AND** NO filesystem access SHALL occur outside the skill root

#### Scenario: Missing required argument
- **WHEN** `read_skill_file` is invoked with `{}` or with only one of the two required fields set to a non-empty string
- **THEN** the result SHALL be `{"error":"skill and path arguments are required"}` (or equivalent messaging naming the missing field)

#### Scenario: Reserved path SKILL.md redirected
- **WHEN** `read_skill_file` is invoked with `{"skill":"code-review","path":"SKILL.md"}`
- **THEN** the result SHALL be a JSON error object whose `error` field directs the caller to use `read_skill`
- **AND** the file SHALL NOT be served by this tool even though it exists

#### Scenario: Oversize file returns error
- **WHEN** `read_skill_file` is invoked with a valid path pointing to a file larger than `SKILL_FILE_MAX_BYTES`
- **THEN** the result SHALL be a JSON error object reporting the size and the cap

#### Scenario: Malformed JSON arguments
- **WHEN** `read_skill_file` is invoked with an `argsJSON` string that does not parse as a JSON object
- **THEN** the result SHALL be `{"error":"invalid arguments: <detail>"}`

### Requirement: Base toolset
Main-agent SHALL maintain a "base toolset" — the set of tool names that are always visible to the model regardless of which skill (if any) is active. The base toolset SHALL consist of every built-in tool registered at startup (`list_skills`, `read_skill`, `read_skill_file`, `current_time`, `run_skill_script`, `bash`) plus every MCP-sourced tool registered via the MCP manager (names beginning `mcp__<server>__`).

#### Scenario: Built-ins in base
- **WHEN** main-agent finishes initialization with the default built-in registrations
- **THEN** the base toolset SHALL include exactly `list_skills`, `read_skill`, `read_skill_file`, `current_time`, `run_skill_script`, and `bash`

#### Scenario: MCP tools in base
- **WHEN** main-agent finishes initialization with one or more MCP servers that register tools
- **THEN** the base toolset SHALL additionally include every registered `mcp__<server>__<tool>` name

#### Scenario: Base is a stable set after startup
- **WHEN** the tool loop runs multiple turns for multiple sessions
- **THEN** the base toolset SHALL be identical across turns (no runtime registration of new tools)

### Requirement: Built-in current_time tool
A built-in tool named `current_time` SHALL be registered at main-agent startup, after `read_skill_file` and before any MCP-sourced tools. Its schema SHALL describe a function taking one optional string parameter `format` (a Go time-layout string). Its `Invoke` SHALL return the current time in UTC formatted with the provided layout, or with `time.RFC3339` when the argument is empty or absent.

#### Scenario: Default format
- **WHEN** `current_time` is invoked with `{}` or `{"format":""}`
- **THEN** the result SHALL be the current UTC time formatted as RFC3339 (e.g. `2026-04-23T15:04:05Z`)

#### Scenario: Custom format
- **WHEN** `current_time` is invoked with `{"format":"2006-01-02"}`
- **THEN** the result SHALL be the current UTC date formatted as `YYYY-MM-DD`

#### Scenario: UTC, not local
- **WHEN** `current_time` is invoked on a host with non-UTC local time
- **THEN** the returned timestamp SHALL reflect UTC, not the host's local time zone

#### Scenario: Malformed JSON arguments
- **WHEN** `current_time` is invoked with `argsJSON` that does not parse as a JSON object
- **THEN** the result SHALL be `{"error":"invalid arguments: <detail>"}`

### Requirement: Schema filtering for the active skill
The tool registry SHALL expose a `SchemasFor(base []string, allowed []string) []ToolSchema` method that returns the subset of registered schemas whose tool name is in `base ∪ allowed`. The method SHALL preserve registration order of the tools in the returned slice. Names in `base` or `allowed` that do not correspond to any registered tool SHALL be silently ignored.

Main-agent SHALL call `SchemasFor(baseToolset, activeSkill.AllowedTools)` before each LLM request during a turn, passing the active skill's allowed-tools list (or an empty slice when no skill is active). Passing both an empty `base` and an empty `allowed` SHALL return an empty schema slice.

#### Scenario: No active skill returns base schemas
- **WHEN** main-agent prepares a turn with no active skill for the session
- **THEN** the schemas passed to the broker SHALL be exactly the base toolset's schemas in registration order

#### Scenario: Active skill expands visible set
- **WHEN** the active skill's `allowed-tools` is `["extra_tool"]` and `extra_tool` is a registered tool not in the base
- **THEN** the schemas SHALL include the base toolset plus `extra_tool` in registration order

#### Scenario: Active skill with empty allowed-tools equals base
- **WHEN** the active skill's `allowed-tools` is empty or absent
- **THEN** the schemas SHALL equal the base toolset exactly

#### Scenario: Unknown allowed-tools ignored in filter
- **WHEN** the active skill's `allowed-tools` includes `["non_existent_tool"]`
- **THEN** the schemas SHALL equal the base toolset (the unknown entry is ignored) and SHALL NOT include any tool not actually registered

#### Scenario: Order preserved under filtering
- **WHEN** tools are registered in order `A, B, C, D, E` and the active skill allows `["D", "B"]` while the base is `["A"]`
- **THEN** `SchemasFor(["A"], ["D","B"])` SHALL return schemas in order `A, B, D` (registration order, not argument order)

#### Scenario: Schemas() continues to return full registry
- **WHEN** a caller (test, smoke tool) invokes the registry's existing `Schemas()` method
- **THEN** the method SHALL return every registered schema in registration order, unchanged from the pre-change behaviour

### Requirement: Active-skill session state
Main-agent SHALL track at most one active skill per session. State SHALL be stored in Valkey under the key `session:<session_id>:active-skill` as a string whose value is the active skill's name. An empty value or a missing key SHALL indicate "no active skill." The key's TTL SHALL be 24 hours and SHALL be refreshed on every write.

Main-agent SHALL read this key at the start of each turn (before `SchemasFor`) to determine the active skill for that turn.

#### Scenario: Fresh session has no active skill
- **WHEN** a turn begins for a session that has never loaded a skill
- **THEN** `session:<id>:active-skill` SHALL not exist and the turn SHALL proceed with the base toolset only

#### Scenario: Active skill persists across turns
- **WHEN** turn 1 successfully activates skill `foo` and turn 2 begins in the same session
- **THEN** turn 2 SHALL read `foo` from Valkey and include `foo.AllowedTools` in its `SchemasFor` call

#### Scenario: TTL refresh
- **WHEN** a session's active skill is written or overwritten
- **THEN** the `session:<id>:active-skill` key SHALL have a TTL of 24 hours measured from the write

#### Scenario: Missing skill clears state
- **WHEN** a turn begins and the stored active-skill name no longer exists in the skills store (e.g. skill removed from disk and main-agent restarted)
- **THEN** main-agent SHALL treat the session as having no active skill, SHALL clear `session:<id>:active-skill`, and SHALL proceed with the base toolset

### Requirement: Activation via successful read_skill
When the tool loop invokes `read_skill` and the registered tool returns a skill body (not an error envelope), main-agent SHALL update the session's active skill to the name that was loaded. The update SHALL occur after the tool result is appended to the message stream and before the next iteration begins so that the new allowlist takes effect immediately.

Activation SHALL NOT occur when `read_skill` returns an error envelope (unknown skill, missing argument, malformed JSON).

#### Scenario: Successful read activates
- **WHEN** iteration N invokes `read_skill` with `{"name":"foo"}`, `foo` exists in the skills store, and the tool returns the skill's body
- **THEN** main-agent SHALL write `foo` to `session:<id>:active-skill` and SHALL use `foo.AllowedTools` when building schemas for iteration N+1

#### Scenario: Failed read does not activate
- **WHEN** iteration N invokes `read_skill` with `{"name":"nonexistent"}` and the tool returns `{"error":"skill not found: nonexistent"}`
- **THEN** main-agent SHALL NOT modify `session:<id>:active-skill`

#### Scenario: Malformed read_skill args do not activate
- **WHEN** iteration N invokes `read_skill` with malformed arguments and the tool returns `{"error":"invalid arguments: ..."}` or `{"error":"name argument is required"}`
- **THEN** main-agent SHALL NOT modify `session:<id>:active-skill`

#### Scenario: Replacement
- **WHEN** the session has active skill `foo` and iteration N successfully activates `bar`
- **THEN** `session:<id>:active-skill` SHALL be overwritten with `bar` and subsequent iterations SHALL see `bar.AllowedTools`

#### Scenario: Activation applies mid-turn
- **WHEN** iteration 1 activates skill `foo` and iteration 2 makes tool calls
- **THEN** iteration 2 SHALL use `foo`'s `allowed-tools` for schema filtering AND for invoke-time gating, without waiting for a new turn

### Requirement: Invoke-time gating
Main-agent SHALL verify every tool call against the current turn's visible set (`base ∪ active_skill.allowed-tools`) before invoking the registry. When a tool call names a tool not in the visible set, main-agent SHALL skip the registry invocation and SHALL append a structured error tool-result instead: `{"error":"tool not available under active skill: <name>"}`.

The emitted `tool_invoked` log line SHALL record `outcome=gated` for such calls, alongside `tool_name` and `active_skill` fields.

#### Scenario: Visible tool executes normally
- **WHEN** the model calls a tool whose name is in the base set or the active skill's `allowed-tools`
- **THEN** main-agent SHALL invoke the registry normally and emit `tool_invoked outcome=ok|error|panic` per existing behaviour

#### Scenario: Hidden tool is gated
- **WHEN** the model calls a tool whose name is neither in the base set nor in the active skill's `allowed-tools`
- **THEN** main-agent SHALL NOT invoke the registry, SHALL append `{"error":"tool not available under active skill: <name>"}` as the tool result, and SHALL emit `tool_invoked outcome=gated tool_name=<name> active_skill=<active>`

#### Scenario: Gated call still advances the loop
- **WHEN** iteration N produces one tool call that is gated
- **THEN** the tool-result message SHALL be appended to the messages slice and iteration N+1 SHALL proceed normally; the gate does not abort the turn

#### Scenario: Unknown tool with no active skill
- **WHEN** no skill is active and the model calls a tool not in the base set
- **THEN** the call SHALL be gated per the same rule (unknown tools are never silently executed)

### Requirement: Logging for active-skill changes and gated calls
Main-agent SHALL emit a structured INFO log line when the session's active skill changes, with fields `{correlation_id, session_id, active_skill, previous_skill}`. Main-agent SHALL emit a structured WARN log line for each unknown entry in an active skill's `allowed-tools` when that skill is read for a turn, with fields `{active_skill, unknown_tool}`.

#### Scenario: Active-skill change logged
- **WHEN** a session's active skill flips from `` to `foo` (or from `foo` to `bar`)
- **THEN** main-agent SHALL emit `INFO msg: "active_skill_changed"` with `active_skill=<new>` and `previous_skill=<prev or empty>`

#### Scenario: Unknown allowed-tool logged once per turn
- **WHEN** a turn begins with an active skill whose `allowed-tools` contains a name that is not registered
- **THEN** main-agent SHALL emit `WARN msg: "skill_allowed_tool_unknown"` with `active_skill` and `unknown_tool` fields, at most once per unknown tool per turn (not once per iteration)

### Requirement: Built-in run_skill_script tool
A built-in tool named `run_skill_script` SHALL be registered at main-agent startup, after `current_time` and before any MCP-sourced tools. Its schema SHALL describe a function that takes two required string parameters and three optional parameters:

- `skill` (string, required) — exact skill name as returned by `list_skills`
- `script` (string, required) — relative path within the skill directory, e.g. `scripts/hello.py`
- `args` (array of strings, optional) — argv passed to the script; default empty
- `stdin` (string, optional) — piped to the script's stdin; default empty
- `timeout_s` (integer, optional) — per-invocation deadline on the exec side; clamped by exec's `EXEC_MAX_TIMEOUT_S`

Its `Invoke` SHALL call the exec service via HTTP `POST /v1/run`, injecting the current turn's `session_id` into the request regardless of what the model provided, and SHALL return the exec response envelope as a JSON string. On HTTP client failures (connection refused, deadline, non-200 response), `Invoke` SHALL return a JSON error envelope consistent with the other built-ins: `{"error":"<description>"}`.

The Go return value for `Invoke` SHALL be `(envelope string, nil error)` in every case — non-nil Go errors break the registry's "tool output is always a string" invariant.

#### Scenario: Successful run returns exec envelope verbatim
- **WHEN** `run_skill_script` is invoked with `{"skill":"demo","script":"scripts/hello.py"}` and exec responds with a successful 200 envelope
- **THEN** the tool result SHALL be the exec JSON envelope byte-for-byte, including `exit_code`, `stdout`, `stdout_truncated`, `stderr`, `stderr_truncated`, `workspace_dir`, `outputs`, `duration_ms`, `timed_out`, `install_duration_ms`

#### Scenario: Session ID injected from turn payload
- **WHEN** `run_skill_script` is invoked during a turn whose `session_id` is `sess-abc`, regardless of whether the model supplies a `session_id` in args
- **THEN** main-agent SHALL override or inject `session_id: "sess-abc"` in the request to exec so exec's workspace scoping uses the turn's true session

#### Scenario: Exec unreachable returns structured error
- **WHEN** `run_skill_script` is invoked and the HTTP call fails with connection refused
- **THEN** the tool result SHALL be a JSON error envelope whose `error` field includes the string `"exec unavailable"`; no Go-level error SHALL be returned to the registry

#### Scenario: Client timeout returns structured error
- **WHEN** `run_skill_script` is invoked and the HTTP client deadline fires before exec responds
- **THEN** the tool result SHALL be a JSON error envelope whose `error` field includes the string `"deadline exceeded"` or `"timeout"`

#### Scenario: Non-200 response surfaces status
- **WHEN** exec returns a non-200 HTTP response (e.g. 400 unknown skill, 409 workspace full, 500 internal error)
- **THEN** the tool result SHALL be a JSON error envelope whose `error` field includes the HTTP status and exec's body message

#### Scenario: Missing required argument
- **WHEN** `run_skill_script` is invoked with `{}` or with either `skill` or `script` absent or empty
- **THEN** the tool result SHALL be `{"error":"skill and script arguments are required"}` without making any HTTP call

#### Scenario: Malformed JSON arguments
- **WHEN** `run_skill_script` is invoked with an `argsJSON` string that does not parse as a JSON object
- **THEN** the tool result SHALL be `{"error":"invalid arguments: <detail>"}` without making any HTTP call

#### Scenario: Client honors larger timeout than exec
- **WHEN** `EXEC_MAX_TIMEOUT_S` is set to 120 on both services
- **THEN** main-agent's HTTP client SHALL use a per-request timeout of at least 130 seconds (exec max + 10s buffer) so exec's timed-out responses arrive before the client disconnects

### Requirement: Built-in bash tool
A built-in tool named `bash` SHALL be registered at main-agent startup, after `run_skill_script` and before any MCP-sourced tools. Its schema SHALL describe a function that takes one required string parameter and one optional parameter:

- `command` (string, required) — shell command to execute via `sh -c`
- `timeout_s` (integer, optional) — per-command deadline; clamped server-side to `EXEC_MAX_TIMEOUT_S`

The tool's description SHALL communicate that (a) files persist across calls within the same session until 60 minutes of inactivity, (b) `/skills` is not accessible via bash (agents use `read_skill_file` instead), and (c) network access follows operator policy.

Its `Invoke` SHALL call the exec service's `POST /v1/bash` endpoint, injecting the current turn's `session_id` into the request regardless of what the model provided, and SHALL return the exec response envelope as a JSON string. On HTTP client failures the tool SHALL return a JSON error envelope with the same classification scheme as `run_skill_script` (exec unavailable / deadline exceeded / exec returned N).

#### Scenario: Successful bash returns envelope verbatim
- **WHEN** `bash` is invoked with `{"command":"echo hi"}` and exec responds 200
- **THEN** the tool result SHALL be the exec JSON envelope byte-for-byte, including `exit_code`, `stdout`, `stdout_truncated`, `stderr`, `stderr_truncated`, `sandbox_dir`, `duration_ms`, `timed_out`

#### Scenario: Session ID injected from turn payload
- **WHEN** `bash` is invoked during a turn whose `session_id` is `sess-abc`, regardless of whether the model supplies a `session_id` in args
- **THEN** main-agent SHALL override or inject `session_id: "sess-abc"` in the request to exec

#### Scenario: Missing command returns structured error
- **WHEN** `bash` is invoked with `{}` or `{"command":""}`
- **THEN** the tool result SHALL be `{"error":"command argument is required"}` without making any HTTP call

#### Scenario: Malformed JSON arguments
- **WHEN** `bash` is invoked with an `argsJSON` string that does not parse as a JSON object
- **THEN** the tool result SHALL be `{"error":"invalid arguments: <detail>"}`

#### Scenario: Exec unreachable returns structured error
- **WHEN** `bash` is invoked and the HTTP call fails with connection refused
- **THEN** the tool result SHALL be a JSON error envelope whose `error` field includes `"exec unavailable"`

#### Scenario: Non-200 surfaces status
- **WHEN** exec returns a non-200 response (e.g. 400 for network-denied, 409 sandbox_full)
- **THEN** the tool result SHALL include the HTTP status and exec's body message in the error envelope

### Requirement: Session-scoped tool whitelist
Main-agent SHALL maintain a whitelist of built-in tool names for which `session_id` is injected from the turn payload (overriding any model-supplied value). The whitelist SHALL include `run_skill_script` and `bash`. Tools not in the whitelist SHALL receive the model's args unchanged.

#### Scenario: Whitelisted tool gets injection
- **WHEN** the model calls `bash` or `run_skill_script`
- **THEN** main-agent SHALL inject the turn's `session_id` into the request before invoking the tool, replacing any model-supplied value

#### Scenario: Non-whitelisted tool args untouched
- **WHEN** the model calls `list_skills`, `read_skill`, `read_skill_file`, or `current_time`
- **THEN** main-agent SHALL pass the model's args to the tool byte-identical; no session_id injection SHALL occur
