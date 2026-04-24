## 1. Skills store package

- [x] 1.1 Add `gopkg.in/yaml.v3` to `go.mod` (run `go get gopkg.in/yaml.v3`)
- [x] 1.2 Create `internal/skills/manifest.go` with `Manifest` struct (`Name`, `Description`, `AllowedTools []string`, `Model string`, `body string`, `sourcePath string`) and an unexported `parseFrontmatter(contents []byte) (fm, body string, err error)` helper that splits on `---\n` delimiters (supports both LF and CRLF)
- [x] 1.3 Create `internal/skills/store.go` with `Store` struct, `NewStore(root string) (*Store, error)` that scans `root` one-level deep for subdirs containing `SKILL.md`, parses frontmatter, validates `name`/`description` non-empty, caches manifests
- [x] 1.4 Implement `Store.List() []*Manifest` returning manifests sorted alphabetically by Name; `Store.Get(name) (*Manifest, bool)`; `Store.Body(name) (string, bool)`
- [x] 1.5 Missing/unreadable root logs WARN `skills_dir_unreadable` and returns an empty store (non-fatal); invalid frontmatter or missing required fields logs WARN `skill_manifest_invalid` or `skill_frontmatter_parse_failed` and skips that directory
- [x] 1.6 On successful init, log INFO `skills_store_initialized` with `{root, skill_count, skipped_count}`
- [x] 1.7 Unit tests `internal/skills/store_test.go`: (a) valid skill dir parses correctly, (b) missing name field skipped with WARN, (c) malformed YAML frontmatter skipped with WARN, (d) non-existent root yields empty store with no error, (e) `List()` returns alphabetical order, (f) `Body(name)` returns exact markdown bytes with trimmed leading blank line after frontmatter, (g) `allowed-tools` and `model` fields parsed when present
- [x] 1.8 Create `./skills/README.md` documenting the directory format (frontmatter fields, body expectations)

## 2. Tool registry and interface

- [x] 2.1 Create `internal/tools/tool.go` with `Tool` interface (`Name() string`, `Schema() messaging.ToolSchema`, `Invoke(ctx, argsJSON) (string, error)`)
- [x] 2.2 Create `Registry` struct with `Register(t Tool) error` (error on name collision), `Schemas() []messaging.ToolSchema` (deterministic insertion order), `Invoke(ctx, name, args) (string, error)`, `Manifest() []ManifestEntry` (for system-prompt injection convenience)
- [x] 2.3 `Registry.Invoke` wraps the tool's `Invoke` with: (a) unknown-tool → return `{"error":"unknown tool: <name>"}` (nil err), (b) tool returns err → return `{"error":"<err>"}` (nil err), (c) panic recovery → log ERROR `tool_panic` and return `{"error":"tool panicked"}` (nil err)
- [x] 2.4 Unit tests `internal/tools/registry_test.go`: (a) duplicate Register returns error, (b) Schemas order matches registration, (c) unknown-tool produces structured error, (d) tool-returned-err produces structured error, (e) panic recovered and produces structured error, (f) successful invocation passes args through and returns string unchanged

## 3. Built-in skills-facing tools

- [x] 3.1 Create `internal/tools/builtin_skills.go` with `NewListSkills(store *skills.Store) Tool` — Schema returns no-args function, Invoke marshals `store.List()` to `[{"name","description"}]`
- [x] 3.2 Add `NewReadSkill(store *skills.Store) Tool` — Schema requires `name` string, Invoke decodes args as `{Name string}`, returns body or JSON error
- [x] 3.3 Tool descriptions in schemas use authoritative, declarative framing: list_skills = "List all available skills…" and read_skill = "Load the full instructions for a named skill and apply them…"
- [x] 3.4 Unit tests `internal/tools/builtin_skills_test.go`: (a) list_skills on empty store returns `[]`, (b) list_skills returns populated catalog in deterministic order, (c) read_skill hit returns exact body, (d) read_skill miss returns structured error, (e) read_skill empty name returns structured error, (f) read_skill malformed JSON returns structured error

## 4. Messaging wire-format extensions

- [x] 4.1 In `internal/messaging/payloads.go`, add `ToolResult` struct `{CallID string "json:call_id"; Output string "json:output"}`
- [x] 4.2 Extend `ChatResponsePayload` with `ToolResults []ToolResult "json:tool_results,omitempty"`
- [x] 4.3 Add `ToolResultPayload` struct `{SessionID, CallID, Output string}` for pub/sub events
- [x] 4.4 Add `TypeToolResult = "tool_result"` constant in `internal/messaging/message.go` next to `TypeToolCall`
- [x] 4.5 Unit tests in `internal/messaging/payloads_test.go`: (a) `ChatResponsePayload` with empty ToolResults omits the field in JSON, (b) `ToolResult` JSON keys are `call_id` and `output`, (c) `ToolResultPayload` round-trips via NewMessage/DecodePayload

## 5. Main-agent tool loop + skill manifest injection

