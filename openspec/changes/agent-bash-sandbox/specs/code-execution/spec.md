## ADDED Requirements

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
