## Why

The four-change Anthropic agent-skills parity roadmap is complete: the agent can run curated, checked-in skill scripts via `run_skill_script`. What it cannot do is *author* skills. Creating a new skill is an iterative activity — try commands, inspect output, write a script, rerun, debug — and it requires arbitrary command execution in a persistent workspace. Without that, operators must hand-author every skill outside the loop and copy it into `skills/`, which defeats the point of an agentic system that's supposed to grow its own capabilities.

This change adds a `bash` tool that gives the agent full shell access inside the existing `exec` container, with a session-persistent scratch directory. The sandbox inherits every security invariant we already built: non-root UID 1500, all caps dropped, no inherited secrets, tmpfs-backed writable area, network policy honored. It shifts the trust model in one concrete way: the agent is now a code *author*, not just a code *invoker*, which means prompt-injection-reachable arbitrary code execution is live for the first time. The sandbox contains the blast radius to what an unprivileged Python process with no secrets and no `/skills` access can do.

Publishing finished skills from the sandbox to `skills/` is explicitly deferred. Drafts live in the workspace, get reclaimed after an hour of inactivity, and operators copy anything worth keeping by hand. The promotion mechanism is a future change with its own threat model.

## What Changes

- New HTTP endpoint on exec: `POST /v1/bash`. Accepts `{"command": string, "session_id": string, "timeout_s"?: int}`. Runs `sh -c "<command>"` with `Setpgid`, SIGKILL on timeout, the same subprocess env construction as `/v1/run` (from scratch; no inherited secrets).
- CWD for bash commands is `/sandbox/<sanitized_session_id>/`, created lazily on first command for a session.
- New tmpfs mount on exec: `/sandbox` with its own size cap (`EXEC_SANDBOX_TMPFS_SIZE`, default 128m — larger than `/workspace` because agents iterate in here).
- `/skills` is NOT mounted in the bash execution path. The bash tool is for *authoring*, not *invoking* existing skills.
- Session-persistent workspace: every bash command for a given session writes to the same `/sandbox/<session>/` directory. Files created in one call are visible in the next.
- Touch-based retention: every command updates `/sandbox/<session>/.last_access`. A new background GC loop sweeps sessions whose last-access is older than `EXEC_SANDBOX_RETENTION_MINUTES` (default 60).
- Per-session mutex: exec serializes commands within a single session_id so filesystem state mutations don't race. Different sessions run in parallel.
- Output envelope mirrors `/v1/run`: `exit_code`, inline stdout/stderr with truncation flags, full streams at `.stdout`/`.stderr`, `sandbox_dir`, `duration_ms`, `timed_out`. No `outputs` array (no auto-detection of artifacts; `ls` from within the sandbox is how the agent discovers what it made).
- Same resource caps: timeout clamped by `EXEC_MAX_TIMEOUT_S`, stdout/stderr capped by the same envs used by `/v1/run`.
- New built-in tool on main-agent: `bash(command, timeout_s?)`. Joins the base toolset as the 6th built-in. Session id is injected by main-agent; model-supplied values are overridden (same pattern as `run_skill_script`).
- New HTTP client method: `execclient.Bash(ctx, req) (*BashResponse, error)` mirroring `execclient.Run`.
- Compose: no new services. `exec` service grows a second tmpfs mount for `/sandbox` alongside `/workspace`.
- Logging: new `exec_bash_started` and `exec_bash_finished` structured log lines (parallel to `exec_run_*`). New `exec_sandbox_gc` log when the retention sweep runs.

## Capabilities

### New Capabilities

(none — extends `code-execution` and `tool-invocation`.)

### Modified Capabilities

- `code-execution`: adds the `POST /v1/bash` endpoint, the `/sandbox/<session>/` layout, touch-based retention GC, and the bash-specific envelope shape. Existing `/v1/run` contract is unchanged.
- `tool-invocation`: adds the `bash` built-in tool; updates the Base toolset and MCP-additive-to-built-ins requirements to include it.

## Impact

- Code:
  - `internal/exec/bash.go` — new `BashRunner` (parallel to `Runner`) handling session-persistent workspaces.
  - `internal/exec/sandbox.go` — session-workspace allocator + touch handling + separate GC loop.
  - `internal/exec/server.go` — new `handleBash` HTTP handler wired to the existing mux.
  - `internal/exec/types.go` — `BashRequest` / `BashResponse` types.
  - `internal/exec/config.go` — new `EXEC_SANDBOX_RETENTION_MINUTES`, `EXEC_SANDBOX_TMPFS_SIZE`, `EXEC_SANDBOX_DIR` settings.
  - `cmd/exec/main.go` — start the sandbox GC goroutine alongside the existing workspace GC.
  - `internal/execclient/execclient.go` — `Bash(ctx, *BashRequest) (*BashResponse, error)`.
  - `internal/tools/bash.go` — new built-in tool; envelope classifier reused from `run_skill_script`.
  - `cmd/main-agent/main.go` — register `bash` after `run_skill_script`; extend `injectSessionID` to also rewrite bash tool calls (or generalize to a whitelist).
  - `docker-compose.yml` — add `/sandbox` tmpfs mount on exec.
- Specs: deltas on `code-execution` and `tool-invocation`.
- Breaking changes: none. Base toolset grows by one; existing tools unchanged.
- Operational: one new env var set (`EXEC_SANDBOX_RETENTION_MINUTES`), one new tmpfs mount. Container image is unchanged — the binary does the rest.
- Container image size: unchanged (no new apt packages; we already ship sh).
- Threat-model delta worth naming in the commit message and docs: this is the capability that turns "agent uses tools" into "agent writes and runs code." Sandbox invariants still hold (non-root, caps dropped, no secrets, isolated fs), but the prompt-injection → arbitrary code path is now live.
