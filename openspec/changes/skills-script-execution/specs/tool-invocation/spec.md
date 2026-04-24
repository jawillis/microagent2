## ADDED Requirements

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

## MODIFIED Requirements

### Requirement: Base toolset
Main-agent SHALL maintain a "base toolset" — the set of tool names that are always visible to the model regardless of which skill (if any) is active. The base toolset SHALL consist of every built-in tool registered at startup (`list_skills`, `read_skill`, `read_skill_file`, `current_time`, `run_skill_script`) plus every MCP-sourced tool registered via the MCP manager (names beginning `mcp__<server>__`).

#### Scenario: Built-ins in base
- **WHEN** main-agent finishes initialization with the default built-in registrations
- **THEN** the base toolset SHALL include exactly `list_skills`, `read_skill`, `read_skill_file`, `current_time`, and `run_skill_script`

#### Scenario: MCP tools in base
- **WHEN** main-agent finishes initialization with one or more MCP servers that register tools
- **THEN** the base toolset SHALL additionally include every registered `mcp__<server>__<tool>` name

#### Scenario: Base is a stable set after startup
- **WHEN** the tool loop runs multiple turns for multiple sessions
- **THEN** the base toolset SHALL be identical across turns (no runtime registration of new tools)

### Requirement: MCP tool registration is additive to built-ins
Main-agent SHALL register built-in tools (`list_skills`, `read_skill`, `read_skill_file`, `current_time`, `run_skill_script`) before MCP-sourced tools, so that `Registry.Schemas()` order places built-ins first. This preserves the invariant where the LLM's `tools` array always begins with the built-ins.

#### Scenario: Registration order
- **WHEN** main-agent initializes with built-ins and one or more MCP servers
- **THEN** the order of tools in `Registry.Schemas()` SHALL be: `list_skills`, `read_skill`, `read_skill_file`, `current_time`, `run_skill_script`, then MCP tools in the order they were registered (which is the order returned by each server's `tools/list`, across servers in config order)

#### Scenario: No MCP servers configured
- **WHEN** `config:mcp:servers` is empty or missing
- **THEN** main-agent SHALL still register all five built-ins and `Registry.Schemas()` SHALL return exactly those built-ins in order: `list_skills`, `read_skill`, `read_skill_file`, `current_time`, `run_skill_script`

#### Scenario: Built-in registered before MCP
- **WHEN** main-agent starts with one MCP server that exposes tool `foo` and registers its built-ins first
- **THEN** `Registry.Schemas()` SHALL return `list_skills`, `read_skill`, `read_skill_file`, `current_time`, `run_skill_script`, `mcp__<server>__foo` in that exact order
