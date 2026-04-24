## Context

This is change 1 of a four-change sequence toward full Anthropic agent-skills runtime parity. The cross-cutting contract — activation semantics, security model, exec service shape, change ordering, deferred questions — is defined in `docs/skills-runtime-design.md`. This document covers only the specifics of preserving skill subdirectories and enabling bounded file access within a skill root. It does not re-derive the broader rationale; §2 of the shared doc establishes why the target is Anthropic format parity, and §4.4 sketches the progressive-disclosure layering that this change partially implements.

Current state:
- `internal/skills/store.go:NewStore` scans `SKILLS_DIR` one level deep, reads each immediate subdirectory's `SKILL.md`, parses frontmatter and body, and stores a `Manifest{Name, Description, AllowedTools, Model, body, sourcePath}` indexed by name.
- `Manifest.sourcePath` is the path to the `SKILL.md` file, not the skill root directory. The store exposes `List`, `Get`, `Body` — no way to reach other files in the skill directory.
- `internal/tools/builtin_skills.go` defines two tools: `list_skills` returns the catalog manifest, `read_skill` returns the body. No tool exists for reading any other file in the skill tree.
- On disk, `skills/code-review/` today has `SKILL.md` and `language-notes.md` as siblings. The SKILL.md references `language-notes.md` but the model cannot actually fetch it through any tool — it can only read the body and guess at what the referenced doc would say.

Constraints:
- Must not break the existing `skills-store` or `tool-invocation` spec requirements. All current scenarios continue to pass byte-identically.
- Single-process scope. No new services, no container work, no HTTP. Matches shared doc §8: change 1 is self-contained and lands independently of changes 2-4.
- Path handling must be safe against traversal, symlink escape, and absolute-path attacks. Skill files live in a trusted location (checked into VCS) but the arguments passed to `read_skill_file` come from the LLM, which is untrusted input.

## Goals / Non-Goals

**Goals:**
- Record the skill root directory for each discovered skill.
- Expose a sandboxed `Store.ReadFile(name, relPath) (contents string, found bool, err error)` method that resolves `relPath` strictly within the skill root.
- Register a new built-in tool `read_skill_file(skill, path)` that wraps `Store.ReadFile` and conforms to the existing `Tool` interface.
- Keep built-in tool registration ordered before MCP-sourced tools so `Registry.Schemas()` remains stable: `list_skills, read_skill, read_skill_file, <mcp tools>`.
- Bound file reads to a size cap; oversize reads return a structured error, not a truncated blob.
- Preserve identical behavior of `list_skills`, `read_skill`, frontmatter parsing, and `<available_skills>` injection.

**Non-Goals:**
- Honoring `allowed-tools` at runtime — deferred to change 2 (`skills-tool-gating`).
- Defining active-skill or session-scoped registry filtering — deferred to change 2.
- Any script execution, process spawning, or container work — deferred to changes 3 and 4.
- Writing into a skill directory or its workspace — this change is strictly read-only.
- Hot-reload of the skill catalog. Startup-only scan remains the contract; `skills-store` §"Scan is one-shot at startup" stays in force.
- Supporting skill content served from sources other than the filesystem (HTTP, git, registry). Out of scope for the whole series per shared doc §9.
- Reading binary files differently from text files. `read_skill_file` returns raw bytes as a string; if the model asks for a PNG, it gets raw bytes (which will not be useful) — but path validation is identical. Binary-aware handling is a later concern tied to change 4 output envelopes.

## Decisions

### Decision 1: Record the skill root in the `Manifest` struct

The existing `Manifest.sourcePath` points at `SKILL.md`. Add a new unexported field `rootDir` and a getter `Root()`. `rootDir` is the directory containing `SKILL.md` — i.e. `filepath.Dir(sourcePath)`.

Alternative considered: derive root from `sourcePath` at call time with `filepath.Dir`. Rejected because every call to `ReadFile` would repeat the derivation, and because an explicit `Root()` getter keeps the Manifest API self-documenting. Storing is effectively free (one string per skill).

