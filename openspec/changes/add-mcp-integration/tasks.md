## 1. Configuration-store extension

- [x] 1.1 Add `MCPServerConfig` struct in `internal/config/config.go` with JSON tags: `name`, `enabled`, `command`, `args`, `env`
- [x] 1.2 Implement `ResolveMCPServers(ctx, store) []MCPServerConfig` in `internal/config/resolve.go`: GET `config:mcp:servers`, parse, validate each entry (non-empty name matching `[a-zA-Z0-9_-]+`, non-empty command, no duplicate names), skip invalid entries with WARN log
- [x] 1.3 Implement `SaveMCPServers(ctx, store, servers []MCPServerConfig) error` that validates before SET and returns a typed error on validation failure
- [x] 1.4 Unit test `internal/config/config_test.go`: (a) empty key returns empty slice, (b) valid list round-trips, (c) invalid entries skipped with log, (d) Save rejects invalid names, (e) Save rejects duplicate names

## 2. Tool registry built-in prefix guard

- [x] 2.1 Edit `internal/tools/tool.go` `Registry.Register`: reject names starting with `mcp__` with a typed error; add an internal `registerMCPTool` method (lowercase, package-private) for the MCP manager to bypass the guard
- [x] 2.2 Unit test in `internal/tools/registry_test.go`: built-in registration with `mcp__foo` name returns error; `registerMCPTool` succeeds; existing registrations unaffected

## 3. MCP client package

- [x] 3.1 Create `internal/mcp/client.go` with `Client` struct holding `cmd *exec.Cmd`, `stdin io.WriteCloser`, `stdout *bufio.Scanner`, `pending map[int64]chan *rpcResponse`, `nextID atomic.Int64`, `writeMu sync.Mutex`, `logger *slog.Logger`, `name string`
- [x] 3.2 Implement `NewClient(ctx, cfg MCPServerConfig, logger) (*Client, error)` that spawns the subprocess (os/exec), wires stdin/stdout pipes, attaches a stderr-to-logger goroutine, and starts the read-loop goroutine
- [x] 3.3 Implement the read loop: `scanner.Scan()`, parse JSON-RPC messages, route responses (with `id`) to waiting channels, drain notifications (no `id`)
- [x] 3.4 Implement `Initialize(ctx)`: build `initialize` request with our `protocolVersion` and client info, send, wait for response, send `notifications/initialized`
- [x] 3.5 Implement `ListTools(ctx) ([]MCPTool, error)`: send `tools/list`, decode response into typed tools (name, description, inputSchema as raw JSON)
- [x] 3.6 Implement `CallTool(ctx, name, argsJSON) (string, error)`: send `tools/call` with parsed arguments, wait for response, flatten content blocks per D7 (text concat, non-text placeholder, wrap isError as JSON error)
- [x] 3.7 Implement invoke timeout via `MCP_INVOKE_TIMEOUT_S` env (default 30s): if the waiting channel doesn't receive within the deadline, return timeout error and leave pending entry (read loop will log+discard late response)
- [x] 3.8 Implement `Close()`: close stdin, wait up to 2s, then `cmd.Process.Kill()` on timeout; use `SysProcAttr.Setpgid=true` and kill the process group on Linux
- [x] 3.9 Unit test with an in-process fake server (goroutine pair using io.Pipe or os.Pipe): (a) Initialize handshake round-trips, (b) ListTools returns expected shape, (c) CallTool on text-only content flattens correctly, (d) CallTool on mixed content inserts placeholder, (e) isError wraps as JSON error, (f) invoke timeout returns error, (g) subprocess exit mid-call causes pending callers to receive disconnected error

## 4. MCP tool wrapper

- [x] 4.1 Create `internal/mcp/tool.go` with `mcpTool` struct implementing `tools.Tool`: holds a `*Client` pointer, server name, original tool name, description, inputSchema
- [x] 4.2 `Name()` returns `mcp__<server>__<tool>`; `Schema()` returns a `messaging.ToolSchema` with the namespaced name and the original description/parameters
- [x] 4.3 `Invoke(ctx, argsJSON)` delegates to `client.CallTool(ctx, originalName, argsJSON)`; on subprocess-exit error, returns `{"error":"mcp server <name> disconnected"}` with nil err
- [x] 4.4 Unit test: wrapper delegation, name composition, disconnected error path

## 5. MCP manager

