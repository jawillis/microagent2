## Context

Slices 1 and 2 gave us a tool-calling protocol and a server-side tool executor backed by a filesystem skills catalog. The registry abstraction is in place. What's missing is external-tool access: the agent can read skills but can't read a file, query a DB, or hit an API.

MCP (Model Context Protocol) is the right layer. It's an open standard — JSON-RPC 2.0 over stdio/SSE/HTTP — that separates tool hosts (MCP servers) from tool consumers (clients like us). Servers advertise tools via `tools/list`; clients invoke via `tools/call`. The spec covers more (prompts, resources, sampling), but for microagent2's needs, tools are the only surface we exercise in this slice.

We deliberately scoped the exploration: hand-rolled stdio client, no vendored MCP library. The Go ecosystem has `mark3labs/mcp-go` but its maturity is thin; for a ~200-line client that speaks one subset of the protocol, the maintenance risk of a dependency exceeds the maintenance cost of owning the code.

Stakeholders: main-agent (host), operators (config authors via dashboard), external MCP server processes (arbitrary — npm packages, Python scripts, Go binaries).

## Goals / Non-Goals

**Goals:**
- Operators can add an MCP server to a running deployment by editing config in the dashboard, restarting main-agent, and seeing the server's tools appear in the registry.
- Discovered MCP tools show up in the LLM's tools array with namespaced names `mcp__<server>__<tool>`, preventing collisions with built-ins or other servers.
- MCP tool invocations flow through the same `tools.Registry.Invoke` path as built-ins — no special casing in the agent loop.
- Connection/tool-count/last-error state visible on `/v1/status` for operator visibility.
- Zero MCP servers configured = behavior byte-identical to slice 2.
- Tool invocations from the agent loop are sequentialized per server (no parallel JSON-RPC requests to the same stdio subprocess) to stay inside MCP's request/response semantics.

**Non-Goals:**
- Non-stdio transports (SSE, HTTP). Spec-compliant, but deferred.
- MCP prompts, resources, sampling, roots, completions. Only tools.
- Hot-reload of server config. Restart is the contract.
- Parallel requests to the same MCP server. If the agent loop calls two `mcp__foo__*` tools in one iteration, they're issued sequentially.
- Sandboxing / authentication of MCP subprocesses. Operators own trust.
- Streaming tool-call results inside one `tools/call` response. MCP's streaming content model (`notifications/progress`) is not implemented; we wait for the full response.
- Letting retro-agent use MCP tools.

## Decisions

### D1: Hand-roll stdio JSON-RPC 2.0

MCP-over-stdio is line-delimited JSON: one JSON-RPC message per line on stdin/stdout. Reading/writing this is ~100 lines:

```go
type Client struct {
    cmd   *exec.Cmd
    stdin io.WriteCloser
    stdout *bufio.Scanner
    pending map[int64]chan *rpcResponse  // id → result channel
    nextID atomic.Int64
    mu sync.Mutex   // protects pending + writes
}
```

Write path: serialize `{jsonrpc:"2.0", id, method, params}`, append `\n`, write under mu.
Read loop: `scanner.Scan()`, parse as `rpcMessage`, route by `id` to the waiting channel; drain notifications (no `id`) silently for now.

*Alternatives considered:* `mark3labs/mcp-go` (emerging, adds a dependency tree, forces its types on us, maintenance risk outweighs its value at our scale); `sourcegraph/jsonrpc2` (more generic, also a dependency, still need MCP-semantic wrappers). For stdio JSON-RPC of this modest shape, hand-rolling wins on transparency and LOC.

### D2: Config schema — JSON array in one Valkey key

`config:mcp:servers` holds a JSON array value, not N individual keys. Simpler atomic reads/writes for the dashboard. Per-server shape:

```go
type MCPServerConfig struct {
    Name    string            `json:"name"`
    Enabled bool              `json:"enabled"`
    Command string            `json:"command"`
    Args    []string          `json:"args"`
    Env     map[string]string `json:"env"`
}
```

Validation at the config-store boundary: names must be unique, non-empty, `[a-zA-Z0-9_-]+`. Invalid entries logged and skipped; they do not fail the whole read.

*Alternatives considered:* one key per server (`config:mcp:server:<name>`) — cleaner for partial updates but needs a list index key or a `SCAN` for enumeration; atomic multi-server edits from the dashboard become two round-trips. Single-key JSON wins for small N and UI-driven edits.

### D3: Naming — `mcp__<server>__<tool>`

Double-underscore separators match Claude Code's MCP convention; they're unlikely to clash with tool names and trivially parseable. A built-in named `mcp__foo__bar` would be forbidden by validating built-in names at registration (reject `mcp__` prefix). MCP servers get their own prefix namespace, collision-free.

Example: a server with `name: "filesystem-ro"` exposing tool `read_file` registers as `mcp__filesystem-ro__read_file`.