### Decision 2: New method `Store.ReadFile(name, relPath) (string, bool, error)`

Signature mirrors `Store.Body(name)` but takes a relative path. Returns:
- `(contents, true, nil)` on success
- `("", false, nil)` when the skill name is unknown — same "not found" semantics as `Body`
- `("", true, err)` when the skill exists but the path is invalid (traversal, absolute, too large, outside root, not a regular file)

Distinguishing "skill not found" (`false, nil`) from "path rejected" (`true, err`) lets the tool layer translate each into a distinct JSON error string.

Alternative considered: single boolean + error. Rejected because the three outcomes (unknown skill / bad path / ok) are genuinely different and the caller wants to emit different error messages.

### Decision 3: Path safety model

Validation rules, enforced in `ReadFile` before any filesystem access:

1. Reject absolute paths (`filepath.IsAbs(relPath)`).
2. Normalize via `filepath.Clean`. Reject if the cleaned path starts with `..` or equals `..`.
3. Reject if the cleaned path contains a `..` segment after normalization (defensive — `Clean` should collapse these, but verifying guards against platform oddities).
4. Reject the path if it equals `SKILL.md` — that is the reserved frontmatter+body file and is served by `read_skill`, not `read_skill_file`. This avoids two tools with overlapping semantics for the same byte range.
5. Join to skill root: `fullPath := filepath.Join(rootDir, cleanPath)`.
6. Evaluate symlinks: `resolved, err := filepath.EvalSymlinks(fullPath)`.
7. Evaluate symlinks on the skill root: `rootResolved, err := filepath.EvalSymlinks(rootDir)`.
8. Verify `resolved` is under `rootResolved` by checking `strings.HasPrefix(resolved+sep, rootResolved+sep)`. Reject if not.
9. Stat the resolved path; reject if not a regular file (directories, devices, sockets, pipes all rejected).
10. Check file size; reject if larger than `SKILL_FILE_MAX_BYTES` (default 256 KB, env-overridable).

Rationale: the skill root itself could be a symlink on some setups (e.g. dev-time bind mounts). Resolving both sides before comparison handles that case. The cleaned-path `..` check is belt-and-suspenders against `filepath.Join` edge cases.

Alternative considered: reject symlinks entirely. Rejected because some skills may legitimately symlink shared docs (shared `LICENSE.txt`), and rejecting all symlinks would surprise authors. The chosen model lets symlinks exist inside the skill tree as long as the final resolved target is also inside the skill tree.

### Decision 4: Size cap

256 KB default, env variable `SKILL_FILE_MAX_BYTES`. Oversize returns a structured error `{"error":"file too large: <size> bytes, max <cap>"}` rather than truncating. Truncation silently loses content, which confuses both the model and human debuggers; an error lets the model decide (re-read a smaller file? ask for an alternative?).

Alternative considered: truncate with a marker. Rejected — inconsistent with `read_skill` (which returns full bodies) and with the broader design doc's stance on bounded-error-returns. Change 4 will handle truncation differently for exec outputs (there, truncation is expected; here, it is not).

### Decision 5: Tool name and schema

Tool name: `read_skill_file`. Schema:

```json
{
  "type": "object",
  "properties": {
    "skill": {"type": "string", "description": "Exact skill name as returned by list_skills"},
    "path":  {"type": "string", "description": "Relative path within the skill directory, e.g. reference/best_practices.md"}
  },
  "required": ["skill", "path"]
}
```

Description string emphasizes **relative path within the skill directory** to discourage the model from providing absolute paths. The path-safety logic in `Store.ReadFile` is the actual enforcement; the description is advisory.

Alternative considered: name `read_skill_resource` or `skill_read_file`. Rejected — `read_skill_file` parallels `read_skill`, is unambiguous in the tool manifest, and matches the likely Anthropic `read_skill_file` naming if they ship one in the future (format compatibility wins).

### Decision 6: Registration order

Registration order in `cmd/main-agent/main.go`:

```
list_skills
read_skill
read_skill_file    ← new, inserted here
<mcp tools>
```

