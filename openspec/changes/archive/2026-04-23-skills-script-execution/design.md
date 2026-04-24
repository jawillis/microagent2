## Context

This is change 4, the final slice of the Anthropic agent-skills runtime parity sequence described in `docs/skills-runtime-design.md`. The three prior changes established:

1. **skills-format-subdirs** — subdirectories preserved; `read_skill_file` tool for progressive disclosure.
2. **skills-tool-gating** — `allowed-tools` honored at runtime; active-skill session state; base toolset; invoke-time gating; `current_time`.
3. **exec-service** — sandboxed HTTP code-execution runtime at `http://exec:8085`; verified live (1211 MB image, install + run + timeout all green).

What remains is wiring main-agent's tool loop to the `exec` HTTP API so the model can invoke a skill's scripts. This document covers only the client + tool layer; the `exec` contract itself is authoritative in `openspec/specs/code-execution/spec.md`.

Current state of the relevant surface:
- `internal/tools/` holds the registry and the five built-ins (`list_skills`, `read_skill`, `read_skill_file`, `current_time`, plus the slots for MCP-sourced tools).
- `cmd/main-agent/main.go` computes `baseTools` from the manifest after `mcpMgr.Start` and carries it through `requestDeps`. Adding a new built-in means registering it and it automatically joins the base.
- `internal/memoryclient/memoryclient.go` is the house pattern for Go HTTP clients calling an in-cluster microagent service: a struct holds `*http.Client` + base URL, options-functional-style constructor, request helper centralizes retries and timeouts.
- `docker-compose.yml` has the `exec` service; `main-agent` does not currently reference it.

Constraints:
- Must not break the existing 5-tool base or the `allowed-tools` gating behavior. Fifth tool joins in the same mechanism change 2 already built.
- Must handle exec being temporarily unavailable (slow start, restart) without crashing main-agent. Lazy first-call is the right pattern.
- Tool result string must be consumable by LLMs. JSON envelopes are fine; the other built-ins already use JSON.
- No changes to `code-execution` spec. If implementation reveals a missing contract detail, update the exec spec in a follow-up rather than modify it under this change.

## Goals / Non-Goals

**Goals:**
- Land `internal/execclient/` as a tiny HTTP client for the three exec endpoints, mirroring `internal/memoryclient/` structure for consistency.
- Land `internal/tools/run_skill_script.go` as the main-agent-facing tool that calls `execclient.Run` and returns the envelope as the tool result.
- Pull `session_id` out of the per-turn payload so per-session workspaces work correctly across iterations.
- Register the new tool before MCP start so it joins the base toolset at its stable position (after `current_time`, before any MCP-sourced tool).
- Update `docker-compose.yml` so main-agent declares its dependency on exec and knows where to reach it.
- Preserve the existing `tool_invoked` log format; this change adds no new structured-log events.

**Non-Goals:**
- Extending `read_skill_file` to also read workspace paths. The tool envelope returns workspace paths and outputs; any follow-up reading is left to a future change if the model's needs exceed the inline caps.
- Retries on the HTTP client. Skills are request-scoped; a retry on a transient network error changes run semantics (duplicate side effects). Callers re-invoke the tool if they want another attempt.
- Circuit-breaker or health-gated routing. If exec is down, the tool fails fast and returns an error envelope; the model sees it and can adjust.
- Streaming of stdout as the subprocess produces it. The exec HTTP call is synchronous; streaming is a v2 concern if it's ever requested.
- Passing arbitrary environment variables from main-agent into the script. exec constructs the subprocess env from scratch on its side; main-agent has no input into that.
- Per-user authentication on the exec endpoints. The compose network is the trust boundary per `code-execution` spec and `docs/skills-runtime-design.md` §5.
- Changes to `sessionskill` or active-skill logic. The model can still `read_skill("foo")` to activate, then `run_skill_script("foo", "scripts/x.py")` to run — the two tools do not need to coordinate because `run_skill_script` works for any existing skill regardless of active-skill state (its behavior is orthogonal to tool visibility gating).
- A `Health` probe from main-agent at startup. Lazy-connect is enough; if the very first `run_skill_script` call fails because exec isn't ready yet, the model sees the error and retries.

## Decisions

### Decision 1: Client lives in `internal/execclient/`, mirrors `memoryclient`

```go
package execclient

type Client struct {
    baseURL    string
    httpClient *http.Client
}

type Option func(*Client)

func WithHTTPClient(c *http.Client) Option { ... }

func New(baseURL string, opts ...Option) *Client { ... }

func (c *Client) Run(ctx context.Context, req *RunRequest) (*RunResponse, error)
func (c *Client) Install(ctx context.Context, skill string) (*InstallResponse, error)
func (c *Client) Health(ctx context.Context) (*HealthResponse, error)
```

The request/response types alias `internal/exec.RunRequest`/`.RunResponse`/`.InstallResponse`/`.HealthResponse`. `internal/exec` can be imported from `internal/execclient` because they're both under the module's internal tree; keeping one source of truth for the wire types avoids drift.

