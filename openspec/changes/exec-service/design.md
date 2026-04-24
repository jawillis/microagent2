## Context

This is change 3 of the four-change sequence toward Anthropic agent-skills runtime parity. The cross-cutting contract — activation semantics, security trust model, network policy, output envelope shape, workspace lifecycle — is defined in `docs/skills-runtime-design.md`. This document covers the mechanics of the new `exec` service: its HTTP API, subprocess runner, workspace and cache layout, install phases, sandbox enforcement, failure modes, and container build.

Current state:
- microagent2 has no code-execution capability. Skills that ship scripts (`mcp-builder`, `webapp-testing`, etc.) have their `scripts/` directories on disk but no runtime to invoke them.
- `internal/mcp/` already models external tool invocation over a subprocess protocol (MCP servers are spawned via stdio). That is a useful reference for how microagent2 treats external processes, but MCP servers are long-lived tool providers; `exec` is a request-scoped subprocess runner with a different contract.
- `docker-compose.yml` already has a `microagent` network. All services share the overlay and communicate by hostname.
- The architecture permits multiple `exec` replicas in principle (stateless HTTP), but v1 runs one. Workspaces and caches are on per-container volumes; cross-instance coordination is out of scope.

Constraints:
- Must not inherit secrets. The runner subprocess is a prompt-injection reachable attack surface; no `ANTHROPIC_AUTH_TOKEN`, no Valkey creds, no operator PII of any kind.
- Long-lived container. Cold-start per invocation is not acceptable (`~100 ms` would dominate for small scripts).
- Must isolate workspaces per-invocation. A Playwright run must not see another run's screenshots.
- Must not require main-agent changes. Change 4 wires the consumer; change 3 stands alone.

## Goals / Non-Goals

**Goals:**
- Land a standalone `cmd/exec/` Go service behind an HTTP API (`/v1/run`, `/v1/install`, `/v1/health`).
- Ship a runtime container image with `python3`, `uv`, `bash`, `git`, `curl`, `jq`, and Chromium + Playwright system deps.
- Per-invocation workspace isolation at `/workspace/<session>/<invocation>/`, with a background GC sweeping entries older than the retention window.
- Per-skill dependency cache at `/cache/<skill>/` — a uv-managed virtualenv populated by `/v1/install` (explicit) or lazily by the first `/v1/run` for that skill.
- Prewarm loop at container boot: scan `/skills/*/SKILL.md`, install for any skill whose frontmatter has `x-microagent.prewarm: true`, without blocking `/v1/health` from reporting `starting` → `ok` once the required prewarms finish.
- Structured run response with inline stdout/stderr capped, truncation flags, workspace path, `outputs` array of non-text files detected by MIME, `exit_code`, `duration_ms`, `timed_out`.
- Security invariants: non-root runtime user, `--cap-drop=ALL`, `/skills` mounted read-only, `/workspace` writable only during an invocation's lifetime, `/cache` readable by the runner but only writable by install code paths, no inherited secrets from main-agent.
- Operator-driven network policy: `EXEC_NETWORK_DEFAULT` and `EXEC_NETWORK_DENY_SKILLS` env variables per `docs/skills-runtime-design.md` §5.3.
- Graceful shutdown that cancels in-flight runs and flushes GC.

**Non-Goals:**
- Any main-agent wiring. `run_skill_script` tool, session plumbing, exec HTTP client — all change 4.
- Base64 inlining of binary outputs, image streaming to broker/proxy, multimodal response delivery. `outputs` in the response are paths only. Deferred per `docs/skills-runtime-design.md` §9.
- Granular network allowlists (per-domain, per-port). v1 is binary allow/deny per skill.
- Non-Python dependency managers (`package.json`, `Gemfile`, etc.). Only `requirements.txt` via uv.
- Remote skill installation. Skills are always checked into `./skills/`.
- Per-skill resource cap overrides. Exec-global defaults apply.
- Metrics endpoint, Prometheus scrape, dashboard panel wiring. Observability is structured logs only.
- Multi-instance coordination. One container handles all traffic.
- Authentication on the HTTP endpoints. The service listens on the private `microagent` network only; there is no ingress path from outside the compose project.
- Horizontal autoscaling, pooling of warm subprocesses, or pre-forked interpreter reuse. Each run spawns fresh.
- Persistent audit trail of runs in Valkey or elsewhere. The turn-complete log line already tracks tool invocations from main-agent's side; exec logs cover its own side.

