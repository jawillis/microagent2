## ADDED Requirements

### Requirement: stdio MCP client implementation
The `internal/mcp` package SHALL implement an MCP client that speaks JSON-RPC 2.0 over stdio to a subprocess. The client SHALL support the MCP methods required for tool use: `initialize`, `tools/list`, and `tools/call`. Notifications (messages without an `id`) SHALL be read and discarded so their presence does not corrupt the request/response correlation.

#### Scenario: Initialize handshake
- **WHEN** a new `Client` is constructed and its `Initialize(ctx)` method is called
- **THEN** the client SHALL send a JSON-RPC request with method `initialize` and a `protocolVersion` compatible with the MCP spec, wait for the response, then send a `notifications/initialized` notification

#### Scenario: Tool discovery
- **WHEN** `Client.ListTools(ctx)` is called on an initialized client
- **THEN** the client SHALL send a `tools/list` JSON-RPC request and return the server's reported tools (each with `name`, `description`, and `inputSchema`) as a typed slice

#### Scenario: Tool invocation
- **WHEN** `Client.CallTool(ctx, name, argsJSON)` is called
- **THEN** the client SHALL send a `tools/call` request with `params.name` and `params.arguments` (the argument object parsed from `argsJSON`), wait for the response, and return the flattened text content or a flattened error

#### Scenario: Notifications drained
- **WHEN** the MCP server sends a JSON-RPC message with no `id` field (a notification)
- **THEN** the client SHALL accept and discard it without disrupting the correlation table used to match responses to pending requests

### Requirement: Per-server subprocess lifecycle
The MCP manager SHALL, for each enabled server in `config:mcp:servers`, spawn a subprocess via `os/exec` with the configured `command`, `args`, and `env`, run the `Initialize`/`ListTools` sequence, and register each discovered tool in `tools.Registry` under the name `mcp__<server.name>__<tool.name>`. A server whose startup fails SHALL be logged and skipped; the manager SHALL proceed to the next server.

#### Scenario: Enabled server spawns
- **WHEN** the manager processes a server config with `enabled: true`
- **THEN** it SHALL start the subprocess, perform the initialize handshake, call `tools/list`, and register each returned tool into the shared registry with the namespaced name

#### Scenario: Disabled server skipped
- **WHEN** the manager processes a server config with `enabled: false`
- **THEN** it SHALL NOT spawn the subprocess and SHALL NOT register any tools for that server

#### Scenario: Startup failure is non-fatal
- **WHEN** a server's subprocess spawn, initialize, or tools/list fails
- **THEN** the manager SHALL log at ERROR with `msg: "mcp_server_startup_failed"` and fields `{name, error, phase}`, SHALL NOT register tools for that server, and SHALL continue processing remaining servers

#### Scenario: Parallel startup
- **WHEN** the manager processes N enabled servers
- **THEN** the servers SHALL be started in parallel (fan-out goroutines with wait-group), so total startup latency is bounded by the slowest server's initialize rather than the sum

#### Scenario: Shutdown cleanup
- **WHEN** the manager's context is canceled (main-agent shutdown)
- **THEN** for each running subprocess, the manager SHALL close stdin to signal graceful exit, wait up to a 2-second grace period, and then kill the process group if it has not exited

### Requirement: Namespaced tool registration
MCP-sourced tools SHALL be registered in the shared `tools.Registry` with names of the form `mcp__<server.name>__<tool.name>`. Built-in tool names starting with `mcp__` SHALL be rejected at registration time to prevent namespace collisions.

#### Scenario: MCP tool name composition
- **WHEN** server `filesystem-ro` exposes a tool named `read_file`
- **THEN** the registry entry SHALL have the name `mcp__filesystem-ro__read_file`

#### Scenario: Built-in mcp__ prefix rejected
- **WHEN** a caller attempts to register a non-MCP `Tool` whose `Name()` starts with `mcp__`
- **THEN** `Registry.Register` SHALL return a non-nil error and SHALL NOT add the tool

#### Scenario: Server name validated
- **WHEN** the manager processes a server config whose `name` is empty, contains characters outside `[a-zA-Z0-9_-]`, or duplicates another server's name
- **THEN** the manager SHALL log at WARN with `msg: "mcp_server_invalid_name"` and fields `{name, reason}`, skip that server, and continue