*Alternatives considered:* single `__` separator (less distinct, risk of accidental collision), `/` or `.` separators (harder to embed in tool name fields that some model parsers trim), registry-side aliasing (adds mapping complexity).

### D4: Per-server subprocess, started at main-agent init

Spawn strategy:
- At startup, for each enabled server in config, fork `exec.Cmd` with `command + args + env`, connect pipes.
- Perform the MCP `initialize` handshake (send our `initialize` params including `protocolVersion`, wait for response, send `notifications/initialized`).
- Call `tools/list`, record the tool schemas, register each as an MCP-sourced `tools.Tool`.
- Keep the subprocess alive for main-agent's lifetime.
- On shutdown (SIGINT/SIGTERM or context cancellation), send `notifications/shutdown` (best effort), close stdin, wait up to a grace period, then kill.

If `initialize` fails, `tools/list` fails, or the subprocess exits early during startup, log at ERROR and proceed without that server's tools. Do NOT crash the agent. A broken MCP server should not take down the whole agent.

*Alternatives considered:* lazy spawn on first tool call (adds startup jitter and complicates the registry — tools would need to exist in the registry before knowing they work); per-invocation spawn (wastes startup cost on every tool call, violates MCP's session model).

### D5: Connection loss = tool disappears

If a subprocess exits after startup (crash, operator kills it), the client marks that server as disconnected. Subsequent invocations of its tools return `{"error":"mcp server <name> disconnected"}` as a normal tool result — no panic, no registry mutation (tool stays "visible" to the LLM but errors on call; the LLM sees the error and can adjust).

No auto-restart in v1. A future change can add it.

*Alternatives considered:* remove tools from registry on disconnect (requires a mutex on the registry's read path; tools disappearing mid-turn is surprising to the LLM); retry-on-invoke (adds latency spikes, hides the underlying problem).

### D6: Invocation serialization per server

A single stdio subprocess speaks one JSON-RPC session. Interleaving N concurrent `tools/call` requests works *because each has its own id*, but the MCP spec models tool calls as synchronous. We serialize writes to stdin under a mutex and let the read loop route responses by `id` to the waiting caller. This means:

- If the agent loop emits two `mcp__foo__*` tool calls, they're invoked serially (one's response must land before the next is dispatched).
- Different servers are fully independent.

`tools/call` timeout: env `MCP_INVOKE_TIMEOUT_S` (default 30). On timeout, return `{"error":"mcp server <name> call timed out"}` and leave the pending request entry in place (response may arrive later, will be logged and discarded — simpler than trying to cancel mid-flight).

*Alternatives considered:* true parallel fan-in within one client (works protocol-wise, adds complexity for negligible speedup when the agent loop is already serial per-iteration); per-call subprocess (hopeless overhead).

### D7: Content-block flattening for tool results

MCP `tools/call` returns `content: [{type, text}, {type, image, ...}, ...]`. Our registry expects a single string. Flattening rule:

- For `type: "text"` blocks: concatenate `.text` fields in order.
- For any other type: append a synthetic `[non-text content omitted: <type>]` marker so the model knows something was dropped without polluting the string with binary.
- If `isError: true` in the response, wrap the flattened string in `{"error": "<flattened>"}` JSON so it matches our existing error convention.

*Alternatives considered:* return structured content to the model (no — would force the entire tool-invocation contract to change upstream); drop non-text silently (model can't reason about what it got); fail the call (too strict; a multi-block response with one image shouldn't fail the text content).

### D8: Dashboard CRUD

New UI subsection under the Agents panel (simpler than a new top-level panel). Shows:

- Table of servers: `name, enabled, command, connected?, tool_count, last_error`
- "Add server" form with inputs for name, command, args (text, space-delimited), env (text area, `KEY=VALUE` lines), enabled checkbox
- Edit (reopen form with fields populated) and Delete (with confirmation)
- Persistent banner at top of section: "Config changed — restart main-agent to apply" when the live state differs from the stored config

Live state comes from `/v1/status`. Stored config comes from `/v1/mcp/servers`. Diff is computed client-side.

*Alternatives considered:* separate MCP panel (inflates top-level nav for a feature that conceptually belongs with Agents); raw JSON editor (unfriendly for a non-JSON-native operator audience). Form-based with a restart banner keeps the CRUD familiar.

### D9: API shape for dashboard endpoints

- `GET /v1/mcp/servers` — returns the stored list (server configs).
- `PUT /v1/mcp/servers` — replaces the entire list. Body: `{"servers": [...]}`. Validates each entry server-side; rejects on duplicate name or invalid shape with 400.
- `POST /v1/mcp/servers` — append one server. Body: single server object. 409 on duplicate name.
- `DELETE /v1/mcp/servers/:name` — remove by name. 404 if not present.

All four write endpoints update `config:mcp:servers` atomically (single SET). No cross-key transactions needed because it's one key.

*Alternatives considered:* JSON-Patch on the list (powerful, but we don't have a PATCH consumer and it's overkill); separate endpoints per field (bloats the API surface). CRUD of whole-list + one-add/one-delete strikes the right balance for a small-N config.

### D10: Health reporting via `/v1/status`

The existing `/v1/status` handler runs in the gateway. MCP connection state lives in main-agent's memory. Two options:

a) Main-agent publishes health to a shared Valkey key (`health:main-agent:mcp`) that the gateway reads.
b) Gateway queries main-agent over a new RPC / pub-sub.