## Decisions

### Decision 1: Stdlib `net/http`, no framework

The HTTP surface is three handlers. A framework adds no value, adds a dependency, and changes the idiom. Stdlib wins. `http.ServeMux` with `HandleFunc` for `/v1/run`, `/v1/install`, `/v1/health`. JSON request/response via `encoding/json`. Request timeouts set per handler via `http.TimeoutHandler`.

Alternative considered: chi or gin. Rejected — neither earns their keep for three endpoints, and consistency with the rest of microagent2 (which uses stdlib HTTP in `internal/gateway/` and elsewhere) is a bigger win.

### Decision 2: Subprocess model — `os/exec` with context timeout, no goroutine pool

Each `/v1/run` call spawns a fresh subprocess via `exec.CommandContext`. The context is derived from the request with a deadline computed as `min(request.timeout_s, EXEC_MAX_TIMEOUT_S)` (global cap defaults to 120s). Cancellation on timeout sends SIGKILL. No worker pool, no subprocess reuse, no pre-warmed interpreter.

Alternative considered: pre-forked Python interpreter pool. Rejected for v1 — the model introduces mutable state between invocations (imports, monkeypatched modules, file descriptors), which is exactly the bleed-through we're trying to prevent. Cold-start cost is acceptable (Python startup is ~50 ms; Go process spawn overhead is trivial relative to the script's own runtime).

Alternative considered: gVisor or similar seccomp sandbox. Deferred — the container + non-root + caps-drop + tmpfs workspace gives defense-in-depth sufficient for v1 given the trust model ("curated skills dir"). gVisor is the v2 lever if an exploit shows up.

### Decision 3: uv for dependency management; per-skill venv cache

`uv pip install -r scripts/requirements.txt --python /cache/<skill>/venv/bin/python` is the install path. Venvs are initialized with `uv venv /cache/<skill>/venv --python 3.12` on first use for that skill. `uv` is ~10× faster than `pip` for resolve + install and produces a lockfile-free deterministic output when called with identical inputs.

Alternative considered: pip + virtualenv directly. Rejected because uv is strictly faster and less chatty. `uv` is a single static binary and is baked into the container image.

Alternative considered: `poetry` or `pdm`. Rejected — `requirements.txt` is the Anthropic agent-skills convention; skills do not ship `pyproject.toml` or poetry lockfiles. Matching the skill format is more important than using the "best" Python tool.

### Decision 4: Install phase gating

- **Prewarm**: on boot, scan `/skills/*/SKILL.md`. Parse frontmatter; if `x-microagent.prewarm: true` under a namespaced top-level key (not just `prewarm:`), enqueue that skill for install. Prewarm happens concurrently across skills (bounded at 4 in-flight to avoid PyPI throttling). Health reports `starting` until prewarms finish, then `ok`.
- **Lazy**: `/v1/run` for a skill whose venv doesn't exist at `/cache/<skill>/venv` triggers install inline with the run. Request waits for install + script; response's `duration_ms` covers both. First-run latency is the cost.
- **Explicit**: `/v1/install` is idempotent. If already installed, returns immediately with `status: "ok"` and the last install's duration. If install is in progress, blocks on the same goroutine (mutex per skill) to avoid double-install races.

Frontmatter extension is namespaced per `docs/skills-runtime-design.md` §7.1:
```yaml
---
name: webapp-testing
description: ...
x-microagent:
  prewarm: true
---
```
Unknown top-level keys (including the `x-microagent` block) are already tolerated by the existing YAML parser in `internal/skills/manifest.go`, but `exec` does not depend on `internal/skills/` — it re-parses SKILL.md frontmatter with a minimal parser to avoid importing the in-process cache. Two simple parsers is cheaper than an import coupling.

### Decision 5: Workspace lifecycle and GC

Workspace layout:
```
/workspace/
  <session_id>/
    <invocation_id>/            <- tmpfs-backed, created per run
      (script-generated files)
      .metadata.json            <- exec-owned: start_ts, script, args
```

