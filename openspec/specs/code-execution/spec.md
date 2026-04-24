## ADDED Requirements

### Requirement: HTTP execution API
The `exec` service SHALL expose an HTTP API at the service-level port (default 8085) with three endpoints: `POST /v1/run`, `POST /v1/install`, `GET /v1/health`. The service SHALL listen only on the `microagent` compose network (no host-port mapping by default). Requests and responses SHALL be JSON-encoded with `Content-Type: application/json`.

#### Scenario: Service listens on the configured port
- **WHEN** the service starts with `EXEC_PORT=8085`
- **THEN** it SHALL accept HTTP connections on port 8085 and reject connections on other ports

#### Scenario: Unknown endpoints return 404
- **WHEN** a request is made to a path other than `/v1/run`, `/v1/install`, or `/v1/health`
- **THEN** the service SHALL respond with HTTP 404 and a JSON body `{"error":"not found"}`

#### Scenario: Wrong HTTP method returns 405
- **WHEN** a GET request is sent to `/v1/run` or `/v1/install`, or a POST request to `/v1/health`
- **THEN** the service SHALL respond with HTTP 405 and a JSON body `{"error":"method not allowed"}`

### Requirement: Run endpoint contract
`POST /v1/run` SHALL accept a JSON body with fields `skill` (string, required), `script` (string, required, relative path within the skill), `args` (array of strings, optional, default empty), `stdin` (string, optional, default empty), `timeout_s` (integer, optional, clamped to `[1, EXEC_MAX_TIMEOUT_S]`), `session_id` (string, optional but recommended — used for workspace scoping; default `"anon"`).

The response SHALL be a JSON object with the following fields:
- `exit_code` (integer): the subprocess exit code, or `-1` when timed out before exit
- `stdout` (string): captured stdout, truncated to `EXEC_STDOUT_CAP_BYTES`
- `stdout_truncated` (boolean): `true` when the cap was hit and the full stdout exists only in the workspace
- `stderr` (string): captured stderr, truncated to `EXEC_STDERR_CAP_BYTES`
- `stderr_truncated` (boolean)
- `workspace_dir` (string): absolute path to the per-invocation workspace directory
- `outputs` (array of objects): non-hidden files at the top level of `workspace_dir` after the run, each with `path`, `mime`, `bytes`
- `duration_ms` (integer): wall-clock run duration not counting any install
- `timed_out` (boolean): `true` when the deadline fired
- `install_duration_ms` (integer): time spent in a lazy install for this run, 0 if no install ran

HTTP response status:
- 200 on any subprocess completion (including non-zero exit codes and timeouts)
- 400 on malformed request body or invalid `skill`/`script` arguments
- 409 on `workspace_full` or similar resource-exhaustion conditions
- 500 only on internal server failures that prevented any attempt to run

#### Scenario: Successful run returns 200 with envelope
- **WHEN** `POST /v1/run` is called with `{"skill":"demo","script":"scripts/hello.py"}` and the script exits 0 after printing `hello`
- **THEN** the response SHALL be 200 with `exit_code: 0`, `stdout: "hello\n"`, `stdout_truncated: false`, `timed_out: false`, `duration_ms > 0`, and a `workspace_dir` that exists

#### Scenario: Unknown skill returns 400
- **WHEN** `POST /v1/run` is called with `{"skill":"nonexistent","script":"anything.py"}`
- **THEN** the response SHALL be 400 with a JSON body containing an `error` field naming the unknown skill

#### Scenario: Script path outside skill rejected
- **WHEN** `POST /v1/run` is called with `{"skill":"demo","script":"../other/scripts/x.py"}`
- **THEN** the response SHALL be 400; no subprocess SHALL be spawned

#### Scenario: Non-regular or non-executable script rejected
- **WHEN** `POST /v1/run` is called with `script` pointing to a directory, a symlink outside the skill root, or a path that does not exist
- **THEN** the response SHALL be 400 with a descriptive error

#### Scenario: Timeout reports timed_out
- **WHEN** a script runs longer than the request's `timeout_s` (or the service-global `EXEC_MAX_TIMEOUT_S` when not set per-request)
- **THEN** the response SHALL have `timed_out: true`, `exit_code: -1`, and the process group SHALL have received SIGKILL