### Requirement: Invocation serialization per server
The MCP client SHALL serialize all `tools/call` requests issued to a given subprocess. A call SHALL NOT be dispatched until the previous call's response has been received or the invoke timeout has expired.

#### Scenario: Sequential calls to same server
- **WHEN** the agent loop invokes two MCP tools on the same server within one iteration
- **THEN** the client SHALL issue the second `tools/call` only after the first has returned or timed out

#### Scenario: Independent servers run in parallel
- **WHEN** the agent loop invokes one tool on server `foo` and one on server `bar`
- **THEN** the two `tools/call` requests SHALL proceed independently without cross-blocking

#### Scenario: Invoke timeout
- **WHEN** a `tools/call` response does not arrive within `MCP_INVOKE_TIMEOUT_S` seconds (default 30)
- **THEN** `CallTool` SHALL return an error whose flattened representation is `{"error":"mcp server <name> call timed out"}`, and the client SHALL log at WARN with `msg: "mcp_invoke_timeout"` and fields `{server, tool, call_id}`

### Requirement: Content-block flattening
The tool wrapper that exposes an MCP tool as a `tools.Tool` SHALL flatten the `content` array returned by `tools/call` into a single string. Text blocks SHALL be concatenated in order; non-text blocks SHALL be replaced by the marker `[non-text content omitted: <type>]`. When the response has `isError: true`, the flattened string SHALL be wrapped as JSON `{"error": "<flattened>"}` to match the existing tool-error convention.

#### Scenario: All-text content
- **WHEN** a `tools/call` response has `content: [{"type":"text","text":"a"},{"type":"text","text":"b"}]` and `isError: false`
- **THEN** the wrapper's `Invoke` return SHALL be the string `"ab"`

#### Scenario: Mixed content
- **WHEN** the response contains a text block and an image block
- **THEN** the return SHALL include the text and the placeholder `"[non-text content omitted: image]"` in the position the image appeared

#### Scenario: Error result wrapped
- **WHEN** the response has `isError: true` and text content `"bad input"`
- **THEN** the return SHALL be exactly `{"error":"bad input"}`

### Requirement: Disconnected server reports errors on invoke
When a subprocess has exited after startup, subsequent invocations of that server's tools SHALL return a structured error without re-spawning or panicking.

#### Scenario: Invoke after subprocess exit
- **WHEN** a server's subprocess has exited and the agent invokes one of its registered tools
- **THEN** the tool's `Invoke` SHALL return the string `{"error":"mcp server <name> disconnected"}` and a nil error, allowing the tool loop to continue

### Requirement: stderr piped to logger
The manager SHALL attach each subprocess's stderr to a reader goroutine that logs each line at INFO with `msg: "mcp_server_stderr"` and fields `{name, line}`, preserving diagnostic output for operators.

#### Scenario: Stderr line logged
- **WHEN** a subprocess writes a line to its stderr
- **THEN** the manager SHALL log that line at INFO with the `mcp_server_stderr` message and the server's name

### Requirement: Lifecycle logging
The manager SHALL emit structured INFO log lines at every lifecycle event: spawn, initialize success, tools registration, disconnection, and invocation completion.

#### Scenario: Lifecycle events
- **WHEN** a server is spawned, initialized, has its tools registered, disconnects, or completes an invocation
- **THEN** the manager SHALL emit (respectively) `mcp_server_spawned`, `mcp_initialize_ok`, `mcp_tools_registered`, `mcp_server_disconnected`, `mcp_invoke_done` log lines at INFO, with fields appropriate to each event (including `name`, `tool_count`, `elapsed_ms`, `outcome`)

### Requirement: Health state published for gateway consumption
The manager SHALL write a JSON health snapshot to Valkey key `health:main-agent:mcp` on every connect, disconnect, register, and at a 30-second heartbeat. The snapshot SHALL be an array of `{name, enabled, connected, tool_count, last_error}` entries so the gateway's `/v1/status` handler can surface it without cross-process RPC.

#### Scenario: Health snapshot shape
- **WHEN** the manager writes the health snapshot
- **THEN** the Valkey value SHALL be a JSON array of objects with `name`, `enabled`, `connected`, `tool_count`, and `last_error` fields; disabled servers SHALL appear with `connected: false` and `tool_count: 0`

#### Scenario: Heartbeat refreshes snapshot
- **WHEN** 30 seconds have elapsed since the last snapshot write
- **THEN** the manager SHALL rewrite the snapshot with current state even if no lifecycle event has occurred
