## 1. Skills store: root tracking

- [x] 1.1 Add unexported `rootDir` field to `Manifest` in `internal/skills/manifest.go` and a public `Root() string` getter.
- [x] 1.2 In `internal/skills/store.go:NewStore`, set `rootDir` to `filepath.Dir(skillPath)` when constructing each `Manifest`. Do not resolve symlinks here — resolution happens at file-access time.
- [x] 1.3 Add a unit test in `internal/skills/store_test.go` asserting `manifest.Root()` equals the expected absolute directory for a scanned skill.

## 2. Skills store: ReadFile method

- [x] 2.1 Add `Store.ReadFile(name, relPath string) (contents string, found bool, err error)` in `internal/skills/store.go`.
- [x] 2.2 Implement the validation sequence per `design.md` Decision 3: absolute-path reject, `filepath.Clean` + `..` reject, reserved-`SKILL.md` reject, join + `EvalSymlinks` on target and root, prefix-compare with trailing separator, regular-file check, size-cap check.
- [x] 2.3 Translate `os.IsNotExist` into a structured "file not found within skill" error, distinct from the `found == false` "unknown skill" case.
- [x] 2.4 Add `SKILL_FILE_MAX_BYTES` env parsing at `NewStore` (positive int, default 262144, invalid values fall back to default). Store the cap on the `Store` struct for `ReadFile` to consult.
- [x] 2.5 Log a WARN line when `SKILL_FILE_MAX_BYTES` is set but unparsable, matching the existing `logger.Warn` patterns in `NewStore`.

## 3. Skills store: tests

- [x] 3.1 Test: `ReadFile("nonexistent", ...)` returns `("", false, nil)`.
- [x] 3.2 Test: valid relative path returns full contents.
- [x] 3.3 Test: absolute path rejected with non-nil error.
- [x] 3.4 Test: `../` traversal rejected without filesystem access (use an external file outside the skill root and assert it is never read, e.g. via fs permissions check or a canary file).
- [x] 3.5 Test: `SKILL.md` rejected with an error that names `read_skill` as the correct tool.
- [x] 3.6 Test: symlink inside the skill tree resolves and returns contents.
- [x] 3.7 Test: symlink whose target is outside the skill tree is rejected with an error mentioning escape/root.
- [x] 3.8 Test: reading a directory path is rejected with a non-regular-file error.
- [x] 3.9 Test: oversize file rejected; file is not fully read into memory (use `os.Stat`-driven assertion).
- [x] 3.10 Test: nonexistent file within a valid skill returns `("", true, err)` with a "file not found" message.
- [x] 3.11 Test: `SKILL_FILE_MAX_BYTES` default applied when env unset.
- [x] 3.12 Test: `SKILL_FILE_MAX_BYTES` honored when set to a custom value.
- [x] 3.13 Test: invalid `SKILL_FILE_MAX_BYTES` value logged and default applied.

## 4. Built-in tool: read_skill_file

- [x] 4.1 Add `NewReadSkillFile(store *skills.Store) Tool` constructor in `internal/tools/builtin_skills.go`.
- [x] 4.2 Implement the tool: name `read_skill_file`, schema with required `skill` and `path` string parameters, description emphasizing relative-path usage.
- [x] 4.3 In `Invoke`, parse args, validate both fields non-empty, call `store.ReadFile`, and translate the three-valued result per `design.md` Decision 7 and `specs/tool-invocation/spec.md` table.
- [x] 4.4 Use the existing `jsonError` helper so wire-format error envelopes match `list_skills` and `read_skill`.

## 5. Main-agent registration

- [x] 5.1 In `cmd/main-agent/main.go`, register `tools.NewReadSkillFile(skillsStore)` immediately after `read_skill` (and before `mcpMgr.Start`, which registers MCP-sourced tools).
- [x] 5.2 Surface registration errors via the existing `logger.Error` + `os.Exit(1)` pattern used for `list_skills` and `read_skill`.

## 6. Tool-layer tests

- [x] 6.1 Test in `internal/tools/builtin_skills_test.go`: successful read returns exact bytes.
- [x] 6.2 Test: unknown skill returns `{"error":"skill not found: ..."}` envelope.
- [x] 6.3 Test: malformed JSON arguments return `{"error":"invalid arguments: ..."}`.
- [x] 6.4 Test: missing/empty `skill` or `path` arguments return an argument-required error.
- [x] 6.5 Test: path rejection error messages pass through from the store layer into the tool envelope.
- [x] 6.6 Test: `SKILL.md` rejection via tool returns the redirect-to-`read_skill` message.
- [x] 6.7 Test in `cmd/main-agent/loop_test.go`: extend the existing registry setup to include `read_skill_file` and assert the schema order is `list_skills, read_skill, read_skill_file`.

## 7. Integration check

- [x] 7.1 With the existing `skills/code-review/` directory (which has `SKILL.md` and `language-notes.md`), run main-agent locally and verify `list_skills` lists code-review and `read_skill_file("code-review","language-notes.md")` returns the expected markdown through the agent loop.
- [x] 7.2 Verify that adding a symlink loop or a traversal path in `skills/test-skill/` is rejected at invocation time without crashing or leaking file contents.
- [x] 7.3 Verify `go vet ./...` and `go test ./...` pass.

## 8. Documentation

- [x] 8.1 Update `skills/README.md` with a short section on progressive disclosure: how to reference a supplementary file in SKILL.md and that the model calls `read_skill_file` to load it.
- [x] 8.2 No changes to `docs/skills-runtime-design.md` are needed — that doc already describes this change as part of the sequence and uses `read_skill_file` as the intended tool name.
