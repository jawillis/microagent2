## Context

This change adds arbitrary shell execution to the `exec` service so agents can iteratively author skills. It reuses everything change 3 built — Docker image, uv, non-root UID 1500, cap-drop, env-from-scratch, process-group-kill-on-timeout, output truncation with full-fidelity fallback files — and changes two things about the workspace model:

1. Storage is **session-persistent** (not per-invocation) so files the agent wrote in turn N are visible in turn N+1.
2. Reclamation is **touch-based** rather than completion-based so an actively-iterating agent isn't evicted mid-thought.

Everything else — transport, sandboxing, network policy, output envelope shape — is the same pattern as `/v1/run`.

Current state of the relevant surface:

- `internal/exec/runner.go` handles `/v1/run`: per-invocation `/workspace/<session>/<invocation>/`, finalized with `.metadata.json`, GC'd after retention expires.
- `internal/exec/workspace.go` and `internal/exec/gc.go` own the per-invocation workspace lifecycle.
- `internal/exec/server.go` wires HTTP endpoints to their handlers.
- `internal/tools/run_skill_script.go` is the template for a built-in that wraps an exec endpoint. Error classification (`exec unavailable` / `exec returned N` / `deadline exceeded`) is already solved there.
- `cmd/main-agent/main.go`'s `invokeOrGate` already injects `session_id` for `run_skill_script` and can be generalized to a whitelist.

Constraints:

- Must not regress `/v1/run`. That endpoint's workspace layout, retention rules, and output envelope stay exactly as they are.
- No new secrets or mounts that widen the attack surface beyond what's documented in `docs/skills-runtime-design.md` §5.
- No Go modules added; stdlib only. Mutex handling reuses the `sync.Mutex` + `sync.Map` pattern from `Installer`.

## Goals / Non-Goals

**Goals:**

- Land `POST /v1/bash` on exec with `{command, session_id, timeout_s?}` request and a response envelope modeled on `/v1/run`.
- Session-persistent `/sandbox/<sanitized_session>/` created lazily on first bash call for a session.
- Touch-based retention: every command updates a `.last_access` marker; background GC sweeps sessions older than `EXEC_SANDBOX_RETENTION_MINUTES` (default 60).
- `/sandbox` is a new tmpfs, independent of `/workspace`. Size cap `EXEC_SANDBOX_TMPFS_SIZE` default 128m.
- Per-session mutex inside exec serializes commands for one session; different sessions run in parallel.
- New built-in tool `bash(command, timeout_s?)` on main-agent. Joins the base toolset in registration order after `run_skill_script`.
- `injectSessionID` in main-agent generalizes from a single-tool special case to a set of tools that need session-scoped routing (`run_skill_script`, now also `bash`).
- Subprocess env is constructed from scratch per command: `PATH`, `HOME=/sandbox/<session>/`, `LANG=C.UTF-8`, `PYTHONUNBUFFERED=1`. `WORKSPACE_DIR` is deliberately NOT set (this is the sandbox path, not the per-invocation path).
- `/skills` is NOT visible to bash subprocesses. The exec container already has it mounted, but the subprocess CWD is `/sandbox/<session>/` and nothing in the env points at `/skills`. Agents that want to inspect an existing skill use `read_skill_file`, not `cat /skills/...`.
- Network policy inherited from the existing config. `EXEC_NETWORK_DEFAULT` and `EXEC_NETWORK_DENY_SKILLS` apply; however, `bash` calls are NOT scoped to a skill, so the deny-list-by-skill mechanism doesn't apply. Design decision below.
- Observability: structured `exec_bash_started`, `exec_bash_finished`, `exec_bash_rejected`, `exec_sandbox_gc` log lines.

**Non-Goals:**

- Publishing from `/sandbox/` into `/skills/`. Operators copy drafts manually. A promotion mechanism is a separate change with its own threat model.
- Multi-session shared workspaces, or "fork a session". Each session has its own `/sandbox/<session>/`.
- Interactive TTY, ANSI escape handling, PTY allocation. Commands are one-shot: stdin string in, stdout/stderr buffer out, exit code.
- Persistent environment variables between commands. Each `sh -c` is a fresh shell; export from one command doesn't survive to the next. Agents that want persistence write to files.
- Concurrent commands within the same session. Per-session mutex serializes; a second call waits for the first.
- `$PATH` customization per skill. One `PATH` value for all bash calls: `/usr/local/bin:/usr/bin:/bin` (plus whatever the agent builds into its own venv under `/sandbox/<session>/`).
- Reading from the session's persistent workspace via a dedicated tool. Everything is done through bash (`cat`, `ls`, etc.). If this turns out to be awkward, a `read_sandbox_file` tool can come later.
- Session lifecycle signals from main-agent. Today main-agent has no "session ended" hook; retention is touch-based and the system is eventually consistent.
- Per-command network policy overrides. The operator either has `EXEC_NETWORK_DEFAULT=allow` (bash has network) or `deny` (bash has no network). No in-between for bash.