Option (a) is simpler and matches the existing config-in-Valkey pattern. Main-agent writes a JSON blob on every connect/disconnect/register event and on a 30s heartbeat; gateway reads and surfaces under `mcp_servers`.

*Alternatives considered:* option (b) — adds a new RPC pattern for just this; (a) reuses Valkey.

## Risks / Trade-offs

- **[Risk] Malicious or buggy MCP server hangs the subprocess.** A server that never returns from `tools/call` would tie up the agent loop on that iteration until the invoke timeout trips. → Mitigation: `MCP_INVOKE_TIMEOUT_S` default 30; tool invocation in the agent loop doesn't hold a broker slot (per slice 2), so slot starvation is bounded to the timeout.

- **[Risk] Startup latency scales with MCP server count.** Each server spawn + initialize + tools/list takes time; serial startup blocks main-agent readiness. → Mitigation: parallelize the startup dance across servers (fan-out goroutines with a wait group); total startup time = slowest server, not sum.

- **[Risk] Orphaned subprocesses on crash.** If main-agent SIGKILLs, children may linger. → Mitigation: set `Pdeathsig: syscall.SIGTERM` (Linux) / document the risk on macOS where this isn't available. Use `os/exec` + `cmd.SysProcAttr.Setpgid=true` so children can be reaped by a parent-tree kill.

- **[Risk] Dashboard doesn't warn about config drift.** Operator edits config, forgets to restart, wonders why nothing changed. → Mitigation: explicit "restart required" banner computed client-side from `GET /v1/status` vs `GET /v1/mcp/servers`.

- **[Risk] Built-in / MCP tool name collision.** A built-in tool literally named `mcp__foo__bar` would be ambiguous. → Mitigation: validate at `Registry.Register` — any name starting with `mcp__` is forbidden for built-ins; internal assertion.

- **[Risk] stdio buffering swallows logs.** If an MCP server writes to stderr, we need to surface it or it's lost. → Mitigation: attach the subprocess's stderr to a goroutine that pipes each line through our logger at INFO with `mcp_server_stderr` and `{server_name}`.

- **[Risk] Large tool schemas bloat the LLM request.** A prolific MCP server could register 50 tools, blowing up the `tools` array on every LLM call. → Accepted: operators decide what to wire up. We don't filter.

- **[Trade-off] Tool invocation serialization per server.** Two sequential MCP calls in one loop iteration = 2× latency vs parallel. For typical flows (one tool per iteration), this is moot. → Accepted; matches MCP's synchronous model.

- **[Trade-off] Content flattening drops non-text blocks.** Image/binary content from MCP servers becomes a placeholder. → Accepted; slice doesn't ship multimodal anywhere, future concern.

## Migration Plan

No data migration. Rolling deploy:

- Old main-agent / new gateway: gateway exposes MCP CRUD routes that write to Valkey. Old main-agent doesn't read them. No behavior change. Config drift banner will always show "restart required" but no action happens.
- New main-agent / old gateway: main-agent reads the MCP config key. If empty (which it will be until dashboard is deployed), no servers start. Behavior identical to slice 2.
- New both: full feature live.

Rollback: revert binaries. The `config:mcp:servers` key stays in Valkey and is harmless to old code (nothing reads it).

## Open Questions

1. **Should `/v1/mcp/servers` endpoints live under `/v1/config/mcp/...` to mirror the existing config API pattern?** Existing config uses section-scoped PUTs via `/v1/config`. MCP servers are a different shape (list of objects, not flat key-value). **Lean:** separate namespace `/v1/mcp/servers` because the CRUD semantics differ; mixing shapes under `/v1/config` would muddy the spec.

2. **Should the client auto-reconnect on subprocess exit?** Current design says no, failures stick until restart. **Lean:** keep it explicit for v1; auto-reconnect adds timing state (exponential backoff, fail-fast conditions) worth a change of its own.

3. **Content type for multimodal MCP responses — should we feed images into an eventual image-capable LLM?** Our LLM is llama.cpp-served text only today. **Lean:** placeholder string, revisit when we have multimodal.

4. **How does the Manifest() method on the Registry handle MCP-sourced tools?** Main-agent's `injectSkillManifest` uses the skills store, not the registry's manifest. MCP tools don't need a system-prompt manifest — they live in the `tools` array the LLM sees. **Lean:** leave `Manifest()` as-is; MCP tools don't auto-inject into system prompt.