#### Scenario: Stdout exceeds cap signals truncation
- **WHEN** a script writes more than `EXEC_STDOUT_CAP_BYTES` bytes to stdout
- **THEN** the response's `stdout` SHALL be the first N bytes (at a UTF-8 rune boundary), `stdout_truncated` SHALL be `true`, and the full stdout SHALL be written to `<workspace_dir>/.stdout`

#### Scenario: Stderr cap behaves symmetrically to stdout
- **WHEN** a script writes more than `EXEC_STDERR_CAP_BYTES` bytes to stderr
- **THEN** the response's `stderr` SHALL be capped with `stderr_truncated: true` and full contents at `<workspace_dir>/.stderr`

#### Scenario: Args and stdin are passed through
- **WHEN** `POST /v1/run` is called with `args: ["--flag", "value"]` and `stdin: "input text"`
- **THEN** the subprocess SHALL receive those args as `sys.argv[1:]` and the stdin string on its stdin file descriptor

#### Scenario: Working directory is the skill root
- **WHEN** a script runs via `/v1/run` for `skill="foo"`
- **THEN** the subprocess's CWD SHALL be the absolute path of `/skills/foo/`

#### Scenario: Workspace env var injected
- **WHEN** a script runs via `/v1/run`
- **THEN** the subprocess environment SHALL include `WORKSPACE_DIR` pointing to the per-invocation workspace directory

### Requirement: Outputs detection
After a run completes, the service SHALL enumerate non-hidden files at the top level of `workspace_dir`, detect MIME type for each, and include them in the response's `outputs` array. Hidden files (names beginning with `.`) and files inside subdirectories SHALL be excluded from the array. MIME detection SHALL use the file extension first with a fallback to content sniffing via `http.DetectContentType` for unknown extensions.

#### Scenario: Generated file appears in outputs
- **WHEN** a script writes `screenshot.png` to `WORKSPACE_DIR` and exits
- **THEN** the response's `outputs` array SHALL include an entry with `path` ending in `screenshot.png`, `mime: "image/png"`, and `bytes` matching the file size

#### Scenario: Hidden files excluded
- **WHEN** `WORKSPACE_DIR` contains `.stdout`, `.stderr`, `.metadata.json` plus a real output `results.txt`
- **THEN** `outputs` SHALL contain only the entry for `results.txt`

#### Scenario: Subdirectory contents excluded
- **WHEN** a script creates `subdir/inner.md` inside the workspace
- **THEN** the `outputs` array SHALL NOT include the nested file; the subdirectory itself SHALL NOT appear as an entry

#### Scenario: Unknown extension falls back to content detection
- **WHEN** a file has no recognized extension
- **THEN** the service SHALL run `http.DetectContentType` on its first 512 bytes and use the returned MIME

### Requirement: Install endpoint contract
`POST /v1/install` SHALL accept a JSON body with one field `skill` (string, required) and SHALL install the skill's `scripts/requirements.txt` into `/cache/<skill>/venv` using `uv`. The endpoint SHALL be idempotent: calling it for an already-installed skill SHALL return immediately with `status: "ok"`. Concurrent install requests for the same skill SHALL serialize on a per-skill mutex.

Response body:
```json
{"status": "ok" | "error", "duration_ms": 12345, "error": "<message if status error>"}
```

HTTP status:
- 200 with `status:"ok"` on successful install
- 200 with `status:"error"` on install failure (uv error, missing requirements.txt treated as "nothing to install" → `status:"ok"`)
- 400 on unknown skill or missing `skill` in the request body

#### Scenario: Install succeeds and caches venv
- **WHEN** `POST /v1/install` is called for a skill with a valid `scripts/requirements.txt`
- **THEN** the response SHALL be 200 with `status:"ok"`, and `/cache/<skill>/venv/bin/python` SHALL exist with the declared packages importable

#### Scenario: Skill without requirements.txt is a no-op success
- **WHEN** `POST /v1/install` is called for a skill whose `scripts/requirements.txt` does not exist
- **THEN** the response SHALL be 200 with `status:"ok"` and an empty venv or no venv created (implementation choice); subsequent `/v1/run` for this skill SHALL proceed without triggering a lazy install

#### Scenario: Install failure is non-fatal to the service
- **WHEN** a `uv pip install` invocation exits non-zero
- **THEN** the response SHALL be 200 with `status:"error"` and a descriptive `error` string; the service SHALL continue accepting requests; a subsequent successful install for the same skill SHALL clear the failure state