Alternative considered: define fresh types in `execclient` to enforce the boundary (client should not depend on server internals). Rejected because (a) the wire types are small and stable, (b) `memoryclient` follows the same import-the-shared-types pattern today, (c) duplicating types would invite drift the first time someone evolves exec's envelope.

### Decision 2: Tool returns the exec envelope verbatim

The tool's `Invoke` returns the exec envelope as a JSON string. No transformation, no summary, no field whitelist. Rationale:

- Other built-ins (`list_skills`) already return JSON arrays; this is idiomatic.
- The envelope is bounded by exec's server-side caps (total ~32 KB worst case; typically 1–10 KB).
- The model needs every field eventually: `exit_code` for success/fail reasoning, `stdout` for content, `outputs` for path references, `duration_ms` for time awareness, `timed_out` for deciding to retry, etc.

Alternative considered: return only `stdout` on success with the other fields promoted to separate tool messages. Rejected — tool results are already strings; splitting loses cohesion and forces the model to correlate multiple results.

### Decision 3: Error envelope for client-side failures

When the HTTP client fails (connection refused, context deadline, non-200 response), the tool returns a JSON error envelope consistent with the pattern used by `read_skill` and `read_skill_file`:

```json
{"error":"exec unavailable: connection refused"}
{"error":"exec request failed: deadline exceeded"}
{"error":"exec returned 500: internal error"}
```

The `Invoke` signature returns `(string, error)`. In every client-failure case the tool returns `(envelope, nil)` — a non-nil Go error would break the registry's "tool output is always a string" invariant (see `tool-invocation` spec §"Tool errors surface as structured errors"). The only time `Invoke` returns a non-nil error is if JSON marshaling the envelope itself fails, which is never in practice.

### Decision 4: HTTP client timeout = EXEC_MAX_TIMEOUT_S + 10s

Exec's server-side timeout (default 120s) kills the subprocess and returns the envelope with `timed_out: true`. The main-agent HTTP client needs a slightly larger deadline so the envelope has time to reach it before the client times out. Adding 10s gives exec enough headroom to flush stdout/stderr captures, finalize the workspace metadata, and serialize the response.

```go
clientTimeout := time.Duration(cfg.ExecMaxTimeoutS+10) * time.Second
```

The `cfg.ExecMaxTimeoutS` value is read from `EXEC_MAX_TIMEOUT_S` on main-agent's side (new env var on main-agent) so both sides have a consistent view. Default is 120; operators who raise it on exec must raise it on main-agent too or get premature client timeouts.

Alternative considered: let main-agent's HTTP client use no timeout and rely entirely on the request context. Rejected — a client with no timeout is a liability if exec hangs for any reason (goroutine leak, inaccessible endpoint). Belt-and-suspenders: context deadline + client timeout.

### Decision 5: `session_id` sourced from the turn payload

`invokeOrGate` in `cmd/main-agent/main.go` currently takes `(ctx, call, visible, activeName)`. It gains a fifth parameter `sessionID` so the tool invocation knows which session to scope workspaces to. The session ID already exists on the `ContextAssembledPayload` at the top of `handleRequest`; plumbing it down three call sites is mechanical.

Alternative considered: use `context.WithValue` to propagate the session ID. Rejected — typed function parameters are clearer and the call depth is shallow. Context-value propagation is a legitimate pattern when the middleware stack is deep; for three hops it's over-engineered.

For tools that do not need the session ID (all existing ones: `list_skills`, `read_skill`, `read_skill_file`, `current_time`), the new parameter is ignored. Only `run_skill_script` reads it.

### Decision 6: Tool receives `session_id` via argsJSON, not a new Invoke signature

The `Tool.Invoke(ctx, argsJSON)` interface is shared across all tools. Adding a session-id parameter would require either changing every tool's signature (intrusive) or creating a parallel interface (duplicative). Instead, main-agent's `invokeOrGate` injects the session id into the tool's arguments JSON before calling `Registry.Invoke` for `run_skill_script` specifically.

```go
// For run_skill_script, main-agent rewrites argsJSON to add session_id
// before invoking. Other tools see their args unmodified.
if call.Function.Name == "run_skill_script" {
    argsJSON = injectSessionID(argsJSON, sessionID)
}
```

`injectSessionID` parses the JSON object, sets (or overrides) `session_id`, and re-serializes. If the model includes its own `session_id`, main-agent's value wins — we don't trust the model to route workspaces safely.

Alternative considered: introduce a richer tool interface (`ToolWithSession` or similar) that Tools can opt into. Rejected — adds typed machinery for exactly one caller; the args-injection trick is surgical and localized to main-agent.

### Decision 7: Register `run_skill_script` after `current_time`, before MCP

Registration order drives base-toolset order drives schema ordering drives LLM request-body stability. Inserting `run_skill_script` after `current_time` keeps the house style and matches the specification update: `list_skills, read_skill, read_skill_file, current_time, run_skill_script, <mcp>`.

