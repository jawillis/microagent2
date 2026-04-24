# exec

Sandboxed HTTP code-execution service. First consumer is the
`skills-script-execution` change (main-agent's `run_skill_script` tool);
later consumers include retro-agent analysis scripts, curiosity-driven
research runs, and self-improvement evaluation harnesses. The contract
lives in `openspec/specs/code-execution/spec.md`; the broader rationale is
in `docs/skills-runtime-design.md`.

## Who calls this service

Main-agent's `run_skill_script` built-in tool (see
`internal/tools/run_skill_script.go`) is the primary client. Operators
curl the endpoints directly only for smoke tests and diagnostics. The
service has no authentication; the `microagent` compose network is the
trust boundary.

## HTTP API

```
POST /v1/run
Request:
  {
    "skill":      "<name>",                  // required; must exist in /skills
    "script":     "scripts/<relative-path>", // required; relative to the skill root
    "args":       ["--flag", "value"],       // optional
    "stdin":      "input for the script",    // optional
    "timeout_s":  30,                        // optional; clamped to EXEC_MAX_TIMEOUT_S
    "session_id": "sess-abc"                 // optional; defaults to "anon"
  }
Response:
  {
    "exit_code": 0,
    "stdout": "...",
    "stdout_truncated": false,
    "stderr": "",
    "stderr_truncated": false,
    "workspace_dir": "/workspace/sess-abc/<invocation-id>",
    "outputs": [{"path": ".../note.txt", "mime": "text/plain", "bytes": 21}],
    "duration_ms": 87,
    "timed_out": false,
    "install_duration_ms": 0
  }

POST /v1/install
Request:  {"skill": "<name>"}
Response: {"status": "ok" | "error", "duration_ms": 1234, "error": "..."}

GET /v1/health
Response:
  {
    "status": "starting" | "ok",
    "ready": true,
    "prewarmed_skills": ["exec-smoke"],
    "failed_installs": []
  }
```

## Running locally (without Docker)

```
SKILLS_DIR=./skills \
  CACHE_DIR=/tmp/exec-cache \
  WORKSPACE_DIR=/tmp/exec-workspace \
  go run ./cmd/exec

# In another shell:
curl -s localhost:8085/v1/health | jq
curl -s -X POST localhost:8085/v1/install -H 'Content-Type: application/json' -d '{"skill":"exec-smoke"}' | jq
curl -s -X POST localhost:8085/v1/run -H 'Content-Type: application/json' \
     -d '{"skill":"exec-smoke","script":"scripts/hello.py","session_id":"demo"}' | jq
```

Requires `uv`, `python3.12`, and `sh` on `PATH`. The Docker image bakes these;
local runs need them installed.

## Configuration

All config is env-driven. Defaults are reasonable for a dev box; production
operators tune via `docker-compose.yml` or the process env.

| Var | Default | Notes |
|---|---|---|
| `EXEC_PORT` | 8085 | HTTP listen port |
| `EXEC_MAX_TIMEOUT_S` | 120 | Global cap on per-request `timeout_s` |
| `EXEC_STDOUT_CAP_BYTES` | 16384 | Inline stdout cap; excess goes to `<workspace>/.stdout` |
| `EXEC_STDERR_CAP_BYTES` | 8192 | Inline stderr cap |
| `EXEC_WORKSPACE_RETENTION_MINUTES` | 60 | How long per-invocation workspaces stay on disk |
| `EXEC_GC_INTERVAL_MINUTES` | 5 | Background GC cadence |
| `EXEC_PREWARM_CONCURRENCY` | 4 | Max in-flight prewarm installs |
| `EXEC_INSTALL_TIMEOUT_S` | 600 | Per-install deadline |
| `EXEC_SHUTDOWN_GRACE_S` | MaxTimeout+5 | SIGTERM → forced-exit window |
| `EXEC_NETWORK_DEFAULT` | allow | `allow` or `deny` |
| `EXEC_NETWORK_DENY_SKILLS` | (empty) | Comma-separated skill names to always deny |
| `SKILLS_DIR` | /skills | Read-only bind-mount of the project's skills |
| `CACHE_DIR` | /cache | Per-skill venv cache; persistent volume |
| `WORKSPACE_DIR` | /workspace | Per-invocation scratch; tmpfs-backed in containers |
| `PYTHON_VERSION` | 3.12 | Interpreter version for uv-managed venvs |
| `UV_BIN` | uv | Resolved via `PATH` |

## Security invariants

- Non-root UID (1500) inside the container.
- `--cap-drop=ALL` at compose level.
- `/skills` is a read-only mount.
- Subprocess environment is constructed from scratch per invocation. No
  `ANTHROPIC_*`, `VALKEY_*`, `HINDSIGHT_*` or other service secrets are
  propagated. Only `PATH`, `HOME` (= workspace), `WORKSPACE_DIR`, `LANG`,
  `PYTHONUNBUFFERED`, and `VIRTUAL_ENV` are set.
- Process group is killed with SIGKILL on deadline; descendants do not
  outlive the invocation.
- Binary artifacts are returned as paths, never inlined as base64.

## Observability

Structured logs on stdout (JSON via `slog`):

- `exec_boot` / `exec_listening`
- `exec_run_started` / `exec_run_finished` / `exec_run_rejected`
- `exec_install_started` / `exec_install_finished`
- `exec_workspace_gc`
- `exec_shutdown_requested` / `exec_exit_clean`

No metrics endpoint in v1.

## Smoke test

See `skills/exec-smoke/SKILL.md` for an operator-only smoke skill and
example curl recipes.