#### Scenario: Concurrent installs serialize
- **WHEN** two `POST /v1/install` requests arrive for the same skill simultaneously
- **THEN** they SHALL not race; the second caller SHALL wait for the first and return the first's result (same venv state, different `duration_ms` reflecting the wait)

### Requirement: Health endpoint
`GET /v1/health` SHALL return a JSON object with `status` (`"starting"` during the initial prewarm sweep, `"ok"` after), `ready` (boolean mirror of `status == "ok"`), `prewarmed_skills` (array of skill names whose venv currently exists at `/cache/<skill>/venv`), and `failed_installs` (array of `{skill, error, at}` objects for skills where the most recent install attempt failed).

The service SHALL always respond to `/v1/health` with HTTP 200, including during `starting` status.

#### Scenario: Startup reports starting then ok
- **WHEN** the service boots and at least one skill has `x-microagent.prewarm: true`
- **THEN** `GET /v1/health` SHALL return `status: "starting", ready: false` until prewarm completes, then `status: "ok", ready: true`

#### Scenario: No prewarm skills yields immediate ok
- **WHEN** the service boots and no skill has `x-microagent.prewarm: true`
- **THEN** `GET /v1/health` SHALL return `status: "ok", ready: true` as soon as the HTTP listener is bound

#### Scenario: Failed prewarm appears in failed_installs
- **WHEN** a prewarm install exits non-zero
- **THEN** the skill SHALL appear in `failed_installs` with a non-empty `error` field and an ISO-8601 `at` timestamp; `status` SHALL still transition to `"ok"` once all prewarm attempts have completed

#### Scenario: Successful retry clears failure
- **WHEN** a skill was in `failed_installs` and a subsequent `/v1/install` succeeds
- **THEN** the skill SHALL be removed from `failed_installs` and added to `prewarmed_skills`

### Requirement: Dependency management via uv
The service SHALL use `uv` as the sole Python dependency resolver and installer. Venvs SHALL be created at `/cache/<skill>/venv` with `uv venv --python 3.12`. Dependency installation SHALL use `uv pip install -r scripts/requirements.txt --python /cache/<skill>/venv/bin/python`. The runtime image SHALL bake `uv` and Python 3.12 so no network access is required to stand up the interpreter itself.

#### Scenario: Venv location follows convention
- **WHEN** a skill's deps are installed
- **THEN** a venv SHALL exist at `/cache/<skill>/venv/` with `bin/python` present

#### Scenario: Subsequent runs use the cached venv
- **WHEN** two `/v1/run` calls for the same skill are made after a successful install
- **THEN** only the first (or the explicit `/v1/install`) SHALL have run `uv pip install`; subsequent runs SHALL use the cached venv with `install_duration_ms: 0`

#### Scenario: requirements.txt parse failure surfaces in install response
- **WHEN** a skill's `scripts/requirements.txt` is malformed (e.g. unresolvable constraint)
- **THEN** `/v1/install` SHALL return `status:"error"` with uv's stderr message in the `error` field

### Requirement: Install phases — prewarm and lazy
The service SHALL scan `/skills/*/SKILL.md` at boot and install dependencies concurrently for each skill whose frontmatter contains `x-microagent.prewarm: true` under a dedicated top-level YAML block. Prewarm concurrency SHALL be capped by `EXEC_PREWARM_CONCURRENCY` (default 4). For skills without prewarm, the first `/v1/run` call SHALL trigger a lazy install inline with the run; the response's `install_duration_ms` SHALL reflect that install's wall-clock time.

#### Scenario: Prewarm on boot for opted-in skills
- **WHEN** the service boots and `skills/foo/SKILL.md` has `x-microagent: {prewarm: true}` in frontmatter
- **THEN** `foo`'s deps SHALL be installed during boot; `foo` SHALL appear in `/v1/health`'s `prewarmed_skills` before `status` becomes `"ok"`

#### Scenario: Lazy install on first run
- **WHEN** `/v1/run` is called for a skill with no prewarm and no prior install
- **THEN** the response SHALL have `install_duration_ms > 0` reflecting the inline install, and subsequent runs for the same skill SHALL have `install_duration_ms: 0`

#### Scenario: Prewarm does not block service startup indefinitely
- **WHEN** one prewarm install hangs
- **THEN** the service SHALL still accept `/v1/health` requests (reporting `"starting"`) and other skills' prewarms SHALL proceed; `/v1/run` for non-pending skills SHALL work while the hung install persists (up to `EXEC_INSTALL_TIMEOUT_S`, default 600)