Per `tool-invocation` spec §"MCP tool registration is additive to built-ins": built-ins first, MCP tools after. Slotting `read_skill_file` immediately after `read_skill` keeps discovery-order stable and groups skill-related tools together in `Registry.Schemas()` output.

### Decision 7: Errors are structured JSON at the tool layer, plain Go errors at the store layer

`Store.ReadFile` returns idiomatic Go `error` values with human-readable messages. The tool wrapper (`read_skill_file`) converts those into the existing `jsonError(msg string)` envelope used by `list_skills` and `read_skill`. This preserves a clean internal API while keeping the wire contract consistent with the other two built-in tools.

## Risks / Trade-offs

- **Risk:** Path validation logic is security-sensitive; a bug here could allow reading arbitrary files on the host. → Mitigation: unit tests covering each rejection branch (absolute path, `..` traversal, symlink out, non-regular file, oversize) plus a positive test for a valid symlink inside the root. Resolved-path comparison uses trailing-separator prefix check to avoid the `skills-foo` / `skills-foobar` confusion.

- **Risk:** `filepath.EvalSymlinks` returns an error if the path does not exist. This needs to produce a clean "file not found" error, not the raw OS error. → Mitigation: explicit check for `os.IsNotExist(err)` inside `ReadFile`, translating to a structured not-found message.

- **Risk:** Large skill directories with many files increase startup scan time. → Not applicable here — this change does not change the scan, only what is stored per skill (one extra string: the root path). File reads happen on-demand per tool call.

- **Risk:** Tool description overlap with `read_skill` may confuse the model — it might call `read_skill_file(skill, "SKILL.md")` instead of `read_skill(skill)`. → Mitigation: explicitly reject `path == "SKILL.md"` (Decision 3.4) with a structured error that names `read_skill` as the correct tool. Turns a UX footgun into a one-shot correction.

- **Risk:** Size cap of 256 KB might be too small for legitimate skill reference docs (some Anthropic skills have multi-hundred-KB markdown files). → Mitigation: env-configurable via `SKILL_FILE_MAX_BYTES`. Operators who need larger bump the cap. If 256 KB turns out to block common skills in practice, we raise the default in a follow-up.

- **Trade-off:** Deferring `allowed-tools` enforcement to change 2 means `read_skill_file` is available to every session regardless of active skill. This is consistent with the shared doc's "medium base toolset" decision (§4.3) but means we do not exercise the gating mechanism in this change. Acceptable because change 2 lands the gating contract explicitly; change 1 lands only the capability.

## Migration Plan

No migration required. This change is strictly additive:
- New `Manifest.rootDir` field is populated by `NewStore` for all discovered skills. Existing skills work unchanged.
- New `Store.ReadFile` method has no existing callers to update.
- New `read_skill_file` tool is registered at main-agent startup. If startup fails to register (unlikely, since it uses the same pattern as `read_skill`), main-agent exits at startup — no silent degradation.
- No config changes required; the env variable `SKILL_FILE_MAX_BYTES` is optional and has a sensible default.
- No rollback plan is needed because the change does not modify existing data, schemas, or external interfaces. A revert is a code-only operation.

## Open Questions

- Does `read_skill_file` need to log a per-invocation structured line separate from the generic `tool_invoked` log? The shared doc doesn't call this out. Likely `tool_invoked` is sufficient since it already carries `tool_name` and `args_bytes`. Revisit if operators report opacity.
- When a skill's root directory contains a dotfile (`.git/`, `.DS_Store`, `LICENSE.txt`), should `read_skill_file` hide those or return them transparently? Initial stance: transparent — skills are checked-in, and hiding dotfiles would complicate the mental model. If this becomes a problem (noise in LLM reasoning, or unintentional exposure of VCS internals), add a denylist in a follow-up. Not worth solving preemptively.
- Should we enumerate files in a skill (`list_skill_files(skill)`)? Not in this change — the shared doc does not include it and the progressive-disclosure model assumes the SKILL.md body names the resources it wants the model to read. Add if the pattern proves insufficient.
