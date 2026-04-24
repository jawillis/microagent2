## 1. Exec config + sandbox allocation

- [x] 1.1 Extend `internal/exec/config.go` with `SandboxDir string` (default `/sandbox`), `SandboxRetention time.Duration` (from `EXEC_SANDBOX_RETENTION_MINUTES`, default 60m), `SandboxGCInterval time.Duration` (from `EXEC_SANDBOX_GC_INTERVAL_MINUTES`, default `GCInterval`).
- [x] 1.2 Update `config_test.go`: default values, env overrides for new keys, invalid falls back to default with WARN.
- [x] 1.3 Create `internal/exec/sandbox.go` with `SandboxFor(root, sessionID string) (dir string, err error)` — sanitizes session_id (same rules as `sanitizeSessionID` in workspace.go), joins under root, lazily creates with mode 0o755, touches `<dir>/.last_access`.
- [x] 1.4 Refactor `sanitizeSessionID` if needed so both `workspace.go` and `sandbox.go` share one copy. Keep existing behavior. *(Shared already; `SandboxFor` calls the existing helper from `workspace.go`.)*
- [x] 1.5 Unit tests for `SandboxFor`: dir created on first call; subsequent calls for same session return same path; different sessions get different paths; traversal-y session ids sanitized; `.last_access` mtime bumped on every call.

## 2. Bash runner

- [x] 2.1 Create `internal/exec/bash.go` with `BashRunner` struct and `NewBashRunner(cfg *Config, logger *slog.Logger) *BashRunner`.
- [x] 2.2 Per-session mutex: `sync.Map[string]*sync.Mutex` with a `lockFor(sessionID)` helper matching the `Installer`'s pattern. *(Implemented with plain `map + sync.Mutex`; same invariants, smaller dep footprint.)*
- [x] 2.3 `Run(ctx, req *BashRequest) (*BashResponse, error)` — acquires the session lock, allocates/touches the sandbox, constructs the subprocess env (PATH/HOME/LANG/PYTHONUNBUFFERED, no secrets, no WORKSPACE_DIR), spawns `sh -c "<command>"` with `Setpgid`, streams stdout/stderr into capped buffers + `<sandbox>/.stdout` + `<sandbox>/.stderr`, watches ctx for timeout and kills `-pgid` on trip.
- [x] 2.4 Reuse the capped-buffer implementation from `runner.go` (`newCappedBuffer`). No extraction needed — already package-scoped.
- [x] 2.5 Build the `BashResponse` envelope: `exit_code`, `stdout`, `stdout_truncated`, `stderr`, `stderr_truncated`, `sandbox_dir`, `duration_ms`, `timed_out`.
- [x] 2.6 Unit tests (mirroring `runner_test.go`): happy-path echo; non-zero exit preserved; shell metacharacters work (`echo a && echo b`); stdout cap truncates at rune boundary and full content written to `.stdout`; stderr captured; timeout sets `timed_out: true`; env isolation (canary via `ANTHROPIC_AUTH_TOKEN` in the test process, asserted absent in subprocess env); per-session files persist across calls; different sessions isolated; process-group kill on timeout (forked-child test).

## 3. Sandbox GC

- [x] 3.1 Create a `SandboxGC` type in `internal/exec/sandbox.go` (or extend existing GC struct to handle both modes via a `RetentionMode` enum — design choice; separate type is simpler). *(Separate `SandboxGC` chosen — cleaner than polymorphic GC.)*
- [x] 3.2 `Run(ctx)` ticks on `EXEC_SANDBOX_GC_INTERVAL_MINUTES`, walks `<SandboxDir>`, reads `<dir>/.last_access` mtime (falls back to directory mtime when absent), removes dirs whose age exceeds `SandboxRetention`.
- [x] 3.3 `RunOnce(ctx) SandboxGCStats` for synchronous invocation (used on tmpfs exhaustion).
- [x] 3.4 Emit `exec_sandbox_gc` INFO log with `{scanned, reclaimed, freed_bytes}`.
- [x] 3.5 Unit tests: active session retained; inactive reclaimed; missing `.last_access` falls back to directory mtime; freed_bytes accounting.

## 4. HTTP handler

- [x] 4.1 Add `handleBash` to `internal/exec/server.go` following the shape of `handleRun`: method check, JSON decode with 1MB size limit, validate `command` and `session_id` non-empty, consult network policy, dispatch to `BashRunner.Run`, translate errors to HTTP status.
- [x] 4.2 Wire `/v1/bash` into the mux in `Server.Handler()`.
- [x] 4.3 Extend `NewServer` constructor to accept a `*BashRunner` alongside the existing runner/installer/health.
- [x] 4.4 Update `notFoundFallback`'s known-paths switch to include `/v1/bash`.
- [x] 4.5 Extend `Config`-driven `EXEC_NETWORK_DEFAULT` handling: when `deny`, reject every bash call at the handler with 400 and `exec_bash_rejected reason=network_denied`.
- [x] 4.6 Unit tests in `server_test.go`: happy-path bash; unknown path still 404; GET /v1/bash → 405; missing command → 400; missing session_id → 400; network deny → 400 with structured error.

## 5. Wire exec main

- [x] 5.1 In `cmd/exec/main.go`, construct `BashRunner` and pass it into `NewServer`.
- [x] 5.2 Start the sandbox GC goroutine alongside the existing workspace GC.
- [x] 5.3 Log startup config line now includes sandbox fields (retention, tmpfs cap marker) so operators see both GC loops wired. *(Handled via `Config.String()` which the existing `exec_boot` log line prints.)*

## 6. execclient Bash method

