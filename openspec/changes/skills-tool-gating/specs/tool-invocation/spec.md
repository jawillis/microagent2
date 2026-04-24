## ADDED Requirements

### Requirement: Base toolset
Main-agent SHALL maintain a "base toolset" — the set of tool names that are always visible to the model regardless of which skill (if any) is active. For this change, the base toolset SHALL consist of every built-in tool registered at startup (`list_skills`, `read_skill`, `read_skill_file`, `current_time`) plus every MCP-sourced tool registered via the MCP manager (names beginning `mcp__<server>__`).

#### Scenario: Built-ins in base
- **WHEN** main-agent finishes initialization with the default built-in registrations
- **THEN** the base toolset SHALL include exactly `list_skills`, `read_skill`, `read_skill_file`, and `current_time`

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

## MODIFIED Requirements

### Requirement: MCP tool registration is additive to built-ins
Main-agent SHALL register built-in tools (`list_skills`, `read_skill`, `read_skill_file`, `current_time`) before MCP-sourced tools, so that `Registry.Schemas()` order places built-ins first. This preserves the invariant where the LLM's `tools` array always begins with the built-ins.

#### Scenario: Registration order
- **WHEN** main-agent initializes with built-ins and one or more MCP servers
- **THEN** the order of tools in `Registry.Schemas()` SHALL be: `list_skills`, `read_skill`, `read_skill_file`, `current_time`, then MCP tools in the order they were registered (which is the order returned by each server's `tools/list`, across servers in config order)

#### Scenario: No MCP servers configured
- **WHEN** `config:mcp:servers` is empty or missing
- **THEN** main-agent SHALL still register all four built-ins and `Registry.Schemas()` SHALL return exactly those built-ins in order: `list_skills`, `read_skill`, `read_skill_file`, `current_time`

#### Scenario: Built-in registered before MCP
- **WHEN** main-agent starts with one MCP server that exposes tool `foo` and registers its built-ins first
- **THEN** `Registry.Schemas()` SHALL return `list_skills`, `read_skill`, `read_skill_file`, `current_time`, `mcp__<server>__foo` in that exact order

### Requirement: Tool invocation logging
Main-agent SHALL emit a structured INFO log line for every tool invocation attempt, including gated calls that did not reach the registry.

#### Scenario: Invocation logged
- **WHEN** main-agent invokes a tool via `Registry.Invoke`
- **THEN** it SHALL log at INFO with `msg: "tool_invoked"` and fields `{correlation_id, tool_name, args_bytes, elapsed_ms, outcome, result_bytes, active_skill}` where `outcome` is one of `ok`, `error` (including unknown-tool and argument-parse errors), `panic`, or `gated`

#### Scenario: Gated calls logged
- **WHEN** main-agent gates a tool call without invoking the registry
- **THEN** it SHALL log at INFO with `msg: "tool_invoked"`, `outcome="gated"`, `tool_name=<name>`, `active_skill=<active>`, `elapsed_ms=0`, and `result_bytes` equal to the length of the emitted error envelope
