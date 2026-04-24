## 1. Session state package

- [x] 1.1 Create `internal/sessionskill/sessionskill.go` with `Key(sessionID) string`, `Get(ctx, rdb, sessionID) (string, error)`, `Set(ctx, rdb, sessionID, name string, ttl time.Duration) error`.
- [x] 1.2 `Get` SHALL return `""` with a nil error when the key is missing (treats missing as "no active skill"). Only non-nil errors are Valkey failures.
- [x] 1.3 `Set` with empty `name` SHALL `DEL` the key (explicit clear). Non-empty `name` SHALL use `SET` with the given TTL.
- [x] 1.4 Unit tests in `internal/sessionskill/sessionskill_test.go` using `miniredis`: roundtrip, missing-key returns empty, empty-name clears, TTL honored, error on Valkey failure.

## 2. Registry: SchemasFor

- [x] 2.1 Add `Registry.SchemasFor(base []string, allowed []string) []ToolSchema` in `internal/tools/tool.go`. Must preserve registration order, union by set membership, silently ignore names that are not registered.
- [x] 2.2 Keep `Registry.Schemas()` unchanged; add a short doc comment noting it returns the unfiltered slice.
- [x] 2.3 Unit tests in `internal/tools/registry_test.go` (or new file) covering: empty base + empty allowed returns empty; base-only returns base in registration order; disjoint base and allowed union correctly; overlapping base/allowed deduplicates; unknown names in both base and allowed are ignored; returned order matches registration order even when argument order differs.

## 3. current_time built-in

- [x] 3.1 Create `internal/tools/current_time.go` with `NewCurrentTime() Tool`.
- [x] 3.2 Schema: name `current_time`, description emphasizing UTC and optional Go layout, optional string `format` parameter (not required).
- [x] 3.3 `Invoke`: parse args, default `format` to `time.RFC3339`, return `time.Now().UTC().Format(layout)` as a plain string; on malformed JSON, return `{"error":"invalid arguments: <detail>"}`.
- [x] 3.4 Unit tests in `internal/tools/current_time_test.go`: default format returns RFC3339 UTC; custom format respected; malformed args yields error envelope; empty format string falls back to default.

## 4. Main-agent: base toolset + active-skill plumbing

- [x] 4.1 In `cmd/main-agent/main.go`, register `tools.NewCurrentTime()` after `read_skill_file` and before `mcpMgr.Start(...)`.
- [x] 4.2 After `mcpMgr.Start(...)` returns, compute the base toolset as the list of tool names returned by `toolRegistry.Manifest()` (which is registration order). Hold this as a package-level or main-local `baseTools []string`.
- [x] 4.3 Pass `baseTools` and the Valkey client to `handleRequest` (extend signature or a small Deps struct).
- [x] 4.4 In `handleRequest`, at turn start: `activeName, _ := sessionskill.Get(ctx, rdb, payload.SessionID)` — compute `activeSkill` by `skillsStore.Get(activeName)`. If `!ok && activeName != ""`, clear Valkey and log `active_skill_changed` with `previous=activeName, active=""`.
- [x] 4.5 Emit one WARN per unknown `allowed-tools` entry at turn start: iterate `activeSkill.AllowedTools`, log `skill_allowed_tool_unknown` for names not in the tool registry.

## 5. Main-agent: turn loop filtering and gating

- [x] 5.1 Replace `toolRegistry.Schemas()` in `handleRequest`'s iteration body with `toolRegistry.SchemasFor(baseTools, activeAllowed)` where `activeAllowed` is `activeSkill.AllowedTools` or nil.
- [x] 5.2 Build `visibleSet := map[string]bool` from `base ∪ activeAllowed` (only names that are registered). Use it to gate before `Registry.Invoke`.
- [x] 5.3 Replace the current per-tool-call invoke with: if `!visibleSet[call.Function.Name]`, emit `tool_invoked outcome=gated tool_name active_skill`, construct the gated error envelope, append the tool-result without calling Invoke. Else invoke as before.
- [x] 5.4 Add `active_skill` field to the existing `tool_invoked` INFO log line (empty string when no skill active).
- [x] 5.5 Post-tool-call: if `call.Function.Name == "read_skill"` and the registry result's `Outcome == "ok"`, parse `name` from `call.Function.Arguments`, verify the name is non-empty AND present in the store, then call `sessionskill.Set` with 24h TTL and emit `active_skill_changed` INFO log (with `previous_skill` from the local state). Update local `activeSkill` and `visibleSet` so iteration N+1 sees the new active skill.

## 6. Main-agent: tests

- [x] 6.1 Extend `cmd/main-agent/loop_test.go` (or add a new test file) covering: registry schema order now `list_skills, read_skill, read_skill_file, current_time`; `SchemasFor(base, nil)` with base = those four returns exactly them.
- [x] 6.2 New test: handleRequest with no active skill uses base-only schemas; assert the broker stub observed tools list contains exactly base names.
- [x] 6.3 New test: session has an active skill whose `allowed-tools` includes a new registered test tool; assert that tool appears in the schemas sent to the broker.
- [x] 6.4 New test: iteration 1 calls `read_skill("demo")` successfully; iteration 2's schemas include demo's `allowed-tools`; Valkey key `session:<id>:active-skill` is set to `demo` with TTL ≤ 24h and > 0.
- [x] 6.5 New test: iteration 1 calls `read_skill("missing")` returning `{"error":"skill not found: missing"}`; active-skill state unchanged.
- [x] 6.6 New test: model calls a tool that's not in the visible set; assert result contains `"tool not available under active skill"`, no invocation occurred (no side effects in a mock tool), and `tool_invoked outcome=gated` is logged.
- [x] 6.7 New test: skill's `allowed-tools` references an unknown tool name; WARN log emitted at turn start; filtered schemas do not include the unknown name.
- [x] 6.8 New test: active skill disappears from skills store across a turn (simulate by resetting store); Valkey key is cleared, schemas revert to base, `active_skill_changed` INFO logged with `previous=foo active=""`.

## 7. Integration

- [x] 7.1 Smoke: run main-agent against a populated skills store with the existing code-review skill. Via direct Invoke, confirm: (a) without activation, `current_time` and `list_skills` are in schemas; (b) after `read_skill("code-review")`, schemas unchanged (code-review's `allowed-tools` is absent/empty, so active set == base); (c) Valkey key appears.
- [x] 7.2 Smoke with a crafted test skill whose frontmatter has `allowed-tools: [current_time]` (or similar real tool): verify the tool is in schemas and a made-up name in `allowed-tools` produces a WARN and is not in schemas.
- [x] 7.3 `go vet ./...` and `go test ./...` pass.

## 8. Documentation

- [x] 8.1 Update `skills/README.md` to document that `allowed-tools` is now honored at runtime: if set, the skill's visible tools are the base set plus the listed tools; omitted/empty means "base only." Mention that unknown tool names in `allowed-tools` are silently ignored (with a WARN log).
- [x] 8.2 No changes needed to `docs/skills-runtime-design.md` — §4.3 already covers the base toolset and §5.1 covers the trust model; this change implements what the doc specifies.
