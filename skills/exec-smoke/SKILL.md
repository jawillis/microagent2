---
name: exec-smoke
description: Exec-service smoke test. Runs a small Python script that prints a greeting and writes a note file to the workspace; useful for verifying the code-execution pipeline end-to-end or as a first-run example when asked to demonstrate run_skill_script.
---

# exec-smoke

A minimal exercise of the `exec` code-execution service. The skill contains one Python script (`scripts/hello.py`) that depends on `tabulate`, prints a greeting plus a version table to stdout, and writes a `note.txt` artifact to `WORKSPACE_DIR`. Suitable for:

- Model-driven demonstration of `run_skill_script`.
- Operator smoke-testing via direct HTTP against `exec:8085`.
- First-run verification that install + run + output detection all work on a given deployment.

## Contents

- `scripts/requirements.txt` — one small Python dep (`tabulate`) to exercise the install path.
- `scripts/hello.py` — prints to stdout and writes a text artifact to `WORKSPACE_DIR`.
- `scripts/long.sh` — sleeps longer than the default timeout; verifies timeout handling.

## Recipes

Install then run (from a container that can reach `exec:8085`):

```
curl -X POST exec:8085/v1/install -H 'Content-Type: application/json' -d '{"skill":"exec-smoke"}'
curl -X POST exec:8085/v1/run     -H 'Content-Type: application/json' -d '{"skill":"exec-smoke","script":"scripts/hello.py","session_id":"smoke"}'
curl -X POST exec:8085/v1/run     -H 'Content-Type: application/json' -d '{"skill":"exec-smoke","script":"scripts/long.sh","session_id":"smoke","timeout_s":1}'
curl exec:8085/v1/health
```

The first run should exit 0, print `hello from exec-smoke`, and include a `note.txt` entry in `outputs`. The second should have `timed_out: true` and `exit_code: -1`.
