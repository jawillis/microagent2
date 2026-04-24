# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Status

This is a greenfield project. No application code exists yet — only workflow scaffolding via OpenSpec.

## Workflow: OpenSpec

This project uses [OpenSpec](https://openspec.dev) for structured change management. The workflow is driven via slash commands:

- `/opsx:explore` — Think through ideas, investigate problems, clarify requirements (no code changes)
- `/opsx:propose <name>` — Create a change with proposal, design, and task artifacts
- `/opsx:apply <name>` — Implement tasks from a change
- `/opsx:archive <name>` — Archive a completed change

### OpenSpec CLI Commands

```bash
openspec new change "<name>"                          # Create a new change
openspec list --json                                  # List active changes
openspec status --change "<name>" --json              # Check artifact status
openspec instructions <artifact-id> --change "<name>" --json  # Get artifact instructions
openspec instructions apply --change "<name>" --json  # Get implementation instructions
```

### Directory Structure

```
openspec/
  config.yaml          # Schema config (currently: spec-driven)
  changes/             # Active changes (each gets proposal.md, design.md, tasks.md)
    archive/           # Archived completed changes (YYYY-MM-DD-<name>/)
  specs/               # Main specs (synced from delta specs during archive)
```

### Schema: spec-driven

The default schema produces three artifacts per change:
1. **proposal.md** — What and why
2. **design.md** — How (depends on proposal)
3. **tasks.md** — Implementation steps (depends on design)

Artifacts must be created in dependency order. Implementation begins after `tasks.md` is complete.