## Decisions

### Decision 1: Separate `/sandbox/` mount, not `/workspace/<session>/`

Per-invocation workspaces live at `/workspace/<session>/<invocation>/` and are reclaimed 60 minutes after *completion* (per `WorkspaceMetadata.EndedAt`). The bash sandbox needs session-scoped persistence with touch-based eviction — different lifecycle, different retention driver.

We could technically co-locate them under `/workspace/<session>/` if the GC understood both retention modes, but that couples two unrelated semantics into one code path. Separating to `/sandbox/<session>/` keeps each GC loop simple and makes the boundary legible: `/workspace/` is exec's per-invocation scratchpad for `run_skill_script`; `/sandbox/` is the agent's own development area.

Alternative considered: single tmpfs with prefix-based separation. Rejected — two tmpfs declarations in compose are trivial, and the independent size caps let operators tune them separately (`/workspace` tends to hold big script outputs briefly; `/sandbox` holds persistent dev state).

### Decision 2: Touch-based retention via `.last_access` file

Each `/sandbox/<session>/` gets a hidden `.last_access` file whose modification time is bumped on every bash call for that session. The GC loop walks `/sandbox/`, stats `.last_access` per child directory, and removes directories whose mtime is older than `EXEC_SANDBOX_RETENTION_MINUTES` ago.