- [x] 6.1 Add `BashRequest`/`BashResponse` type aliases in `internal/execclient/execclient.go` (reusing `internal/exec` types per Decision 1 of change 4).
- [x] 6.2 Add `Bash(ctx, *BashRequest) (*BashResponse, error)` mirroring `Run`.
- [x] 6.3 Unit tests in `execclient_test.go`: happy path.

## 7. main-agent bash tool

- [x] 7.1 Create `internal/tools/bash.go` with `bashTool` struct holding `*execclient.Client` and `*slog.Logger`.
- [x] 7.2 Constructor `NewBash(client *execclient.Client, logger *slog.Logger) Tool`.
- [x] 7.3 Schema per design Decision 8: name `bash`, description calling out persistence, `/skills` inaccessibility, and network-follows-policy; `command` required string, `timeout_s` optional integer.
- [x] 7.4 `Invoke` parses args, validates `command`, calls `client.Bash`, returns envelope as JSON string; error classification reuses `classifyClientError` from `run_skill_script.go` (extract to a shared helper in `internal/tools/` — e.g. `tool_error.go`). *(Already package-private in `run_skill_script.go`; same-package reuse works without extraction.)*
- [x] 7.5 Unit tests in `bash_test.go` mirroring `run_skill_script_test.go`: success envelope verbatim; exec unavailable; deadline; non-200 status; missing command; malformed args; args forwarded intact.

## 8. Session-scoped tool whitelist

- [x] 8.1 In `cmd/main-agent/main.go`, replace the single-string `call.Function.Name == "run_skill_script"` check with a small set lookup `sessionScopedTools`, initialized at package level, containing `"run_skill_script"` and `"bash"`.
- [x] 8.2 Extend the whitelist in `invokeOrGate`'s injection path.
- [x] 8.3 Unit tests in `gating_test.go`: bash call gets session_id injected even if model omits it; bash call gets model-supplied session_id overridden; list_skills/read_skill/read_skill_file/current_time pass args unchanged.

## 9. main-agent registration

- [x] 9.1 In `cmd/main-agent/main.go`, register `tools.NewBash(execClient, logger)` immediately after `tools.NewRunSkillScript(...)` and before `mcpMgr.Start`.
- [x] 9.2 Update the startup log line listing `base_tools` to reflect the new order. *(Existing log emits `base_tools` from the registry manifest; no code change needed — the new tool joins the list automatically.)*

## 10. main-agent tests

- [x] 10.1 Extend `cmd/main-agent/loop_test.go` to register `bash` and assert the schema order is `list_skills, read_skill, read_skill_file, current_time, run_skill_script, bash`.
- [x] 10.2 New test in `gating_test.go` (mirror `TestRunSkillScript_EnvelopeReturnsInToolResult`): turn with a `bash` tool call, stub exec with `httptest.NewServer` returning a scripted bash envelope, assert envelope verbatim in `ChatResponsePayload.ToolResults[0].Output`.
- [x] 10.3 New test: session_id injection for bash — model passes `session_id: "attacker"`, main-agent overrides to the turn's real session; verified via the captured exec request body.
- [x] 10.4 New test: bash exec-unavailable surfaces the `exec unavailable` envelope to the model.

## 11. Compose + operational

- [x] 11.1 Add `/sandbox` tmpfs to the `exec` service in `docker-compose.yml`: `/sandbox:size=128m,mode=1777` (same Podman-compatible mode as `/workspace`).
- [x] 11.2 Add `EXEC_SANDBOX_RETENTION_MINUTES=${EXEC_SANDBOX_RETENTION_MINUTES:-60}` to the exec service environment.
- [x] 11.3 Verify `docker compose config --quiet` exits 0.
- [x] 11.4 Live smoke: `docker compose up -d --build exec main-agent`; from inside main-agent, `curl -s -X POST http://exec:8085/v1/bash ... '{"command":"echo hi && touch persist.txt","session_id":"smoke"}'` returns `exit_code:0`; a second call `{"command":"ls","session_id":"smoke"}` shows `persist.txt` still present. *(Verified: first call produced `exit_code:0, stdout` with "hello" + full dir listing, sandbox_dir `/sandbox/smoke`; second `ls` returned `persist.txt` — persistence across calls confirmed.)*
- [x] 11.5 Live end-to-end through the chat flow: a user message "run 'uname -a' in a sandbox" produces a `bash` tool call and the model summarizes the kernel info. *(Verified via gateway: "Use the bash tool to run uname -a" → model returned Linux kernel info for the exec container `489153f22ce6` running `6.17.7-300.fc43.aarch64`.)*

## 12. Documentation

- [x] 12.1 Update `cmd/exec/README.md` with the new `/v1/bash` endpoint example + the sandbox retention semantics.
- [x] 12.2 Update `skills/README.md` with a short "Authoring skills in the sandbox" section — explains that agents can draft skills in `/sandbox/<session>/` via `bash` and operators copy finished artifacts to `skills/` manually.
- [x] 12.3 Add a pointer in `docs/skills-runtime-design.md` §9 noting that the previously-deferred "agent authoring skills" capability has landed as the `bash` tool; link the new section.

## 13. Quality gates

- [x] 13.1 `go vet ./...` clean.
- [x] 13.2 `go test ./...` passes.
- [x] 13.3 `go test -race ./internal/exec/... ./internal/execclient/... ./internal/tools/... ./cmd/main-agent/...` passes.
- [x] 13.4 Container image rebuild produces no apt diff (no new packages added) — only the Go binary changes.
- [x] 13.5 Log-spec check: every `exec_bash_*` log line matches the fields specified in the `code-execution` capability's Bash observability requirement.