- Created immediately before subprocess spawn, `0o755`, owned by the non-root runner UID.
- `WORKSPACE_DIR` env var injected into the subprocess environment pointing at this directory.
- Script CWD is the skill root (`/skills/<skill>/`), not the workspace — matches Anthropic skill conventions (scripts reference siblings like `reference/best.md` by relative path).
- Retained 1h after invocation end (ts in `.metadata.json`). Background goroutine sweeps every 5 minutes, removing workspaces whose `.metadata.json` age exceeds the retention.
- `/workspace` as a whole is a tmpfs with a 64 MB cap (operator-configurable). Exhaustion causes subsequent runs to fail with a structured `workspace_full` error; GC runs immediately on exhaustion.

Alternative considered: per-session workspace (shared across invocations). Rejected — multi-step workflows can read each other's artifacts via the path in the response envelope, so per-invocation gives stronger isolation at no usability cost.

Alternative considered: no retention, wipe immediately on return. Rejected — the model needs to reference outputs by path in follow-up tool calls (e.g. `read_skill_file` extended to read workspace files, or `run_skill_script` that chains). 1h is ample for all realistic follow-ups.

### Decision 6: Output envelope

```json
{
  "exit_code": 0,
  "stdout": "first N KB...",
  "stdout_truncated": false,
  "stderr": "...",
  "stderr_truncated": false,
  "workspace_dir": "/workspace/sess-abc/inv-xyz/",
  "outputs": [
    {"path": "/workspace/sess-abc/inv-xyz/screenshot.png", "mime": "image/png", "bytes": 48291}
  ],
  "duration_ms": 1240,
  "timed_out": false,
  "install_duration_ms": 0
}
```

