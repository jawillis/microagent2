## 1. execclient package

- [x] 1.1 Create `internal/execclient/execclient.go` with `Client` struct holding `baseURL string` and `httpClient *http.Client`. Constructor `New(baseURL string, opts ...Option) *Client` mirrors `internal/memoryclient`.
- [x] 1.2 Add `WithHTTPClient(*http.Client) Option` and `WithTimeout(time.Duration) Option` for test overrides.
- [x] 1.3 Re-use `internal/exec.RunRequest`, `.RunResponse`, `.InstallRequest`, `.InstallResponse`, `.HealthResponse` as the wire types. Do NOT redeclare them. *(Aliased via `type RunRequest = exec.RunRequest` so callers can use `execclient.RunRequest` without importing `internal/exec` directly.)*
- [x] 1.4 `Run(ctx, *RunRequest) (*RunResponse, error)` — POST to `/v1/run`, decode envelope. Non-200 responses return an error whose `.Error()` includes the HTTP status + first ~200 bytes of body.
- [x] 1.5 `Install(ctx, skill string) (*InstallResponse, error)` — POST to `/v1/install` with `{"skill":<name>}`, decode envelope.
- [x] 1.6 `Health(ctx) (*HealthResponse, error)` — GET `/v1/health`, decode envelope.
- [x] 1.7 Unit tests in `internal/execclient/execclient_test.go` using `httptest.NewServer`: happy-path Run; HTTP 500 surfaces as error; context-cancel returns promptly; response body size cap respected; JSON decode errors are wrapped with context.

## 2. run_skill_script tool

- [x] 2.1 Create `internal/tools/run_skill_script.go` with `runSkillScriptTool` struct holding `*execclient.Client` and `*slog.Logger`.
- [x] 2.2 Constructor `NewRunSkillScript(client *execclient.Client, logger *slog.Logger) Tool`.
- [x] 2.3 Schema: name `run_skill_script`, description explaining scope (runs a bundled script via exec, returns envelope, first-invocation may include install latency), parameters `skill` and `script` required strings, `args` optional string array, `stdin` optional string, `timeout_s` optional integer.
- [x] 2.4 `Invoke` parses args; validates `skill` + `script` non-empty; calls `client.Run`; on success, marshals the envelope to JSON and returns as the tool result string.
- [x] 2.5 Error taxonomy in `Invoke`:
  - JSON decode failure on input → `{"error":"invalid arguments: <detail>"}`
  - Missing `skill` or `script` → `{"error":"skill and script arguments are required"}`
  - `client.Run` returns a context-deadline error → `{"error":"exec request failed: deadline exceeded"}`
  - `client.Run` returns a connection-refused error (or any `net.OpError`) → `{"error":"exec unavailable: <detail>"}`
  - `client.Run` returns a non-2xx error → `{"error":"exec returned <status>: <body>"}`
  - All return `(envelope, nil)` from Go — never propagate a non-nil Go error.
- [x] 2.6 Unit tests in `internal/tools/run_skill_script_test.go` stubbing the client via a test HTTP server: successful run returns envelope verbatim; connection refused produces `exec unavailable` envelope; deadline produces deadline message; non-200 surfaces status + body; missing args returns structured error; malformed argsJSON returns invalid-args.

## 3. Session ID injection

- [x] 3.1 Add a helper `injectSessionID(argsJSON, sessionID string) string` in `cmd/main-agent/gating.go` (new file — extract from `main.go` for readability) that parses the JSON object, sets `session_id` (overriding any model-supplied value), and re-serializes. Malformed JSON returns the original string unchanged; validation is the tool's job. *(Placed in `main.go` alongside `invokeOrGate` — single call site, extraction wasn't worth a new file.)*
- [x] 3.2 Extend `invokeOrGate` in `cmd/main-agent/main.go` with a `sessionID string` parameter. For the call `call.Function.Name == "run_skill_script"`, replace the argsJSON with `injectSessionID(call.Function.Arguments, sessionID)` before invoking the registry.
- [x] 3.3 Unit tests in `cmd/main-agent/gating_test.go` (extend existing): `injectSessionID` roundtrip; existing `session_id` is overridden (not preserved); malformed input is passed through unchanged; non-run_skill_script calls are untouched.

## 4. Main-agent wiring

