## Why

Slices 1 and 2 delivered end-to-end tool calling with a built-in skills catalog. The system can now execute its own tools but can't reach anything external — no filesystem reads, no shell, no databases, no third-party APIs. MCP (Model Context Protocol) is the standards-track answer: a JSON-RPC protocol over stdio (or SSE/HTTP) that lets an agent discover and invoke tools hosted by separate processes. This slice adds a hand-rolled stdio MCP client to main-agent, a Valkey-backed config schema for declaring MCP servers, and dashboard CRUD so operators can enable/disable servers without editing files. Zero servers ship with microagent2; the user supplies them via config.

## What Changes

- Add a hand-rolled stdio JSON-RPC 2.0 MCP client in `internal/mcp/`. Supports the subset of the MCP spec required for tool discovery and invocation: `initialize`, `tools/list`, `tools/call`, and `notifications/*` (drained, ignored for now).
- Define a `config:mcp:servers` schema stored in Valkey as a JSON array of server definitions: `{name, enabled, command, args, env}`. Only stdio transport in v1; SSE/HTTP deferred.
- Main-agent at startup reads the MCP server config, spawns each enabled server, performs the initialize handshake, fetches the tool list, and registers each returned tool in the existing `tools.Registry` using the namespaced name `mcp__<server>__<tool>`.
- Invocations of `mcp__<server>__<tool>` route back through the right stdio client, which makes a `tools/call` JSON-RPC request and returns the content blocks flattened to a string.
- MCP servers managed only at main-agent startup. Config changes require a main-agent restart to take effect. Dashboard shows a "restart required" banner when live config differs from what's loaded.
- Dashboard gains an MCP panel (or section within Agents panel) for listing, adding, editing, and removing MCP server entries. Backed by new gateway API routes `GET /v1/mcp/servers`, `PUT /v1/mcp/servers` (replace entire list), `POST /v1/mcp/servers` (add one), `DELETE /v1/mcp/servers/:name` (remove one).
- Tool schemas discovered from MCP servers merged into `Registry.Schemas()` output alongside built-ins, so the LLM sees a unified tool list. The namespaced name prevents collisions with built-ins.
- Health visibility: `/v1/status` gains an `mcp_servers` field listing `{name, enabled, connected, tool_count, last_error}` so operators see connection state.
- Structured logging on every MCP lifecycle event: `mcp_server_spawned`, `mcp_initialize_ok`, `mcp_tools_registered`, `mcp_server_disconnected`, `mcp_invoke_done` with per-invocation timing.

Explicitly **out of scope**:
- SSE or HTTP transport — stdio only. A future change can add them.
- MCP prompts or resources features — only tools. MCP spec is broader; this slice ships the subset actually needed for tool calling.
- Hot-reload of MCP server config on mutation — restart required.
- MCP client concurrency safety for the **same** server handling **parallel** `tools/call` requests from the agent — we serialize through one request/response loop per stdio subprocess. Parallel agent tool calls against the same server will be sequentialized by the client.
- Authentication or sandboxing of MCP subprocesses beyond what the OS provides via `exec.Cmd` with the env explicitly passed. Operators run whatever they configure; trust is theirs.
- `allowed-tools` enforcement on skills loading MCP-provided tools — parsed in slice 2 but still unenforced.
- retro-agent gaining MCP access — stays out of the interactive surface.

## Capabilities

### New Capabilities
- `mcp-integration`: stdio MCP client, per-server subprocess lifecycle, tool discovery via `tools/list`, tool invocation via `tools/call`, registration into the existing `tools.Registry` with namespaced names.

### Modified Capabilities
- `configuration-store`: adds a new `config:mcp:servers` section, JSON-encoded array of server definitions, edited via dashboard.
- `dashboard-ui`: adds an MCP servers panel (or subsection of Agents panel) for CRUD of the server list, plus a "restart main-agent to apply" banner when live state differs.
- `gateway-api`: adds `GET/PUT/POST/DELETE` endpoints for `/v1/mcp/servers`. Extends `/v1/status` with `mcp_servers` health info.
- `tool-invocation` (slice 2): tool registry now accepts MCP-sourced tools; naming convention documented; registration happens after built-ins at main-agent startup.
- `system-health`: status endpoint exposes MCP server connection state per server.

## Impact

**Code**
- `internal/mcp/` (new package):
  - `client.go` — stdio subprocess manager + JSON-RPC 2.0 read/write loop, `Initialize`, `ListTools`, `CallTool`, graceful shutdown
  - `tool.go` — wraps a discovered MCP tool as a `tools.Tool`, handling invocation and content-block flattening
  - `manager.go` — per-agent orchestration: reads config, spawns enabled servers, registers tools, exposes health
  - `client_test.go`, `manager_test.go` — stdio handshake + tool invocation tests using a fake MCP server binary or a piped in-process fake
- `internal/config/` — adds `ResolveMCPServers(ctx, store)` returning `[]MCPServerConfig`
- `internal/gateway/server.go` — new MCP CRUD routes, `mcp_servers` field on `/v1/status`
- `internal/gateway/web/` — new MCP UI subsection (HTML + JS + CSS)
- `cmd/main-agent/main.go` — after built-in tool registration, instantiate `mcp.Manager`, register discovered tools, wire lifecycle into shutdown

**Wire format**
- New message types / pub-sub events: none. MCP traffic stays inside main-agent's process.

**Config schema**
- New Valkey key `config:mcp:servers` with JSON array value. Per-server shape:
  ```json
  {
    "name": "filesystem-ro",
    "enabled": true,
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-filesystem", "/data"],
    "env": {"KEY": "VALUE"}
  }
  ```
- `name` is the namespace prefix; MUST be non-empty, unique, and match `[a-zA-Z0-9_-]+`.

**Dependencies**
- No new Go modules. JSON-RPC 2.0 is hand-rolled with `encoding/json` + `bufio.Scanner` over `os/exec` pipes. Adds ~200 lines of Go.

**Environment**
- No new env vars. Main-agent reads MCP config from Valkey at startup; operators set server definitions via dashboard or `valkey-cli`.

**Regression surface**
- Main-agent with zero MCP servers configured behaves identically to slice-2 — no subprocesses spawned, no registry entries added beyond built-ins.
- Tool registry's insertion order (built-ins first, then MCP) means slice-2's list_skills/read_skill are still first in the LLM's tool array; no change to behavior users rely on.

**Does not touch**
- Broker, context-manager, retro-agent, response-chain, skills store.
- The client-facing API filter from the hidden-trace fix earlier: MCP tool calls are still agent-internal and stay hidden from `/v1/responses` clients via the existing `clientFacingOutput` filter.
