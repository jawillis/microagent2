## ADDED Requirements

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

## MODIFIED Requirements

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