- [x] 5.1 Edit `cmd/main-agent/main.go`: instantiate `skills.Store` from `SKILLS_DIR` env (default `./skills/`) and register two built-in tools into a `tools.Registry`
- [x] 5.2 Read `TOOL_LOOP_MAX_ITER` env (default 10) in main
- [x] 5.3 Add a `injectSkillManifest(messages, registry)` helper that appends `"\n\n<available_skills>\n- <name>: <desc>\n...\n</available_skills>"` to the first `system`-role message's content when registry has skills-facing tools — no-op when empty; operates on a copied slice so caller's slice is untouched
- [x] 5.4 Rewrite `handleRequest` around a bounded tool loop: `for iter := 0; iter < maxIter; iter++ { slot = RequestSlot(); content, toolCalls, err = Execute(messages, tools, onToken, onToolCall); ReleaseSlot(); if preempted/err: return; if no toolCalls: break; for each call: invoke via registry, append assistant(tool_calls) + tool(tool_call_id, output), publish tool_result pub/sub event, append to local toolCalls/toolResults accumulator }`
- [x] 5.5 Ensure slot is released BEFORE invoking tools (iteration-scoped slot hold)
- [x] 5.6 On `tool_loop_max_iter_hit`: log at WARN with `{correlation_id, iterations}`; append `"\n(max iterations reached)"` to the final assistant text
- [x] 5.7 At turn end, publish a single `ChatResponsePayload` on the reply stream with final `Content`, full `ToolCalls` accumulator, and full `ToolResults` accumulator
- [x] 5.8 Keep the existing pub/sub token flow; only add tool-result pub/sub publishes — new helper `onToolResult(call ToolCall, output string)` that publishes `TypeToolResult` to `channel:tool-calls:{session_id}`
- [x] 5.9 Log `tool_invoked` at INFO per tool call with `{correlation_id, tool_name, args_bytes, elapsed_ms, outcome, result_bytes}`

## 6. History reconstruction updates

- [x] 6.1 Edit `internal/response/store.go` `GetSessionMessages`: when iterating `resp.Output`, add a branch for `out.Type == "function_call_output"` that emits `ChatMsg{Role: "tool", Content: out.Output, ToolCallID: out.CallID}`
- [x] 6.2 Edit `internal/gateway/responses.go` `chainToMessages`: same extension — `function_call_output` in Output maps to a `role: "tool"` ChatMsg
- [x] 6.3 Unit test `internal/response/store_tools_test.go`: add a case where Output contains interleaved function_call / function_call_output / message items, and verify `GetSessionMessages` yields the correct `ChatMsg` sequence with ordering preserved
- [x] 6.4 Unit test `internal/gateway/tool_calls_test.go`: add a case asserting `chainToMessages` handles function_call_output in Output

## 7. Gateway persistence of tool_calls + tool_results

- [x] 7.1 Edit `handleResponsesNonStreaming` in `internal/gateway/responses.go`: after decoding `ChatResponsePayload`, build interleaved OutputItems: for each `ToolCalls[i]`, append `function_call{CallID, Name, Args}`; for the matching `ToolResults[i]` (matched by CallID), append `function_call_output{CallID, Output}`; finally append the `message` assistant text item. Order: call/result pairs first, then final text
- [x] 7.2 Edit `handleResponsesStreaming` in `internal/gateway/responses.go`: maintain per-turn accumulator for both tool_calls (existing) and tool_results (new); the existing `tcSub` select branch grows a type switch — `TypeToolCall` keeps current behavior; `TypeToolResult` decodes into `ToolResultPayload`, appends `function_call_output` item, and emits `event: response.tool_result` SSE event with `{response_id, call_id, output}` data
- [x] 7.3 At stream completion (token done branch), append the collected function_call_output items alongside function_call items in the stored response's Output array, preserving arrival order
- [x] 7.4 Log `gateway_tool_result_relayed` at INFO on every tool-result SSE event with `{correlation_id, session_id, call_id, output_bytes}`
- [x] 7.5 E2E test in `internal/gateway/tool_calls_e2e_test.go`: non-streaming turn where agent reply includes ToolCalls + ToolResults → response body has interleaved function_call/function_call_output/message items; pure-text turn regression-free

## 8. Docker and operator plumbing

- [x] 8.1 Edit `docker-compose.yml`: mount `./skills/` into the main-agent container at a stable path; set `SKILLS_DIR=/app/skills` (or equivalent) in the environment block
- [x] 8.2 Edit `.env.example`: add `SKILLS_DIR` and `TOOL_LOOP_MAX_ITER` with commented defaults
- [x] 8.3 Create `./skills/` directory at repo root with a `.gitkeep` or the README authored in 1.8 so operators find it

## 9. Dashboard session view sanity

- [x] 9.1 Verify the session-view UI from slice 1 already renders stored tool_calls and role=tool messages correctly; if history reconstruction now returns interleaved items, confirm the rendering order looks right in a manual load of a seeded session with tool activity
- [x] 9.2 No new UI code expected; this task exists as a manual verification gate before calling the slice done

## 10. End-to-end verification

- [x] 10.1 Integration-style unit test: stub llama.cpp emitting tool_calls for `list_skills` on turn 1 and final text on turn 2; assert the tool loop runs two iterations, `list_skills` is invoked, and the final response body's Output contains `function_call`, `function_call_output`, `message` items in that order
- [x] 10.2 Regression test: pure-text turn behavior is byte-identical to slice-1 baseline — same message flow, same SSE event kinds, same stored shape
- [x] 10.3 Run full test suite `go test ./...` — all green
- [x] 10.4 Run `go vet ./...` — clean
- [x] 10.5 Build all binaries `go build ./cmd/...` — zero compilation errors
- [x] 10.6 Author one demonstration skill at `./skills/example-skill/SKILL.md` with trivial instructions, boot the stack with `docker compose up`, ask the model "what skills are available?" and confirm it calls `list_skills`. Then ask "use the example-skill" and confirm it calls `read_skill` and applies the instructions. Document the outcome in a brief note (conversation log) — not a formal test, but a manual smoke gate
