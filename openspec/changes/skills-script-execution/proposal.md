## Why

The `exec` service (change 3) is deployed and verified but has zero consumers. This change closes the last gap in the Anthropic agent-skills runtime parity roadmap (`docs/skills-runtime-design.md`) by wiring main-agent's tool loop to it. Skills that ship scripts — `mcp-builder`, `webapp-testing`, `claude-api`, and anything an operator drops into `skills/` with an Anthropic-format layout — become actually executable by the model through a new `run_skill_script` built-in tool. Until this lands, exec is a service the architecture promises but no agent can reach.

## What Changes

- New HTTP client package `internal/execclient/` mirroring the shape of `internal/memoryclient/`. Exposes `Run(ctx, req) (*RunResponse, error)` and thin wrappers for `Install` and `Health`. Honors `EXEC_ADDR` env (default `http://exec:8085`).
- New built-in tool `run_skill_script` in `internal/tools/run_skill_script.go`, registered in `cmd/main-agent/main.go` immediately after `current_time`. Tool schema:
  - `skill` (string, required) — skill name as returned by `list_skills`
  - `script` (string, required) — relative path within the skill (e.g. `scripts/hello.py`)
  - `args` (array of strings, optional) — argv passed to the script
  - `stdin` (string, optional) — piped to the script's stdin
  - `timeout_s` (integer, optional) — capped by exec's server-side `EXEC_MAX_TIMEOUT_S`
- Tool invocation pulls `session_id` from the turn's `ContextAssembledPayload` (available via a narrow widening of `invokeOrGate`'s signature) so per-session workspaces in exec are stable across iterations of the same turn.
- Tool result is the JSON envelope returned by exec verbatim — LLMs consume JSON fluently, and preserving the envelope keeps workspace paths + output metadata available for follow-up reasoning. Error cases (exec down, timeout on the HTTP client, non-200 response) produce a structured `{"error":"..."}` envelope shaped like the other built-ins.
- `run_skill_script` joins the base toolset. Like the other built-ins it is always visible; `allowed-tools` expansion from active skills is additive per `code-execution` semantics (already in place from change 2).
- `docker-compose.yml` grows `depends_on: [exec]` under `main-agent`, plus `EXEC_ADDR=http://exec:8085` in its environment block. `exec` moves earlier in the file so compose parses dependencies cleanly.
- HTTP client timeout is set to `EXEC_MAX_TIMEOUT_S + 10s` (default 130s) so that exec's own timed-out runs have time to return their envelope before the client gives up.
- No main-agent session plumbing changes beyond reading `session_id` from the payload — `sessionskill` and active-skill gating (change 2) already handle the turn context exec needs.
- No changes to `skills-store`, `code-execution`, `agent-runtime`, or any other capability outside `tool-invocation`. The exec service's spec is the authoritative contract for the service; this change just adds the consumer.

## Capabilities

### New Capabilities

(none — the `code-execution` capability was introduced in change 3.)

### Modified Capabilities

- `tool-invocation`: adds the new `run_skill_script` built-in; updates the Base toolset, MCP-additive-to-built-ins, and registration-order requirements to reflect the new fifth built-in. No other requirements change.

## Impact

- Code:
  - New `internal/execclient/execclient.go` + test (HTTP client; `Run`, `Install`, `Health`).
  - New `internal/tools/run_skill_script.go` + test (the tool, consumes execclient).
  - `cmd/main-agent/main.go` — register `run_skill_script`; add `EXEC_ADDR` env; construct and pass an execclient instance to the tool registry.
  - `docker-compose.yml` — main-agent gains `depends_on: [exec]` and `EXEC_ADDR`; exec service stays in place.
- Specs: delta on `tool-invocation` (MODIFIED base toolset + MCP registration + tool invocation logging for the new tool's log fields, ADDED built-in run_skill_script tool).
- Breaking changes: none. The base toolset grows by one tool; skills that previously ran fine continue to run. `allowed-tools` gating still constrains visibility — a skill whose allowed list excludes `run_skill_script` sees it only if it is in the base set (which it will be).
- No new services, no container rebuild. The `exec` image built in change 3 is sufficient.
- Deployment ordering: main-agent now depends on exec, so a clean `docker-compose up` starts exec first. Restarting main-agent without exec healthy fails at startup only if we make the client's first call blocking; design.md opts for a non-blocking lazy-connect pattern to keep main-agent resilient to exec restarts.
