---
name: exec-smoke
description: Operator smoke-test skill for the exec service. NOT for agent use. Exercises install + run end-to-end; safe to delete once the exec service is known-good in a given deployment.
---

# exec-smoke

Operator-facing smoke test for the `exec` code-execution service. It is intentionally named and described in a way that discourages the model from loading it during ordinary operation. Use via direct HTTP calls against the `exec` service, not via the agent tool loop.

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