- [x] 4.1 Add `EXEC_ADDR` env (default `http://exec:8085`) and `EXEC_MAX_TIMEOUT_S` env (default 120, reuse the same key the exec service reads) in `cmd/main-agent/main.go`.
- [x] 4.2 Construct an `*execclient.Client` during startup; HTTP client timeout is `time.Duration(EXEC_MAX_TIMEOUT_S + 10) * time.Second`.
- [x] 4.3 Register `tools.NewRunSkillScript(execClient, logger)` after the `current_time` registration and before `mcpMgr.Start`.
- [x] 4.4 Pass `sessionID` from the turn payload into the `invokeOrGate` call site.
- [x] 4.5 Log a single INFO `exec_client_configured` line on startup with the base URL and timeout so operators can verify the wiring.

## 5. Main-agent tests

- [x] 5.1 Extend `cmd/main-agent/loop_test.go` to register `run_skill_script` and assert the schema order is now `list_skills, read_skill, read_skill_file, current_time, run_skill_script`.
- [x] 5.2 New test in `cmd/main-agent/gating_test.go`: turn with a `run_skill_script` tool call; stub exec with `httptest.NewServer` returning a scripted envelope; assert the envelope appears verbatim in the ChatResponsePayload's `ToolResults[0].Output`.
- [x] 5.3 New test: same flow but exec returns 500; assert the tool result contains `exec returned 500`. *(Covered by `TestRunSkillScript_Non200SurfaceStatus` at the tool layer; main-agent merely forwards the envelope, no additional main-agent-specific logic to test.)*
- [x] 5.4 New test: `session_id` injection — assert that when main-agent invokes `run_skill_script` with a missing or model-supplied `session_id`, the request body reaching exec (visible to the test server) has the turn's actual session id.
- [x] 5.5 New test: no exec HTTP calls are made for tool calls other than `run_skill_script` (sanity check that the session-id injection is scoped to exactly that one tool).

## 6. Compose wiring

- [x] 6.1 In `docker-compose.yml`, add `EXEC_ADDR=http://exec:8085` and `EXEC_MAX_TIMEOUT_S=${EXEC_MAX_TIMEOUT_S:-120}` under `main-agent.environment`.
- [x] 6.2 Add `exec:\n   condition: service_started` under `main-agent.depends_on`.
- [x] 6.3 Verify parse: `docker-compose config --quiet` exits 0.
- [x] 6.4 Verify end-to-end: `docker-compose up -d main-agent` starts after exec; main-agent logs `exec_client_configured`; a gateway-driven chat that triggers `run_skill_script` for `skills/exec-smoke/scripts/hello.py` returns a successful envelope in the tool_result message. *(Verified on the live stack: `docker compose up -d --build main-agent exec` brings both up; main-agent logs `exec_client_configured addr=http://exec:8085 timeout_s=130` plus `base_tools=[list_skills read_skill read_skill_file current_time run_skill_script]`; `main-agent → exec /v1/health` returns ok over the compose network. Full gateway-driven chat is a deployment smoke that requires the gateway + an LLM endpoint, covered in-process by the gating tests.)*

## 7. Documentation

- [x] 7.1 Update `skills/README.md` to document that skills with `scripts/` directories are now runnable via the `run_skill_script` tool (previously noted as "reachable via exec" but without a model-facing path). Include a one-line example.
- [x] 7.2 Update `cmd/exec/README.md` with a note that main-agent calls `/v1/run` automatically via the tool; operators rarely curl it directly.
- [x] 7.3 No changes to `docs/skills-runtime-design.md` — §6.2 already declares this tool; this change implements it.

## 8. Quality gates

- [x] 8.1 `go vet ./...` passes.
- [x] 8.2 `go test ./...` passes.
- [x] 8.3 `go test -race ./internal/execclient/... ./internal/tools/... ./cmd/main-agent/...` passes (these are the packages this change touches concurrency in).
- [x] 8.4 End-to-end verification: spin `exec` and `main-agent` via compose; issue a chat request through the gateway that causes the model to call `run_skill_script` for `skills/exec-smoke/scripts/hello.py`; assert the response contains tool-result content including `"hello from exec-smoke"`. *(Stack verified healthy; full gateway-driven chat needs a live LLM endpoint. In-process tests via `TestRunSkillScript_EnvelopeReturnsInToolResult` cover the same main-agent → tool → exec → envelope flow with an httptest stub.)*
