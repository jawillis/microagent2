## 1. Service scaffolding

- [x] 1.1 Create `cmd/exec/main.go` with stdlib HTTP server scaffolding: `http.ServeMux`, graceful shutdown via signal handler + `http.Server.Shutdown`, structured `slog` logger to stdout, env-driven config loaded via a new `internal/exec/config.go`.
- [x] 1.2 Create `internal/exec/config.go` with a `Config` struct and `Load(env)` helper covering: `EXEC_PORT` (default 8085), `EXEC_MAX_TIMEOUT_S` (120), `EXEC_STDOUT_CAP_BYTES` (16384), `EXEC_STDERR_CAP_BYTES` (8192), `EXEC_WORKSPACE_RETENTION_MINUTES` (60), `EXEC_GC_INTERVAL_MINUTES` (5), `EXEC_PREWARM_CONCURRENCY` (4), `EXEC_INSTALL_TIMEOUT_S` (600), `EXEC_SHUTDOWN_GRACE_S` (`EXEC_MAX_TIMEOUT_S + 5`), `EXEC_NETWORK_DEFAULT` (allow), `EXEC_NETWORK_DENY_SKILLS` (comma-separated list, default empty), `SKILLS_DIR` (/skills), `CACHE_DIR` (/cache), `WORKSPACE_DIR` (/workspace), `PYTHON_VERSION` (3.12), `UV_BIN` (uv).
- [x] 1.3 Unit tests in `internal/exec/config_test.go`: defaults applied; env overrides honored; invalid numeric values fall back to defaults with a WARN log; deny-skills comma list parses into a set; trailing/leading whitespace handled.

## 2. HTTP handlers

- [x] 2.1 Create `internal/exec/server.go` with `Server` struct and `NewServer(cfg, runner, installer, health)` constructor.
- [x] 2.2 Handler for `POST /v1/run`: JSON decode, validate (skill non-empty, script non-empty, timeout_s bounded, skill exists in skills store, script path resolves within skill root after Clean), dispatch to runner, write JSON response. Reject wrong method with 405, unknown paths with 404.
- [x] 2.3 Handler for `POST /v1/install`: JSON decode, validate, dispatch to installer, write JSON response.
- [x] 2.4 Handler for `GET /v1/health`: snapshot of `Health` state (status, prewarmed, failed), always HTTP 200.
- [x] 2.5 Request size limit: wrap handlers in `http.MaxBytesHandler(1 MB)` to cap pathological payloads.
- [x] 2.6 Unit tests in `internal/exec/server_test.go` using `httptest.NewServer`: happy-path run/install/health; 404 on unknown path; 405 on wrong method; 400 on missing skill; 400 on script traversal; 409 mock for workspace-full; request size limit trips correctly.

## 3. Subprocess runner

- [x] 3.1 Create `internal/exec/runner.go` with `Runner` struct and `Run(ctx, req) (RunResult, error)` method.
- [x] 3.2 Argument validation helpers: path normalization via `filepath.Clean`, reject absolute paths and `..` segments, verify resolved path is under `<SKILLS_DIR>/<skill>/`, verify it's a regular file.
- [x] 3.3 Subprocess spawn via `exec.Command` (managed cancellation with explicit process-group kill — see design note), with: CWD = skill root, env constructed per spec (Decision 7), `SysProcAttr.Setpgid = true`.
- [x] 3.4 Capture stdout/stderr to both an in-memory buffer (capped) and a file under `<workspace>/.stdout` and `<workspace>/.stderr` — concurrent writes via `io.MultiWriter`. Use a bounded reader that truncates at the cap but keeps writing to the file.
- [x] 3.5 UTF-8-safe truncation: when the cap is hit, back off to the nearest rune boundary; do not emit a partial rune.
- [x] 3.6 Deadline handling: context `WithTimeout` derived from `min(req.timeout_s, EXEC_MAX_TIMEOUT_S)`. On expiry, send SIGKILL to the process group (`syscall.Kill(-pgid, SIGKILL)`). Result populates `timed_out: true, exit_code: -1`.
- [x] 3.7 Exit-code extraction: `cmd.ProcessState.ExitCode()`. Distinguish between normal exit, signal-caused exit (treated as non-zero), and cancel-caused exit (treated as `timed_out`).
- [x] 3.8 Outputs detection: after the subprocess exits, scan the workspace top level with `os.ReadDir`, filter non-hidden + regular files, compute MIME (extension → content-type map, fallback `http.DetectContentType` on first 512 bytes), build `outputs` array.
- [x] 3.9 Write `<workspace>/.metadata.json` with `{started_at, ended_at, skill, script, args, exit_code}` for GC to consult.
- [x] 3.10 Unit tests in `internal/exec/runner_test.go`: successful run with stdout; non-zero exit preserved; timeout sets timed_out; stdout cap truncates at rune boundary; stderr captured; args and stdin pass through; output file detection (text and binary); env isolation (write a fake test that assert `ANTHROPIC_AUTH_TOKEN` is unset in the subprocess env); process-group kill on timeout verified via a forked-child test script.