Why a file rather than directory mtime or in-memory state:
- Directory mtime in tmpfs isn't reliably updated by file reads/writes inside the directory.
- In-memory state would be lost across exec restarts, causing early eviction or inadvertent retention.
- A file we explicitly touch is simple, survives restarts, and is inspectable from inside the sandbox if the agent wants to know how much time it has left (though we don't expose that as a tool).

Alternative considered: `.last_access` as an ISO-8601 string (content-based). Rejected — mtime is what the filesystem already tracks for us; parsing a timestamp is extra code for no benefit.

### Decision 3: Per-session mutex inside exec

`BashRunner` holds a `sync.Map` of `*sync.Mutex` keyed by session id. Each command acquires the mutex for its session before spawning the subprocess. This prevents two parallel bash calls for the same session from racing on filesystem state (e.g., one writing a file the other expects).

Different sessions run in parallel — the mutex is per session, not global. Under realistic load this is fine: a single chat session won't issue concurrent tool calls, and different sessions are independent.

Alternative considered: no mutex, let the agent deal with concurrency. Rejected — bash commands that mutate files are non-commutative, and the ordering guarantee is cheap to provide.

### Decision 4: No `/skills` access from the sandbox

The exec container has `/skills` bind-mounted (for `run_skill_script`). The bash subprocess env doesn't mention it, and the CWD is `/sandbox/<session>/`, but a process that types `cat /skills/code-review/SKILL.md` would still see the contents because the filesystem namespace is shared.

Options for enforcing "no /skills":
1. Accept the read-only peek. The agent can `cat` existing skill files but can't modify them (mount is `:ro`).
2. Run bash in a chroot or mount namespace that hides `/skills`. Requires capabilities we've dropped.
3. Two separate exec containers (one for `/v1/run` with `/skills`, one for `/v1/bash` without). Operationally heavier.

**Chosen**: Option 1. The agent can inspect `/skills/` read-only via bash. This matches what `read_skill_file` already exposes through the tool interface, so we're not widening the data surface — just the access path. Documented explicitly so nobody assumes `/skills` is unreachable from bash.

The separation in `docs/skills-runtime-design.md` §5 was about *writability* of `/skills`, which remains enforced by the mount being `:ro`. Readability from the sandbox is a design concession we're making deliberately.

Alternative considered: chroot the bash subprocess. Deferred — requires privileged operations the container doesn't have; `unshare` with user namespaces is a rabbit hole we won't explore in v1.

### Decision 5: Shell is `sh -c "<command>"`, not a full `bash` binary

Debian slim ships `/bin/sh` (dash on recent versions) but not full GNU bash. Installing bash adds ~1-2 MB and a handful of extra dependencies we don't need. `sh -c "command"` covers pipes, redirects, `&&`/`||`, env-var assignments, heredocs — the vast majority of what agents will write.

If an agent explicitly needs bashisms (arrays, `[[`, process substitution), it can invoke a script file with `#!/bin/bash` — but for v1 there's no bash binary and that would fail. Document the limitation; revisit if agents routinely bump against it.

Alternative considered: install bash in the runtime image. Rejected for v1 to keep image size stable; easy to add later with one line in the Dockerfile if demand is real.

### Decision 6: No `outputs` array in the bash envelope

`/v1/run` auto-detects artifacts in the workspace top level and returns a MIME-typed list. For bash, artifacts accumulate across calls in the session's sandbox. Auto-detecting "what's new since the last call" is ambiguous (before-snapshot? creation-time filter? file-hash diff?); auto-detecting "everything currently in `/sandbox/<session>/`" quickly becomes a lot of files.

Agents that want to know what's in the sandbox run `ls -la` themselves. Agents that want to read a specific file use `cat`. The tool call is free from the model's perspective; we don't need to pre-compute an outputs list.

Alternative considered: return the top-level listing every time, capped to N entries. Rejected — it'd encourage the model to skip explicit `ls` calls and rely on the envelope; if the listing ever exceeds the cap, the model is blind without warning.

### Decision 7: Session_id injection generalizes to a whitelist

`cmd/main-agent/main.go` currently special-cases `run_skill_script` in `invokeOrGate` to inject `session_id`. This change adds `bash` to the same treatment. Rather than grow an `if` for each tool, introduce a small set:

```go
var sessionScopedTools = map[string]struct{}{
    "run_skill_script": {},
    "bash":             {},
}
```

and check membership. The `injectSessionID` function is unchanged; only the gate in `invokeOrGate` changes.

Future session-scoped tools land in this map. When the set stabilizes, it could move to a convention on the `Tool` interface (`ToolWithSession` or a flag in the schema), but a map is sufficient for the second entry.

### Decision 8: Bash tool schema mirrors run_skill_script's shape

```json
{
  "type": "function",
  "function": {
    "name": "bash",
    "description": "Run a shell command in a per-session sandbox. Files persist across calls within the same session until 60 minutes of inactivity, then are reclaimed. No access to /skills (use read_skill_file for that). Network follows operator policy. Timeout applies per command.",
    "parameters": {
      "type": "object",
      "properties": {
        "command":   {"type": "string",  "description": "Shell command (run as sh -c)"},
        "timeout_s": {"type": "integer", "description": "Per-command deadline in seconds; capped server-side"}
      },
      "required": ["command"]
    }
  }
}
```

Important phrasings in the description:

- "Files persist across calls within the same session until 60 minutes of inactivity" — prevents the agent from assuming `/tmp`-style ephemerality and then being surprised when state appears from a prior call.
- "then are reclaimed" — tells the agent not to rely on files over days/weeks.
- "No access to /skills" — steers the agent toward `read_skill_file` for reading existing skill artifacts.
- "Network follows operator policy" — honest; the agent may find `curl` hangs if the operator set `deny`.

### Decision 9: Envelope shape

```json
{
  "exit_code": 0,
  "stdout": "hello\n",
  "stdout_truncated": false,
  "stderr": "",
  "stderr_truncated": false,
  "sandbox_dir": "/sandbox/sess-abc/",
  "duration_ms": 12,
  "timed_out": false
}
```

Fields:
- `exit_code`: subprocess exit, `-1` on timeout (matches `/v1/run`).
- `stdout` / `stderr`: inline caps identical to `/v1/run` (`EXEC_STDOUT_CAP_BYTES` / `EXEC_STDERR_CAP_BYTES`).
- `stdout_truncated` / `stderr_truncated`: boolean flags; full streams at `/sandbox/<session>/.stdout` and `.stderr` (overwritten on each call).
- `sandbox_dir`: the working directory, for the agent to reference when it wants to `cd` or mention paths.
- `duration_ms`: wall-clock run time.
- `timed_out`: true when the deadline fired.

No `outputs`. No `install_duration_ms` (there's no install phase for bash). No `workspace_dir` — we say `sandbox_dir` to distinguish from `/v1/run`'s per-invocation dir.

## Risks / Trade-offs

- **Risk:** Prompt-injection-reachable arbitrary code execution. The sandbox containment is real (non-root, caps dropped, env from scratch, tmpfs-bounded storage) but the agent can now execute whatever a Python/shell script can do with no privs. → Mitigation: document the threat-model delta. Trust boundary is still "operator reviews skills at PR time" for `run_skill_script`; for `bash` it's "operator trusts the agent + the prompt source." If the deployment serves untrusted prompts, the operator should set `EXEC_NETWORK_DEFAULT=deny` to cap the blast radius.

- **Risk:** Sandbox tmpfs exhaustion from an agent that writes a lot. The 128m default is modest. → Mitigation: configurable via `EXEC_SANDBOX_TMPFS_SIZE`; commands that hit the cap return a structured error envelope (same `workspace_full` behavior as `/v1/run`); the agent can rm and retry.

- **Risk:** `.last_access` file gets deleted by the agent, making GC think the session is fresh forever. → Mitigation: GC also falls back to directory mtime when `.last_access` is absent, and touches it on every command before returning. An agent that deletes `.last_access` immediately after a command gets it recreated on the next call. If the session has no further calls, the directory mtime still ages out.

- **Risk:** Per-session mutex starves a session if a single bash command hangs up to the timeout (2min default). → Accepted. The timeout is a hard ceiling; worst case is one session is paused for 2 minutes waiting on a runaway. Not a production-scale concern for agent authoring workloads.

- **Risk:** Agent uses `sh`-only syntax assuming bash. → Accepted. If an agent types `[[ -f foo ]]`, it gets `sh: 1: [[: not found`. The model reads the error and adapts; we document the shell in the tool description as "sh -c" so this is clear.

- **Risk:** `/skills` is readable from bash. An agent reading `SKILL.md` via bash would get text that `read_skill_file` would also serve — so the data surface is the same — but the path exposes more than a tool-scoped read (it can `ls /skills/`, see all skill names). → Accepted. `list_skills` already exposes the same info via its tool; no data is revealed via bash that isn't already reachable via tools.

- **Trade-off:** No publishing mechanism to `/skills/`. Drafts live in `/sandbox/<session>/` only. An agent that authors a finished skill can't "commit" it — an operator has to `docker cp` or similar. Deliberate: publishing changes the threat model (agent-written code becomes authoritative for future sessions) and deserves its own change. This one keeps the blast radius contained to the current session's sandbox.

- **Trade-off:** No `outputs` auto-detection. Agent has to `ls` to discover its own files. Tiny model-driven overhead; less magic behavior.

## Migration Plan

- Build and redeploy: `docker-compose up -d --build exec main-agent`. Main-agent picks up the new tool; exec picks up the new endpoint and GC loop.
- New tmpfs mount on exec: operators who override the compose file need to add `/sandbox` themselves. Default compose handles it.
- No config migration: new env vars have sensible defaults.
- Rollback: revert the main-agent commit; the `exec` service's new endpoint becomes unreachable (no caller) but remains alive and harmless. Revert the exec commit to fully remove.

Operational notes:
- `EXEC_SANDBOX_RETENTION_MINUTES` (default 60) tunes how long an idle session's files survive.
- `EXEC_SANDBOX_TMPFS_SIZE` (compose-level, default 128m) caps total `/sandbox` usage across all sessions.
- `/sandbox` tmpfs does not persist across container restarts. Agents mid-session lose their scratch state on exec redeploys. Acceptable for a dev sandbox; if persistence is ever needed, it's a separate change.

## Open Questions

- Should the tool description include a hint like "use `ls` to discover what you've already created in this session"? Worth a line; small models sometimes forget.
- If the sandbox accumulates a lot of orphaned sessions (agents that open a session, run one command, never return), will 60 minutes of retention churn enough tmpfs that it matters? Probably not for a single-host dev setup; flag for operators on larger deployments.
- Agent-driven cleanup — should we expose a `clear_sandbox` tool for the agent to explicitly reclaim its own sandbox? Not in v1; the touch-based GC handles it after inactivity. Add if agents start asking to clean up.
- Do we want a `bash_logs` tool or similar to retrieve the full `.stdout`/`.stderr` from a prior command? Not today — the file persists in the sandbox and the agent can `cat .stdout` from a subsequent bash call. If that pattern turns out to be awkward, promote to a dedicated tool later.