- Inline stdout/stderr capped per stream: `EXEC_STDOUT_CAP_BYTES` (default 16384), `EXEC_STDERR_CAP_BYTES` (default 8192). Both streams are fully written to `<workspace>/.stdout` and `<workspace>/.stderr` for full retrieval; the inline versions are truncation-prefix-safe (UTF-8 rune boundaries respected).
- Binary detection: for each file at the top level of `workspace_dir` after the run finishes, determine MIME from file extension first (trust the script to name outputs properly), fallback to `http.DetectContentType` for unknowns. Inline only `text/*` and well-known structured text types (`application/json`, `application/xml`, `application/yaml`); everything else is a path-only entry. Hidden files (`.metadata.json`, `.stdout`, `.stderr`) are excluded.
- `install_duration_ms` is populated only for lazy installs that ran during this request; zero otherwise.
- No base64 anywhere. A caller wanting image content can read the file via a side channel (change 4 will likely add workspace file-reading to `read_skill_file`'s scope).

### Decision 7: Sandbox invariants, enforced at container and process level

Container-level (Dockerfile + compose):
- Non-root runtime user `exec` (UID 1500). `USER exec` in Dockerfile so the Go server and the subprocesses it spawns run unprivileged.
- `--cap-drop=ALL` and no `--privileged`. The compose file sets `cap_drop: [ALL]`.
- `/skills` bind-mounted read-only from the project.
- `/workspace` is a tmpfs (`tmpfs: /workspace:size=64m,mode=0755`).
- `/cache` is a named docker volume writable by UID 1500.
- No exposure of the Docker socket.
- No inheritance of host environment variables; compose `environment:` lists exactly `VALKEY_ADDR` (unused — `exec` does not touch Valkey), `EXEC_*` settings, and nothing secret.

Process-level (in `cmd/exec/main.go`):
- The Go server validates every request-supplied path is within `/skills/<skill>` before spawning; it never trusts `skill` names to be safe path components.
- The subprocess environment is *constructed from scratch* per invocation: `PATH=/usr/local/bin:/usr/bin:/bin`, `HOME=<workspace>`, `WORKSPACE_DIR=<workspace>`, `LANG=C.UTF-8`, `PYTHONUNBUFFERED=1`, and the skill's venv `VIRTUAL_ENV` + `PATH` prepend. Nothing from `os.Environ()` is propagated.
- `runtime.LockOSThread` is not used; `exec.Cmd.SysProcAttr` is set with `Setpgid: true` so SIGKILL on context cancel reaches the whole process group (catches forked children like `playwright install`'s helper processes).

### Decision 8: Network policy, v1 simple

Two modes, binary:

```go
// Config resolution
func networkModeFor(skill string, cfg *Config) NetMode {
    for _, denied := range cfg.DenySkills {
        if denied == skill { return NetDeny }
    }
    return cfg.Default // typically NetAllow
}
```

Per `docs/skills-runtime-design.md` §5.3. `EXEC_NETWORK_DEFAULT=allow|deny` and `EXEC_NETWORK_DENY_SKILLS=skill1,skill2,...` env vars control policy. The enforcement lives at container level when we build in a v2: an unshare-based netns drop for deny mode. For v1, deny mode is a documented "this skill refuses to run" marker — the design acknowledges that true network isolation per-invocation needs netns manipulation, which is non-trivial in a rootless container. v1 operator can either allow all or deny specific skills entirely (the `deny` branch returns an immediate error without invoking the script).

Alternative considered: implement per-invocation netns dropping via `unshare -n`. Deferred because (a) it requires capabilities we've dropped and (b) the trust model has the operator reviewing skills before install; the network policy is a defense-in-depth layer, not the primary gate.

### Decision 9: Health reporting

```json
GET /v1/health
{
  "status": "starting" | "ok",
  "prewarmed_skills": ["webapp-testing", "mcp-builder"],
  "failed_installs": [
    {"skill": "broken", "error": "pip install failed: ...", "at": "2026-04-23T15:00:00Z"}
  ],
  "ready": true
}
```

- `status` is `starting` until the initial prewarm sweep finishes (success or failure for each); then `ok`.
- `ready` mirrors `status == "ok"`.
- `prewarmed_skills` is the set of skills whose venv exists at `/cache/<skill>/venv` AND whose install completed successfully (not the set with prewarm frontmatter — that's metadata, not runtime state).
- `failed_installs` is the set of skills that attempted install (prewarm or explicit) and failed; stays populated until a subsequent install succeeds.

### Decision 10: Concurrency model

- HTTP server handles requests concurrently (stdlib default).
- Per-skill install mutex: `sync.Mutex` keyed by skill name, held for the duration of install to serialize concurrent `/v1/run` or `/v1/install` calls for the same skill. Reads of the venv do not take the lock.
- Workspace GC runs in a single goroutine started at service boot, tick every 5 minutes, sweep sequentially.
- Prewarm uses a `golang.org/x/sync/errgroup` with a semaphore of 4 to cap concurrency.
- Graceful shutdown: on SIGTERM, HTTP server stops accepting new connections, in-flight runs run to completion (bounded by their timeouts), GC goroutine exits on context cancel.

## Risks / Trade-offs

- **Risk:** Prompt-injection-reachable script execution. Even with non-root + caps-drop + no-inherited-secrets, a malicious Python script can read `/skills` content, make network calls (default allow), and write to `/workspace`. → Mitigation: trust model is "curated skills dir" per `docs/skills-runtime-design.md` §5.1. Operator reviews skills before install. Defense-in-depth for containment, not prevention. Deny-list network policy is the escape hatch.

- **Risk:** Python is heavyweight; cold starts on lazy install can be 30-120s for large dep trees (Playwright-like). First-run UX will feel slow. → Mitigation: prewarm opt-in is the lever. Document which Anthropic skills should prewarm in `skills/README.md`. Health endpoint shows prewarm state so operators can verify.

- **Risk:** Workspace tmpfs exhaustion under concurrent heavy scripts. 64 MB is modest. → Mitigation: cap is configurable via `EXEC_WORKSPACE_TMPFS_SIZE`. Runs that exceed the cap fail with `workspace_full` + structured error. GC runs on exhaustion as well as on the periodic tick.

- **Risk:** uv installs can fail transiently (PyPI rate-limiting, network blips). Failures are sticky (`failed_installs` list) until a retry succeeds. → Mitigation: `/v1/install` is an operator-retry hook; failures log enough detail for debug; next `/v1/run` retries automatically (no poisoning).

- **Risk:** SIGKILL on process-group leaks Playwright subprocesses or other orphans. → Mitigation: `Setpgid: true` + kill the process group rather than just the direct child. Tested with Playwright in integration smoke.

- **Risk:** Skills author naming conflicts — two skills with scripts sharing absolute paths in a shared cache. → Not applicable: `/cache/<skill>/` namespaces per skill. `<skill>` comes from directory name, which is already unique due to skills-store's one-level scan.

- **Trade-off:** HTTP over Valkey messaging. Sync request/response is natural for a tool call; async would complicate the main-agent tool-loop model. HTTP also gives debuggable curl access. Cost: no natural fit into the existing Valkey observability.

- **Trade-off:** Fresh subprocess per run (no pool). Cold-start cost of ~50-100ms per Python start is paid on every call. Accepted in exchange for strong isolation. If cold-start dominates a future workload, a pool can be added behind the same API.

- **Trade-off:** No authentication on the HTTP endpoints. Relies on the `microagent` compose network being private. If the service were ever exposed beyond the compose project, this would need a bearer-token check or mTLS. Documented as a v1 constraint.

- **Trade-off:** Text-vs-binary inlining heuristic is extension-first, MIME-detection-fallback. A `.txt` file with binary content would be inlined; a `.md` file with image bytes would be inlined. Accepted — the "script names its outputs honestly" contract is reasonable and the false-inline cost is bounded by the stdout cap.

## Migration Plan

No data migration required. This is a new service.

Deployment order:
1. Merge and build the `exec` image.
2. Pull/build on deployment host.
3. `docker-compose up -d exec` — main-agent and other services are unaffected.
4. Verify `curl http://exec:8085/v1/health` from inside the compose network returns `ok` (or `starting` then `ok`).
5. Proceed to change 4 (`skills-script-execution`) at leisure; until then, `exec` is idle and costs only its idle memory/CPU footprint.

Rollback: `docker-compose stop exec && docker-compose rm -f exec`. No downstream consumer (until change 4), so removal is clean. `/cache` volume can be preserved or wiped as the operator prefers.

Operational notes:
- `EXEC_PORT` defaults to 8085. Bound to the `microagent` network only; no host-port mapping by default.
- `EXEC_WORKSPACE_RETENTION_MINUTES` default 60.
- `EXEC_GC_INTERVAL_MINUTES` default 5.
- `EXEC_MAX_TIMEOUT_S` default 120. Per-request `timeout_s` is clamped to this.
- `EXEC_STDOUT_CAP_BYTES` default 16384, `EXEC_STDERR_CAP_BYTES` default 8192.
- `EXEC_WORKSPACE_TMPFS_SIZE` default 64m (compose-level, not server-level — tmpfs is mounted at container start).
- `EXEC_NETWORK_DEFAULT` default `allow`, `EXEC_NETWORK_DENY_SKILLS` default empty.
- `EXEC_PREWARM_CONCURRENCY` default 4.
- `PYTHON_VERSION` for venv creation default `3.12` (container provides exactly this; mismatch is not supported in v1).

Container runtime portability fixes learned during first deploy:

- **Drop `playwright install --with-deps`.** The `--with-deps` mode invokes
  `apt-get install` against a hardcoded Ubuntu-20.04 package list that
  includes legacy font packages (`ttf-unifont`, `ttf-ubuntu-font-family`) no
  longer present in Debian trixie. The Dockerfile instead curates the apt
  package list directly and runs `python -m playwright install chromium`
  (without deps). Chromium cache is relocated to `/opt/playwright-browsers/`
  via `PLAYWRIGHT_BROWSERS_PATH` so it persists across image rebuilds and
  is independent of the skill venv cache.
- **Tmpfs mount options `uid=`/`gid=` are Docker-engine extensions** that
  Podman rejects ("unknown mount option"). The compose file uses
  `mode=1777` (sticky-world-writable, same as /tmp) so the non-root `exec`
  user can write to `/workspace` regardless of container runtime. This is
  acceptable because the container runs exactly one unprivileged user; the
  sticky bit's delete-protection has no practical effect in a single-user
  container.

## Open Questions

- Do we want a simple `/v1/runs/<id>/output` endpoint for retrieving the full stdout/stderr beyond the inline cap, or do we rely on main-agent reading the files via `read_skill_file` (change 4) once the workspace path is known? The latter is cheaper but couples this service's consumer to a file-read path; the former makes `exec` self-sufficient. Default to the latter for v1; revisit if the dependency feels wrong.
- Should the prewarm scan re-run periodically or only at boot? v1: boot only. New skills added via VCS + operator restart pick up the scan on next boot. Hot-reload of prewarm state is out of scope.
- Python 3.12 is pinned in the image; some skills may want a different interpreter. Out of scope for v1, but worth a follow-up if we hit a skill that requires 3.11 or 3.13.
- The Chromium + Playwright dependency makes the image large (~500-800 MB). Acceptable for a service that will be used regularly but worth noting. Splitting into separate base + playwright layers is a later optimization.
- Does `failed_installs` auto-expire? v1: no. Entry clears only on successful re-install. Risk: operator doesn't see the stale state. Acceptable because `/v1/health` is operator-facing and the failure signal is useful.