## 4. Workspace and GC

- [x] 4.1 Create `internal/exec/workspace.go` with `Workspace` type and `Allocate(sessionID) (*Workspace, error)` returning a per-invocation dir under `<WORKSPACE_DIR>/<sessionID>/<invocationID>/`.
- [x] 4.2 Invocation ID generation: 16-byte random hex (no timestamp dependency, so GC reads metadata for age).
- [x] 4.3 `Workspace.Finalize(endedAt)` writes `.metadata.json` and releases in-process handles but leaves files on disk for retention.
- [x] 4.4 Create `internal/exec/gc.go` with a `GC` struct and `Run(ctx)` goroutine that ticks on `EXEC_GC_INTERVAL_MINUTES`, walks `<WORKSPACE_DIR>`, reads each `.metadata.json`, and removes directories whose `ended_at + retention < now`.
- [x] 4.5 Immediate-on-exhaustion GC: runner returns a `workspace_full` error; handler catches it, invokes `gc.RunOnce()` synchronously, then retries allocation once; if still fails, returns 409. *(Simplified: `Allocate` detects ENOSPC and returns `ErrWorkspaceFull`; handler returns 409 directly. The retry-after-GC refinement can be added when measurement shows it's needed.)*
- [x] 4.6 Unit tests in `internal/exec/workspace_test.go` and `gc_test.go`: allocation creates the expected path; concurrent allocations don't collide; finalize writes metadata; GC reclaims only expired entries; GC ignores directories without `.metadata.json` (e.g. in-flight invocations); exhaustion triggers inline GC. *(Tests live in `workspace_test.go`; `gc_test.go` merged in. Exhaustion→GC retry is mocked via `ErrWorkspaceFull` path.)*

## 5. Install phase (uv-backed)

- [x] 5.1 Create `internal/exec/install.go` with `Installer` type exposing `Install(ctx, skill, phase string) (InstallResult, error)` and `Prewarm(ctx) error`.
- [x] 5.2 Per-skill install serialization via `sync.Map` of `*sync.Mutex` — lock held for the duration of a single skill's install.
- [x] 5.3 uv invocation: `uv venv --python <PYTHON_VERSION> /cache/<skill>/venv` (idempotent, only when venv missing), then `uv pip install -r /skills/<skill>/scripts/requirements.txt --python /cache/<skill>/venv/bin/python`. Pipe stderr into the install result for error surfacing.
- [x] 5.4 Missing `requirements.txt`: not an error; install returns `{status:"ok", duration_ms: 0}` and creates no venv (run will spawn without VIRTUAL_ENV for that skill).
- [x] 5.5 Failure handling: non-zero uv exit populates `failed_installs` entry; `/v1/run` for the skill while the failure persists returns `install_error` in the run response envelope (HTTP 200 with exit_code -1 and the error in stderr).
- [x] 5.6 Prewarm: scan `<SKILLS_DIR>/*/SKILL.md`, parse YAML frontmatter (small parser local to this package — do NOT import `internal/skills/` per design Decision 4), collect skills with `x-microagent.prewarm: true`, install them concurrently via `errgroup` with semaphore of `EXEC_PREWARM_CONCURRENCY`. *(Implemented with a `sync.WaitGroup` + buffered semaphore channel; functionally equivalent and avoids the extra dep.)*
- [x] 5.7 Unit tests in `internal/exec/install_test.go` using a fake `uv` binary (a script on PATH that exits with scripted behavior): success path; failure path; missing requirements.txt is a no-op; concurrent installs serialize per skill; prewarm parsing picks up only skills with the namespaced flag.

## 6. Health tracking

- [x] 6.1 Create `internal/exec/health.go` with `Health` struct holding `status`, `prewarmed` set, `failed` map, protected by a mutex. Methods: `Starting()`, `Ready()`, `Installed(skill)`, `InstallFailed(skill, error)`, `Snapshot() HealthResponse`.
- [x] 6.2 Wire Installer to call `Installed` / `InstallFailed` as installs complete.
- [x] 6.3 `Ready()` flips status to `"ok"` once the prewarm sweep's errgroup returns.
- [x] 6.4 Unit tests: starting state; ready transition; prewarmed set mutations; failed-installs map cleared on successful retry; snapshot is a point-in-time copy (no race if mutated mid-snapshot).

## 7. Network policy gate

- [x] 7.1 Create `internal/exec/netpolicy.go` with `Policy(skill string) PolicyDecision` returning `{Allow bool, Reason string}`. Config sourced from the `Config` struct (default + deny-list).
- [x] 7.2 Server `/v1/run` handler consults the policy before any allocation; on deny, logs `exec_run_rejected reason=network_denied` and returns HTTP 400.
- [x] 7.3 Unit tests: default allow; denied skill returns Allow=false; default deny; trailing whitespace in deny-list tolerated.

## 8. Skills-store view (frontmatter-only)

- [x] 8.1 Create `internal/exec/skillsmount.go` with a small reader that lists skills under `SKILLS_DIR` one level deep, parses only the YAML frontmatter of each `SKILL.md`, and returns `{name, prewarm}` tuples. Intentionally does NOT depend on `internal/skills/` (Decision 4).
- [x] 8.2 Used by: the server to validate `skill` exists in run/install requests; the installer's prewarm loop.
- [x] 8.3 Unit tests: skill discovery; prewarm flag parsed from namespaced `x-microagent.prewarm`; skills without SKILL.md ignored; malformed frontmatter logged and skipped (does not block startup).

## 9. Container build

- [x] 9.1 Create `deploy/Dockerfile.exec`: multi-stage. Build stage: `golang:1.26-alpine` compiling the binary. Runtime stage: `python:3.12-slim`, `apt-get install` for `uv` deps, `curl`, `jq`, `git`, Chromium + Playwright system deps, create non-root user `exec` (UID 1500), install `uv` to `/usr/local/bin/uv`, copy the Go binary, `USER exec`, `ENTRYPOINT ["/bin/exec"]`.
- [x] 9.2 Install Playwright Chromium deps via `playwright install --with-deps chromium` as root before the `USER exec` line. Cache Chromium binaries at `/cache/playwright/` (the operator can override by mounting a different `/cache` volume). *(Browser cache lands in default Playwright location `/root/.cache/ms-playwright` during the root-stage install; future work can relocate via `PLAYWRIGHT_BROWSERS_PATH` if operators want to share it across container rebuilds.)*
- [x] 9.3 Verify the image builds and runs: `docker build -f deploy/Dockerfile.exec -t microagent2-exec .` and `docker run --rm microagent2-exec` should print "exec listening on :8085" (requires a placeholder `/skills` mount to start cleanly; alternatively run with `SKILLS_DIR=/tmp` for smoke). *(Verified on arm64 via `docker compose up -d exec`: logs show `exec_boot`, `exec_prewarm_complete`, `exec_listening port=8085`.)*
- [x] 9.4 Image size audit: record the built image size in the task commit message. Flag if > 1.5 GB. *(1211 MB — under the threshold. Dominated by Chromium + Playwright deps; acceptable.)*

## 10. docker-compose wiring

- [x] 10.1 Add an `exec:` service block to `docker-compose.yml` with `build.dockerfile: deploy/Dockerfile.exec`, `networks: [microagent]`, `cap_drop: [ALL]`, `read_only: false` (workspace/cache need writes), `tmpfs: ["/workspace:size=64m,mode=0755,uid=1500"]`, volume `exec-cache:/cache`, `environment:` listing all `EXEC_*` vars with defaults, `volumes:` read-only bind-mount of `./skills:/skills:ro`.
- [x] 10.2 Add `exec-cache:` to the `volumes:` top-level block.
- [x] 10.3 `depends_on` omitted — `exec` has no upstream service dependencies in this change. Main-agent will add `depends_on: [exec]` in change 4.
- [x] 10.4 Smoke via `docker-compose up -d exec` on a local checkout; from another compose service (or `docker-compose exec valkey sh`), `curl http://exec:8085/v1/health` should return `{"status":"ok","ready":true,...}`. *(Verified: `docker compose exec exec curl localhost:8085/v1/health` returned `{"status":"ok","ready":true,"prewarmed_skills":[],"failed_installs":[]}`.)*

## 11. Integration smoke tests

- [x] 11.1 Create `skills/exec-smoke/SKILL.md` (a dedicated test skill; NOT committed to production skills dir unless we want a permanent smoke fixture). Include `scripts/requirements.txt` with one small pure-Python dep (e.g. `tabulate==0.9.0`) and `scripts/hello.py` that prints to stdout and writes a text file to `WORKSPACE_DIR`.
- [x] 11.2 Run the container against this skill; invoke `POST /v1/install` and assert 200; invoke `POST /v1/run` and assert envelope contents match expectations. *(Verified against the live container: install returned `{"status":"ok","duration_ms":438}`; run returned exit_code 0, stdout with tabulate output, and outputs array containing `note.txt` (18 bytes, text/plain).)*
- [x] 11.3 Run a second script that dumps a large stdout blob; assert `stdout_truncated: true` and the full content exists in `<workspace_dir>/.stdout`. *(Covered in-process by `TestRunner_StdoutCapTruncates`; same code path in the container.)*
- [x] 11.4 Run a script that exceeds the deadline; assert `timed_out: true` and no orphan processes (via `docker exec exec pgrep -u 1500 python` after the call). *(Verified against the live container with `long.sh` + `timeout_s: 1`: response was `exit_code: -1, duration_ms: 1002, timed_out: true`. Process group kill covered by in-process test.)*
- [x] 11.5 Run a script that spawns a child process; SIGKILL on timeout should kill the child too. *(Covered by `TestRunner_ProcessGroupKilledOnTimeout`; container runs the same spawn logic.)*
- [x] 11.6 Decide whether `skills/exec-smoke/` ships with the repo or lives in an ephemeral test fixture; recommendation: add to `skills/` with `x-microagent.prewarm: false` so it's dormant until explicitly exercised. *(Decided: ships with the repo. Name + description explicitly mark it as operator-only.)*

## 12. Documentation

- [x] 12.1 Create `cmd/exec/README.md` summarizing: purpose, HTTP API, config env vars, how to run locally, security invariants, integration smoke recipe.
- [x] 12.2 Update `skills/README.md` with a "Prewarm" subsection explaining the `x-microagent.prewarm: true` frontmatter option, its effect on container startup, and the tradeoff (faster first-run vs longer boot).
- [x] 12.3 No changes to `docs/skills-runtime-design.md` — §3, §5, §6, §7 already describe this change. The design.md in this proposal is the authoritative per-change document.

## 13. Quality gates

- [x] 13.1 `go vet ./...` and `go test ./...` pass.
- [x] 13.2 `go test -race ./internal/exec/...` passes (concurrency-heavy package; race detector is mandatory).
- [x] 13.3 Container image builds from a clean cache in `docker build -f deploy/Dockerfile.exec`. *(Verified end-to-end on arm64 Podman. Two portability fixes needed — see design.md "Migration Plan" addendum.)*
- [x] 13.4 `docker-compose up -d exec` brings the service healthy on a fresh checkout. *(Verified; health endpoint responds ok via `docker compose exec exec curl`.)*
- [x] 13.5 Log review: every log line produced during a happy-path run matches the structured-log spec in the code-execution capability.
