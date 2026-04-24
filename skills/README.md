# Skills

This directory is scanned by main-agent at startup to populate the tool registry's skills catalog.

## Layout

Each skill is a subdirectory containing a `SKILL.md` file. Nested directories are ignored (only one-level deep scan).

```
skills/
  my-skill/
    SKILL.md
  other-skill/
    SKILL.md
    references/    # optional; unused by v1 but won't break anything
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
- `allowed-tools` (array of strings) — reserved for future enforcement; parsed but not acted on today.
- `model` (string) — reserved for future routing; parsed but not acted on today.

## Hot reload

Not supported. Restart main-agent to pick up changes.

## Invalid skills

Skills that fail to parse (missing delimiters, missing required fields, invalid YAML) are logged at WARN by main-agent and skipped — they do not block startup.