- [x] 5.1 Create `internal/mcp/manager.go` with `Manager` struct holding `clients map[string]*Client` (keyed by server name), `health []HealthEntry`, `healthMu sync.Mutex`, `healthKey string`, `rdb *redis.Client`, `logger *slog.Logger`
- [x] 5.2 Implement `NewManager(rdb, logger) *Manager`
- [x] 5.3 Implement `Start(ctx, cfgs []MCPServerConfig, registry *tools.Registry)`: for each cfg in parallel (wait group), validate name, if enabled spawn + init + list + registerMCPTool each discovered tool; update health; log lifecycle events
- [x] 5.4 Implement a 30-second heartbeat goroutine that rewrites the health snapshot
- [x] 5.5 Implement `Close(ctx)`: iterate clients, call `Close()` in parallel, wait
- [x] 5.6 Implement `publishHealth()` helper that JSON-marshals the health array and SETs `health:main-agent:mcp`
- [x] 5.7 Unit test with fake stdio servers (started as goroutines piping through os.Pipe): (a) two servers register N+M tools, (b) disabled server skipped, (c) broken server (initialize errors) logged and skipped, other server still registers, (d) Close cleans up

## 6. Main-agent wiring

- [x] 6.1 Edit `cmd/main-agent/main.go`: after built-in tool registration, read `ResolveMCPServers`, instantiate `mcp.Manager`, call `Start(ctx, servers, toolRegistry)`; wire `Close` into the signal handler path
- [x] 6.2 Ensure the manifest-injection path is unchanged: MCP tools don't appear in the `<available_skills>` block (that's for skills only)
- [x] 6.3 Confirm tool registration order: built-ins first (list_skills, read_skill), then MCP tools server-by-server in config order
- [x] 6.4 Test the no-op case: empty MCP config behaves byte-identical to slice 2 (no subprocesses spawned, only 2 tools registered)

## 7. Gateway API endpoints

- [x] 7.1 Add routes in `internal/gateway/server.go`: `GET /v1/mcp/servers`, `PUT /v1/mcp/servers`, `POST /v1/mcp/servers`, `DELETE /v1/mcp/servers/:name`
- [x] 7.2 Implement handlers using `config.ResolveMCPServers` and `config.SaveMCPServers`; PUT body is `{"servers": [...]}`; POST body is a single server object; validate + return 400/409/404 as specified
- [x] 7.3 Extend `/v1/status` handler: read `health:main-agent:mcp` key, parse JSON array, include under `mcp_servers` top-level field; default to empty array when key missing
- [x] 7.4 Unit tests `internal/gateway/mcp_test.go`: GET empty, PUT valid list, PUT duplicate rejected, POST new, POST duplicate → 409, DELETE present → 204, DELETE missing → 404, status surfaces mcp_servers from Valkey

## 8. Dashboard UI

- [x] 8.1 Edit `internal/gateway/web/index.html`: add an MCP subsection within `#panel-agents` containing a table, Add button, and modal/inline form
- [x] 8.2 Edit `internal/gateway/web/app.js`: `loadMCPServers()` fetches `/v1/mcp/servers` and `/v1/status` concurrently, joins by name, renders rows with stored+live fields, computes drift to toggle a restart banner
- [x] 8.3 Implement Add, Edit, Delete flows wired to the new endpoints
- [x] 8.4 Edit `internal/gateway/web/style.css`: style for the MCP table and the restart-required banner (amber/warning tone)
- [x] 8.5 Manual verification: add a server via the UI, observe the banner appears; restart main-agent; banner disappears; table reflects live state

## 9. Docker and operator plumbing

- [x] 9.1 Update `.env.example` with `MCP_INVOKE_TIMEOUT_S` commented default
- [x] 9.2 Confirm docker-compose's main-agent has access to any binaries MCP servers might exec (document that operators are responsible for the tool ecosystem inside the container or by mounting volumes)
- [x] 9.3 No changes to compose file required for v1; operators who want `npx`-based MCP servers would need a main-agent image with node/npm — documented as an operator concern, not shipped

## 10. End-to-end verification

- [x] 10.1 Write a fake stdio MCP server binary in `internal/mcp/testserver/` or equivalent: simple Go program that speaks the protocol for tests
- [x] 10.2 Integration-style test in `internal/mcp/`: spawn the fake server via real os/exec, do full Start + invoke + Close cycle, assert tools registered and invocations round-trip
- [x] 10.3 Verify no-MCP regression: run slice-2's `TestHandleRequest_ToolLoopRunsTwoIterations` and confirm it still passes with the new codepath
- [x] 10.4 `go test ./...` — all green
- [x] 10.5 `go vet ./...` — clean
- [x] 10.6 `go build ./cmd/...` — zero errors
- [x] 10.7 Manual smoke: configure one real MCP server (e.g. `@modelcontextprotocol/server-everything` or a trivial local echo server), start the stack, observe tool_count in `/v1/status`, have the LLM invoke one tool, verify `mcp_invoke_done` logs and the tool result appears in the stored response trace (client-facing body still hides it, per the earlier filter fix)