### Requirement: Workspace isolation and lifecycle
The service SHALL allocate a per-invocation workspace directory at `/workspace/<session_id>/<invocation_id>/` before spawning each subprocess, with ownership of the runtime UID and mode `0o755`. The workspace SHALL persist for `EXEC_WORKSPACE_RETENTION_MINUTES` (default 60) after the invocation ends, then be reclaimed by a background GC sweep. The `/workspace` mount SHALL be a tmpfs bounded at `EXEC_WORKSPACE_TMPFS_SIZE` (default 64 MB, enforced at container start).

#### Scenario: Each invocation gets a fresh directory
- **WHEN** two `/v1/run` calls execute for the same `session_id`
- **THEN** each SHALL have a distinct `<invocation_id>` subdirectory; writes in one SHALL NOT be visible from the other until GC or retention end

#### Scenario: Retention preserves workspace for follow-up reads
- **WHEN** a run completes at T=0 and `EXEC_WORKSPACE_RETENTION_MINUTES=60`
- **THEN** the workspace SHALL remain on disk until at least T=60min; a file-reader (e.g. main-agent following up to read an output path) SHALL succeed within that window

#### Scenario: GC reclaims expired workspaces
- **WHEN** the GC sweep runs (every `EXEC_GC_INTERVAL_MINUTES`, default 5) and a workspace's `.metadata.json` shows the invocation ended more than `EXEC_WORKSPACE_RETENTION_MINUTES` ago
- **THEN** the GC SHALL remove the directory and its contents; the response for the original run remains historically accurate but the `workspace_dir` path is no longer dereferencable

#### Scenario: Tmpfs exhaustion returns structured error
- **WHEN** a run's writes would exceed the tmpfs cap
- **THEN** the response SHALL be HTTP 409 with a JSON body indicating `workspace_full`; GC SHALL run immediately in case retention-expired space can be reclaimed

