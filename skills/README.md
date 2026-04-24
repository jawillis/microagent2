# Skills

This directory is scanned by main-agent at startup to populate the tool registry's skills catalog.

## Layout

Each skill is a subdirectory containing a `SKILL.md` file. Additional files (reference docs, scripts, examples) can live beside `SKILL.md` and are reachable via the `read_skill_file` tool. Only the top-level subdirectories are scanned for `SKILL.md`; nested SKILL.md files (e.g. `skills/foo/bar/SKILL.md`) are not treated as separate skills.

```
skills/
  my-skill/
    SKILL.md
  other-skill/
    SKILL.md
    reference/
      best_practices.md
    examples/
      usage.md
```

## SKILL.md format

```markdown
---
name: my-skill
description: One-line description used by the model to decide when to load this skill.
---

The body is Markdown. When the model calls read_skill with this skill's
name, this body is returned verbatim and the model applies the instructions.

Keep descriptions short and action-oriented — the description drives
discoverability, not the body.
```

### Required frontmatter fields
- `name` (string) — the identifier the model passes to `read_skill`.
- `description` (string) — shown to the model alongside the skill's name via the system-prompt manifest and by `list_skills`.

### Optional frontmatter fields
- `allowed-tools` (array of strings) — tools this skill is allowed to call when active. See "Tool gating" below. Omitted or empty means the skill exposes only the base toolset.
- `model` (string) — reserved for future routing; parsed but not acted on today.
- `x-microagent` (object) — microagent2-specific extensions, namespaced so other Anthropic-compatible runtimes ignore them. See "Prewarm" below.

## Progressive disclosure

When a skill needs to reference supplementary material (reference docs, examples, etc.), the body should name the relative path and the model calls `read_skill_file` to load it:

```markdown
---
name: code-review
description: Review, audit, critique or improve code
---

Apply the review pipeline below. For language-specific pitfalls, consult
`language-notes.md` via read_skill_file.
```

The model-facing contract for `read_skill_file`:

- Takes two string arguments: `skill` (exact name as returned by `list_skills`) and `path` (relative to the skill's directory — e.g. `reference/best_practices.md`).
- Returns the file contents verbatim on success.
- Rejects absolute paths, traversal (`..`), symlinks that resolve outside the skill root, non-regular files, and files larger than `SKILL_FILE_MAX_BYTES` (default 256 KB).
- `SKILL.md` itself is reserved — `read_skill_file` redirects to `read_skill` for the skill body.

## Tool gating

When the model calls `read_skill("foo")` and the skill exists, `foo` becomes the session's *active skill*. Until another skill is loaded, the tools visible to the model are the union of:

- the **base toolset** (`list_skills`, `read_skill`, `read_skill_file`, `current_time`, plus every configured MCP tool), and
- the active skill's `allowed-tools` list.

An active skill with an empty `allowed-tools` narrows to the base only — handy for instruction-only skills. Names in `allowed-tools` that do not match a registered tool are logged at WARN (`skill_allowed_tool_unknown`) and silently dropped from the visible set; the skill still activates. A call to a tool that is not in the visible set is not executed; main-agent returns `{"error":"tool not available under active skill: <name>"}` as the tool result and logs `tool_invoked outcome=gated`.

Active skill state is stored in Valkey at `session:<session_id>:active-skill` with a 24h TTL. It persists across turns within a session and is cleared automatically when a new skill is loaded or when the stored skill is no longer present on disk.

## Running skill scripts

Skills that ship executables under `scripts/` can be invoked by the model via
the `run_skill_script` built-in tool. The tool calls the `exec` service's
`/v1/run` endpoint, returning a JSON envelope with `exit_code`, `stdout`,
`stderr`, `workspace_dir`, `outputs`, and `duration_ms`. Example request:

```json
{
  "skill": "mcp-builder",
  "script": "scripts/evaluation.py",
  "args": ["--target", "github"],
  "timeout_s": 60
}
```

`session_id` is always populated by main-agent from the turn's session;
model-supplied values are overridden. First invocation for a skill may
include dependency-install latency unless prewarm is set (see below).

## Prewarm (exec service)

Skills that ship executable helpers under `scripts/` can opt into dependency
prewarming at `exec` service startup by adding a namespaced frontmatter flag:

```markdown
---
name: webapp-testing
description: ...
x-microagent:
  prewarm: true
---
```

At boot, the `exec` container scans `skills/*/SKILL.md`, installs
`scripts/requirements.txt` for each prewarm-opted skill concurrently (capped
by `EXEC_PREWARM_CONCURRENCY`), and flips its `/v1/health` from `starting` to
`ok` once all prewarm installs finish. Skills without the flag install
lazily on first use; the first `run_skill_script` call pays the install
latency.

Trade-off: prewarming speeds up first-run but slows container boot. Use it
for frequently-used heavy skills (Playwright-based testing, large package
sets); omit it for rarely-used skills.

## Hot reload

Not supported. Restart main-agent to pick up changes. The `exec` service
likewise scans skills only at boot.

## Invalid skills

Skills that fail to parse (missing delimiters, missing required fields, invalid YAML) are logged at WARN by main-agent and skipped — they do not block startup.

## Configuration

- `SKILLS_DIR` (default `./skills/`) — root directory scanned at startup.
- `SKILL_FILE_MAX_BYTES` (default `262144`) — per-file size cap enforced by `read_skill_file`. Oversize reads return a structured error; they are not truncated.