Alternative considered: register before `current_time` (adjacent to the other skill tools). Rejected — `current_time` is the catch-all utility, `run_skill_script` is skill-specific; keeping skill-related tools grouped feels clearer. This is a minor preference and could go either way; once set, the spec scenarios pin it.

### Decision 8: Compose: main-agent gains `depends_on: [exec]` with lazy connect

```yaml
main-agent:
  depends_on:
    valkey:
      condition: service_healthy
    llm-broker:
      condition: service_started
    memory-service:
      condition: service_started
    exec:
      condition: service_started
  environment:
    - EXEC_ADDR=http://exec:8085
    - EXEC_MAX_TIMEOUT_S=120
```

`service_started` is sufficient; `service_healthy` would require an exec healthcheck, which is a separate hardening. Lazy connect means main-agent can start even if exec is slow to warm up — the first `run_skill_script` call pays the fail-fast cost and retries are model-driven.

Alternative considered: wait for `exec` to return 200 on `/v1/health` before main-agent accepts its first chat request. Rejected — the wait would serialize agent readiness on exec's Chromium download (~30 s cold boot), which penalizes every main-agent startup even for sessions that never touch scripts.

## Risks / Trade-offs

- **Risk:** Model calls `run_skill_script` before exec is ready. First call returns an error envelope; the model likely retries; the second call succeeds. → Accepted. The window is ~30s at cold boot; it does not recur once exec's prewarm completes.

- **Risk:** HTTP client timeout mismatch between main-agent and exec. If exec is configured with a larger `EXEC_MAX_TIMEOUT_S` than main-agent knows about, long-running scripts could time out on the client side prematurely, abandoning an in-flight subprocess on the exec side. → Mitigation: both services read the same env. Document that they must be set consistently. Compose defaults both to 120.

- **Risk:** Injecting `session_id` into argsJSON mutates what the model sent. If a model deliberately passes a different `session_id` for some reason, main-agent overrides it. → Accepted. The model has no legitimate reason to set a cross-session workspace; overriding prevents misuse.

- **Risk:** exec envelope could grow past 32 KB if stdout + stderr + outputs + metadata all hit their maxima. Main-agent would store it in the tool-result message which bloats the next LLM request. → Mitigation: the envelope is bounded by exec's caps (16 KB stdout + 8 KB stderr + small metadata). 32 KB is the comfortable upper bound. If operators raise exec's caps, they should expect per-request context growth.

- **Risk:** A network partition between main-agent and exec produces hanging goroutines. → Mitigation: HTTP client timeout + request context deadline. Goroutine per tool call is bounded; no pool to leak.

- **Risk:** `run_skill_script` tool being in the base set means every LLM request carries its schema, costing tokens per turn even when the skill flow doesn't use scripts. → Accepted. The schema is ~200 bytes; the cost is small relative to the value of the tool being reachable without first activating a skill. A skill with `allowed-tools: []` still sees base (per change 2) and can use scripts from any skill.

- **Trade-off:** Main-agent's `Invoke` loop grows slightly (session-id plumbing + the args-injection special case). Localized to `invokeOrGate` and one helper function; no change to the tool registry's abstraction.

- **Trade-off:** No `Install` or `Health` tool surfaced to the model. Operators use those endpoints directly; the model only ever needs `Run`. Deliberately narrowing the model-facing surface keeps the tool manifest short.

## Migration Plan

No data migration needed. Deployment order on a cluster already running the earlier changes:

1. Pull and rebuild `main-agent` with the new code.
2. `docker-compose up -d` brings up `main-agent` behind the now-required `exec` dependency. Compose resolves ordering automatically.
3. Model requests that call `run_skill_script` start working immediately. Requests that don't touch scripts continue to work unchanged.
4. Rollback: revert the main-agent commit. `exec` itself is untouched by rollback — it stays running, just idle.

Operational notes:
- `EXEC_ADDR` must be set on main-agent (default `http://exec:8085`); compose handles this automatically.
- `EXEC_MAX_TIMEOUT_S` on main-agent should match exec's setting; compose pins both to 120.
- No new logs appear on main-agent; the existing `tool_invoked` line covers `run_skill_script` with `tool_name=run_skill_script` as a matter of course.

## Open Questions

- Should the tool's description warn the model about install latency on first use for large skills? Likely yes — a model that doesn't know the first call can take 30 s may time out internally and report failure prematurely. The proposed description says "First invocation for a skill may include dependency install latency; subsequent calls reuse the cached venv." Revisit if the model still struggles.
- Do we want the tool to hide `workspace_dir` paths that point outside any readable surface? Today main-agent has no tool to read workspace files; exposing the path is informational. When/if a `read_run_output` tool lands, this becomes a live question. For now, we expose the path.
- Should main-agent expose a way to *reset* a session's workspace (delete `/workspace/<session_id>`)? Not in v1. Retention handles it on a 1h timer.
- If exec is deployed with `EXEC_NETWORK_DEFAULT=deny`, run calls for any skill return a 400. Main-agent's error envelope says so, but the model might not know why. Low-priority UX concern; operators who set `deny` know what they're doing.