### Requirement: Sandbox invariants
The subprocess spawned by `/v1/run` SHALL execute under a non-root UID (the container's `exec` user, UID 1500), with `--cap-drop=ALL` enforced at container level. Its environment SHALL be constructed from scratch per invocation and SHALL NOT include any variable from the service process's own `os.Environ()` except those explicitly whitelisted. `/skills` SHALL be mounted read-only; the subprocess SHALL NOT be able to modify it.

The minimum environment passed to the subprocess SHALL include:
- `PATH` set to the venv bin followed by `/usr/local/bin:/usr/bin:/bin`
- `HOME` set to the workspace directory
- `WORKSPACE_DIR` set to the workspace directory
- `VIRTUAL_ENV` set to `/cache/<skill>/venv` (when a venv exists for this skill)
- `LANG=C.UTF-8`
- `PYTHONUNBUFFERED=1`

No other variables SHALL be inherited from the service process.

#### Scenario: Subprocess runs as non-root
- **WHEN** any `/v1/run` invocation executes
- **THEN** the subprocess's effective UID SHALL be 1500 (the `exec` user), verifiable via `id -u` invoked as a test script

#### Scenario: /skills is read-only from the subprocess
- **WHEN** a subprocess attempts to write to any path under `/skills/`
- **THEN** the write SHALL fail with `EROFS` or equivalent filesystem error

#### Scenario: No secrets in environment
- **WHEN** a subprocess inspects its environment (e.g. `os.environ` in Python)
- **THEN** it SHALL NOT see any variable whose name begins with `ANTHROPIC_`, `VALKEY_`, `HINDSIGHT_`, or any EXEC_-prefixed variable outside the whitelist above

#### Scenario: Process group receives SIGKILL on timeout
- **WHEN** a subprocess forks child processes (e.g. Playwright browser) and the outer deadline fires
- **THEN** SIGKILL SHALL be delivered to the entire process group, leaving no surviving child processes attributable to the invocation

### Requirement: Network policy
The service SHALL maintain a binary network policy per skill: `allow` (default) or `deny`. Policy is configured by two env variables: `EXEC_NETWORK_DEFAULT` (default `allow`) and `EXEC_NETWORK_DENY_SKILLS` (comma-separated list of skill names, default empty). When a skill's policy is `deny`, `/v1/run` for that skill SHALL return HTTP 400 with a JSON error `{"error":"network policy denies this skill: <skill>"}` without spawning a subprocess.

#### Scenario: Default allow lets all skills run
- **WHEN** `EXEC_NETWORK_DEFAULT=allow` and `EXEC_NETWORK_DENY_SKILLS=` (empty)
- **THEN** `/v1/run` SHALL execute any valid skill script

#### Scenario: Per-skill deny blocks the run
- **WHEN** `EXEC_NETWORK_DENY_SKILLS=suspect-skill`
- **THEN** `/v1/run` for `suspect-skill` SHALL return 400 with the deny error; other skills SHALL continue to run

#### Scenario: Default deny with specific allows
- **WHEN** `EXEC_NETWORK_DEFAULT=deny` and `EXEC_NETWORK_DENY_SKILLS=` (empty)
- **THEN** `/v1/run` for any skill SHALL return 400 with the deny error (all skills are denied; there is no explicit allow-list in v1)

### Requirement: Observability via structured logs
The service SHALL emit structured JSON log lines for lifecycle events using the `slog` stdlib logger writing to stdout. At minimum, the following messages SHALL be emitted with the listed fields:

| Message | Fields |
|---|---|
| `exec_run_started` | `skill`, `script`, `args`, `session_id`, `invocation_id`, `workspace_dir` |
| `exec_run_finished` | `skill`, `script`, `invocation_id`, `exit_code`, `duration_ms`, `timed_out`, `stdout_bytes`, `stderr_bytes` |
| `exec_install_started` | `skill`, `phase` (`"prewarm"` or `"lazy"` or `"explicit"`) |
| `exec_install_finished` | `skill`, `phase`, `status`, `duration_ms`, `error` (when status is `error`) |
| `exec_workspace_gc` | `scanned`, `reclaimed`, `freed_bytes` |
| `exec_run_rejected` | `skill`, `reason` (`"unknown_skill"`, `"script_escape"`, `"network_denied"`, `"workspace_full"`, etc.) |

#### Scenario: Every run produces start and finish lines
- **WHEN** `/v1/run` completes (successfully or with timeout/error)
- **THEN** exactly one `exec_run_started` and exactly one `exec_run_finished` log line SHALL be emitted, with matching `invocation_id`

#### Scenario: Rejections are logged without started/finished
- **WHEN** `/v1/run` is rejected before spawning a subprocess (validation failure, network-denied, workspace-full)
- **THEN** a single `exec_run_rejected` SHALL be emitted and no `exec_run_started`/`exec_run_finished` pair SHALL appear

### Requirement: Graceful shutdown
On SIGTERM or SIGINT, the service SHALL stop accepting new HTTP connections, allow in-flight runs to complete up to their request-scoped deadline, cancel the GC goroutine, and exit. Forced shutdown SHALL occur only if the grace period exceeds `EXEC_SHUTDOWN_GRACE_S` (default equal to `EXEC_MAX_TIMEOUT_S + 5`).

#### Scenario: In-flight run completes during shutdown
- **WHEN** `/v1/run` is executing a subprocess and SIGTERM is received
- **THEN** the HTTP server SHALL stop accepting new connections; the in-flight request SHALL run to completion (or its deadline); the service SHALL then exit 0

#### Scenario: Deadline-exceeded runs are killed on shutdown
- **WHEN** SIGTERM is received and a run exceeds the shutdown grace period
- **THEN** the subprocess group SHALL receive SIGKILL and the service SHALL exit within a bounded time

### Requirement: Bash endpoint
The `exec` service SHALL expose `POST /v1/bash` accepting a JSON body with fields:
- `command` (string, required) — shell command to execute via `sh -c`
- `session_id` (string, required) — identifier scoping the persistent sandbox directory
- `timeout_s` (integer, optional) — per-command deadline; clamped to `[1, EXEC_MAX_TIMEOUT_S]`

The response envelope SHALL be a JSON object with:
- `exit_code` (integer)
- `stdout` (string, inline-capped)
- `stdout_truncated` (boolean)
- `stderr` (string, inline-capped)
- `stderr_truncated` (boolean)
- `sandbox_dir` (string) — absolute path to the session's persistent workspace
- `duration_ms` (integer)
- `timed_out` (boolean)

HTTP status:
- 200 on any subprocess completion (including non-zero exits and timeouts)
- 400 on malformed request body, missing `command`, or missing `session_id`
- 409 on sandbox tmpfs exhaustion
- 500 only on internal server failures that prevented any attempt to run

#### Scenario: Successful command returns 200 envelope
- **WHEN** `POST /v1/bash` is called with `{"command":"echo hi","session_id":"sess-1"}`
- **THEN** the response SHALL be 200 with `exit_code: 0`, `stdout` containing `"hi"`, `stdout_truncated: false`, `timed_out: false`, and a `sandbox_dir` ending in `sess-1/`

#### Scenario: Missing command returns 400
- **WHEN** `POST /v1/bash` is called without `command` or with an empty `command`
- **THEN** the response SHALL be 400 with a JSON error

#### Scenario: Missing session_id returns 400
- **WHEN** `POST /v1/bash` is called without `session_id` or with an empty `session_id`
- **THEN** the response SHALL be 400

#### Scenario: Non-zero exit preserved
- **WHEN** a command exits with status 7
- **THEN** the response SHALL have `exit_code: 7` and `timed_out: false`

#### Scenario: Timeout reports timed_out
- **WHEN** a command runs longer than its deadline
- **THEN** the response SHALL have `timed_out: true`, `exit_code: -1`, and the process group SHALL have received SIGKILL

#### Scenario: Shell metacharacters honored
- **WHEN** the command is `echo one && echo two`
- **THEN** stdout SHALL contain both `one` and `two` on separate lines, proving the command is run via a shell interpreter

#### Scenario: Stdout cap truncates at rune boundary
- **WHEN** a command writes more than `EXEC_STDOUT_CAP_BYTES` bytes to stdout
- **THEN** `stdout` SHALL be capped (at a UTF-8 rune boundary), `stdout_truncated: true`, and the full stdout SHALL be written to `<sandbox_dir>/.stdout`

#### Scenario: Stderr cap behaves symmetrically
- **WHEN** a command writes more than `EXEC_STDERR_CAP_BYTES` to stderr
- **THEN** `stderr` SHALL be capped with `stderr_truncated: true` and full content at `<sandbox_dir>/.stderr`

### Requirement: Session-persistent sandbox directory
Each session id SHALL have one persistent workspace at `/sandbox/<sanitized_session_id>/`. The directory SHALL be created lazily on the first `/v1/bash` call for that session and persisted across subsequent calls. Files written in one call SHALL be visible to subsequent calls for the same session. Sanitization of the session id SHALL follow the same rules as `/v1/run`'s session id (path-separator-safe; empty → `anon`).

#### Scenario: Directory created on first call
- **WHEN** `POST /v1/bash` is called with a session id that has no existing sandbox directory
- **THEN** the directory SHALL be created at `/sandbox/<sanitized>/` with mode 0o755, owned by the runtime UID

#### Scenario: Files persist across calls
- **WHEN** a command writes `hello.py` to the sandbox and a subsequent call for the same session executes `ls`
- **THEN** the second call's stdout SHALL include `hello.py`

#### Scenario: Sessions isolated from each other
- **WHEN** session A writes a file and session B runs `ls`
- **THEN** session B's output SHALL NOT include session A's file

#### Scenario: Session ID sanitization prevents traversal
- **WHEN** `POST /v1/bash` is called with `session_id: "../../etc"` or similar traversal input
- **THEN** the resolved sandbox path SHALL be inside `/sandbox/` regardless of the input

### Requirement: Touch-based sandbox retention
Each sandbox directory SHALL have a `.last_access` file whose mtime is bumped on every `/v1/bash` call for that session. A background GC goroutine SHALL periodically sweep `/sandbox/` and remove session directories whose `.last_access` mtime (or directory mtime when `.last_access` is absent) is older than `EXEC_SANDBOX_RETENTION_MINUTES` (default 60). The GC interval SHALL be `EXEC_SANDBOX_GC_INTERVAL_MINUTES` (default equal to `EXEC_GC_INTERVAL_MINUTES`).

#### Scenario: Active session retained
- **WHEN** a session issues one bash call per minute
- **THEN** its sandbox directory SHALL NOT be reclaimed even after hours of activity

#### Scenario: Inactive session reclaimed
- **WHEN** a session's `.last_access` is older than the retention window and the GC sweep runs
- **THEN** the directory SHALL be removed from `/sandbox/`

#### Scenario: Missing .last_access falls back to directory mtime
- **WHEN** a session directory has no `.last_access` file
- **THEN** the GC SHALL use the directory's mtime for retention comparison, preventing indefinite survival via deletion of the marker

#### Scenario: GC logs summary
- **WHEN** a sandbox GC sweep completes
- **THEN** an INFO log line `exec_sandbox_gc` SHALL be emitted with fields `{scanned, reclaimed, freed_bytes}`

### Requirement: Per-session mutex
The bash runner SHALL serialize `/v1/bash` calls for a single session via a `sync.Mutex` keyed by session id. Different sessions SHALL continue to run in parallel.

#### Scenario: Concurrent calls same session serialize
- **WHEN** two `/v1/bash` calls for the same session id arrive simultaneously
- **THEN** the second SHALL wait for the first to finish before running; no filesystem race SHALL occur

#### Scenario: Different sessions run in parallel
- **WHEN** two `/v1/bash` calls for different session ids arrive simultaneously
- **THEN** both SHALL execute concurrently without blocking each other

### Requirement: Subprocess isolation for bash
Bash subprocesses SHALL inherit the same sandbox invariants as `/v1/run`:
- Non-root UID 1500 (from the container's USER directive)
- `--cap-drop=ALL` at container level
- Environment constructed from scratch per invocation with only: `PATH=/usr/local/bin:/usr/bin:/bin`, `HOME=/sandbox/<session>/`, `LANG=C.UTF-8`, `PYTHONUNBUFFERED=1`
- `SysProcAttr.Setpgid = true` so SIGKILL on timeout reaches the whole process group
- No variables from the service's own `os.Environ()` propagated

The bash subprocess SHALL NOT have `WORKSPACE_DIR` set (that variable is reserved for `/v1/run` per-invocation workspaces).

#### Scenario: Non-root execution
- **WHEN** a bash command invokes `id -u`
- **THEN** the output SHALL be `1500`

#### Scenario: No inherited secrets
- **WHEN** a bash command invokes `env | grep -E 'ANTHROPIC|VALKEY|HINDSIGHT'`
- **THEN** the output SHALL be empty

#### Scenario: CWD is the session sandbox
- **WHEN** a bash command invokes `pwd`
- **THEN** the output SHALL be `/sandbox/<sanitized_session_id>`

#### Scenario: Process group killed on timeout
- **WHEN** a bash command spawns a child process and the outer deadline fires
- **THEN** SIGKILL SHALL be delivered to the entire process group

### Requirement: Network policy inherited
Bash commands SHALL honor the operator's network policy configured via `EXEC_NETWORK_DEFAULT`. When the default is `deny`, all bash calls SHALL be rejected with a 400 response before any subprocess is spawned. When the default is `allow`, bash commands SHALL have network access.

The per-skill `EXEC_NETWORK_DENY_SKILLS` list does NOT apply to `/v1/bash` (bash is not scoped to a skill).

#### Scenario: Allow default lets bash reach network
- **WHEN** `EXEC_NETWORK_DEFAULT=allow` and a bash command runs `curl -s http://example.com`
- **THEN** the command SHALL proceed and reach the network (subject to the command's own behavior)

#### Scenario: Deny default blocks bash entirely
- **WHEN** `EXEC_NETWORK_DEFAULT=deny`
- **THEN** every `POST /v1/bash` call SHALL return 400 with a JSON error indicating the operator policy denies bash

### Requirement: Bash observability
Structured log lines SHALL be emitted around each bash invocation:

| Message | Fields |
|---|---|
| `exec_bash_started` | `session_id`, `command_length`, `timeout_s` |
| `exec_bash_finished` | `session_id`, `exit_code`, `duration_ms`, `timed_out`, `stdout_bytes`, `stderr_bytes` |
| `exec_bash_rejected` | `session_id`, `reason` (`"missing_command"`, `"missing_session_id"`, `"network_denied"`, `"sandbox_full"`, etc.) |

The `command` string itself SHALL NOT be logged at INFO (it may contain prompt-injected content); only its length.

#### Scenario: Every accepted call produces start and finish
- **WHEN** `POST /v1/bash` completes (successfully or with timeout)
- **THEN** exactly one `exec_bash_started` and exactly one `exec_bash_finished` log line SHALL be emitted

#### Scenario: Rejections produce a single rejected log
- **WHEN** a `/v1/bash` call is rejected before spawn (bad input, network denied, full sandbox)
- **THEN** exactly one `exec_bash_rejected` log SHALL be emitted and no `exec_bash_started`/`exec_bash_finished` pair SHALL appear

#### Scenario: Command content not logged
- **WHEN** a bash command contains the string `"secret_value_please_dont_log"`
- **THEN** no log line emitted by the exec service SHALL contain that substring
