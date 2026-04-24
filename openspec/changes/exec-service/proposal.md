## Why

The Anthropic agent-skills format ships executable helper scripts (Python under `scripts/`, sometimes shell) alongside each skill. microagent2 cannot run them today — there is no sandboxed runtime, no per-skill dependency management, no way to spawn a subprocess with resource caps and output bounds. Change 4 of the `docs/skills-runtime-design.md` sequence (`skills-script-execution`) needs this primitive to wire the `run_skill_script` tool. This change lands the execution primitive as a general-purpose service so that skills are its first consumer but not its only one — `retro-agent` analysis scripts, curiosity-driven research runs, and self-improvement framework evals are all plausible later consumers of the same HTTP API. Running code for the agent is not a skill-specific concern; the name and shape reflect that.

## What Changes

- New Go service at `cmd/exec/` consumed over HTTP. Endpoints: `POST /v1/run`, `POST /v1/install`, `GET /v1/health`.
- New container image (`deploy/Dockerfile.exec`) bundling the Go binary on top of a Python-capable base: `python3`, `uv`, `bash`, `git`, `curl`, `jq`, Playwright browser dependencies (Chromium-only for v1), a non-root user.
- Long-lived container service in `docker-compose.yml`. Main-agent reaches it over the `microagent` network by hostname `exec:8085` (port configurable). This change does not wire main-agent to call it — that's change 4.
- Mounts: `/skills` read-only (the project's `./skills` directory), `/workspace` tmpfs-backed writable, `/cache` persistent volume for per-skill venvs and Playwright browser caches.
- Hybrid dependency install per `docs/skills-runtime-design.md` §7: at container boot, any skill declaring `x-microagent.prewarm: true` in its SKILL.md frontmatter has its `scripts/requirements.txt` installed into `/cache/<skill>/venv` via `uv`. Other skills install lazily on first script run. Install failures mark the skill's deps as failed but do not crash the service; subsequent `run` calls for that skill return a structured error.
- HTTP API contract per `docs/skills-runtime-design.md` §6:
  - `/v1/run` spawns a subprocess for `scripts/<path>` within a specified skill, with working directory set to the skill root, a fresh per-invocation workspace directory injected as `WORKSPACE_DIR`, and a bounded execution deadline.
  - Response envelope includes inline stdout/stderr capped at (by default) 16 KB / 8 KB, truncation flags, a workspace path, detected binary outputs listed as paths (no inline base64 in v1), `exit_code`, `duration_ms`, `timed_out`.
  - `/v1/install` triggers an explicit install for one skill; useful for operator-run warm-ups and as the idempotent target for prewarm.
  - `/v1/health` reports which skills are prewarmed, which have failed installs, and a simple `ok|starting` top-level status.
- Security invariants per `docs/skills-runtime-design.md` §5.2: runner process inside the container runs as a non-root UID with dropped capabilities. Network policy is operator-declared in `exec` config — default allow, with a per-skill deny override list. No main-agent env or secrets are inherited; `exec`'s process env contains only what its config explicitly whitelists.
- Workspace lifecycle per `docs/skills-runtime-design.md` §6.4: per-invocation `/workspace/<session>/<invocation>/` directories with a 1-hour retention window. A background GC goroutine sweeps expired workspaces. Cross-invocation caches (browser binaries, venvs) live at `/cache/<skill>/` and are managed only by install.
- Binary artifact handling v1: scripts that produce non-text files have them listed by path in the response's `outputs` array with detected MIME types. No base64 inlining, no vision plumbing — those are deferred per `docs/skills-runtime-design.md` §9.
- Observability: structured `exec_run_started`, `exec_run_finished`, `exec_install_started`, `exec_install_finished`, `exec_workspace_gc` log lines. No metrics endpoint in v1.

## Capabilities

### New Capabilities

- `code-execution`: the HTTP-driven sandboxed subprocess runner, its request/response contract, the install + prewarm lifecycle, workspace isolation and GC, and the security invariants the runner enforces. Consumed in change 4 by main-agent's new `run_skill_script` tool, but framed as a general primitive.

### Modified Capabilities

(none — this change is additive and introduces its own capability. Change 4 will modify `tool-invocation` to add `run_skill_script`.)

## Impact

- Code:
  - New `cmd/exec/main.go` — HTTP server, graceful shutdown, lifecycle.
  - New `internal/exec/` package with sub-areas: `server.go` (HTTP handlers), `runner.go` (subprocess spawn + output capture), `install.go` (uv-backed dep management), `workspace.go` (per-invocation directory management + GC), `skillsmount.go` (read-only SKILL.md scan for prewarm detection), `config.go` (env-driven settings), `errors.go` (error sentinels).
  - `deploy/Dockerfile.exec` — multi-stage; build stage compiles the Go binary, runtime stage has Python 3.12, uv, bash, git, curl, jq, Chromium + Playwright dependencies, non-root `exec` user.
  - `docker-compose.yml` — new `exec` service, volume for `/cache`, network entry.
- Specs: one new capability, `code-execution`.
- Dependencies: uv binary baked into container image; no new Go modules unless the HTTP framework choice differs from `net/http` stdlib (design says stdlib). Playwright browser install is part of container build, not go.mod.
- Operational surface: one new long-lived container. Default port 8085. Service does not register with the agent-registry (it is not an agent) and does not participate in Valkey messaging (it is a sync HTTP service).
- Breaking changes: none. No existing service consumes `exec` yet. Change 4 will add main-agent as its first consumer.
- What this does NOT do: add `run_skill_script` tool to main-agent's registry (change 4); plumb vision for image outputs (deferred); support non-Python dependency managers (deferred); register with the agent-registry (it's a service, not an agent); expose metrics or a dashboard panel (deferred).
