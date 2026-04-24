## Why

microagent2's skills store today treats a skill as a single `SKILL.md` file — it scans one level deep, parses frontmatter and body, and ignores everything else in the skill's directory. Anthropic agent-skills, which we are targeting for runtime parity (see `docs/skills-runtime-design.md`), bundle reference documentation and helper resources in sibling subdirectories like `reference/` and `scripts/`. Without preserving and exposing those files, we cannot consume doc-heavy marketplace skills (`frontend-design`, `brand-guidelines`, `doc-coauthoring`, etc.) even though their SKILL.md files already parse cleanly in our format. This change unblocks progressive disclosure — the second layer of Anthropic's skill model — so a SKILL.md that says "See `reference/mcp_best_practices.md`" can be followed up by the model via a new `read_skill_file` tool.

## What Changes

- Skills store records each skill's root directory (not just the parsed SKILL.md contents) and exposes a method to read files at paths relative to that root.
- Path resolution rejects traversal (`..`), absolute paths, and symlink escape; the returned path must stay strictly within the skill root.
- File reads are bounded to a size cap (config default; out-of-bounds reads return an error, not a truncation).
- New built-in tool `read_skill_file(skill, path)` registered on main-agent, wired to the store's new method.
- Skill manifest's existing behavior is unchanged: `list_skills`, `read_skill`, one-level SKILL.md scan, frontmatter parsing, injection of `<available_skills>` all continue to work byte-identically.
- The skills-store startup scan's behavior is unchanged in what it finds (still `SKILL.md` in each immediate subdirectory of `SKILLS_DIR`); the change is that each discovered skill now remembers its root directory for later file access.

## Capabilities

### New Capabilities

(none — this change is additive to existing capabilities)

### Modified Capabilities

- `skills-store`: gains a requirement for recording the skill's root directory and exposing a bounded, sandboxed file-access API. The startup-scan behavior and catalog API shape are unchanged.
- `tool-invocation`: gains a new built-in tool `read_skill_file` alongside the existing `list_skills` and `read_skill`. Registration order keeps built-ins before MCP-sourced tools, consistent with the current invariant.

## Impact

- Code:
  - `internal/skills/store.go`, `internal/skills/manifest.go` — add root-directory tracking and a `ReadFile(name, relPath)` method.
  - `internal/tools/builtin_skills.go` — add `NewReadSkillFile` constructor and tool implementation; register in `cmd/main-agent/main.go`.
  - `cmd/main-agent/main.go` — register the new tool at startup.
- Specs: deltas on `skills-store` and `tool-invocation`.
- No new services, containers, or deployment changes. No new dependencies.
- No breaking changes. Existing tool schemas (`list_skills`, `read_skill`) and existing skill manifest injection are unchanged.
- Opens the door for change 2 (`skills-tool-gating`) and change 4 (`skills-script-execution`) to build on skill-relative file access. Does not depend on the `exec` service (change 3).
