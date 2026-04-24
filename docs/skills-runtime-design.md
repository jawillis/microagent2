# Skills Runtime Design

Design for expanding microagent2's skills layer to full runtime parity with
Anthropic's agent-skills format, backed by a general-purpose `exec` service
for code execution. This document captures the cross-cutting decisions that
will be referenced by the per-change OpenSpec proposals; it is the contract,
the per-change `design.md` files are the specifics.

Date: 2026-04-23
Status: Design locked, proposals pending.

## 1. Context and goal

microagent2 already has a skills layer (`internal/skills/`, `internal/tools/`,
`skills/` on disk). Today it is a thin instruction-loader: one scan at startup,
one skill checked in (`code-review`), frontmatter fields parsed but two of them
(`allowed-tools`, `model`) silently ignored. The only way to reach capability
beyond `list_skills` and `read_skill` is MCP — which is fine for external
capability, less fine for "the skill declares *which* tools it should use."

The goal is **runtime parity with the Anthropic agent-skills format**. A skill
authored for the Anthropic marketplace (e.g. `mcp-builder`, `webapp-testing`,
`claude-api`) should be able to drop into `skills/<name>/` and work: subdirectory
layout preserved, progressive disclosure of reference files supported, bundled
scripts runnable, `requirements.txt` honored. This unlocks the existing
Anthropic skill marketplace as a capability library for microagent2's agents.

The non-goal is divergence: we do not invent a new skill format. Where we extend
(policy, gating), we namespace so a skill remains portable.

## 2. Target: Anthropic agent-skills runtime parity

The canonical spec lives at `agentskills.io/specification`. The skill examples
bundled locally (`~/.claude/plugins/marketplaces/anthropic-agent-skills/skills/`)
show the realized format: a directory with `SKILL.md` at root, `scripts/` and
`reference/` subdirectories, per-skill `requirements.txt`, sometimes a license
file. The spec should be pinned to a specific revision in each per-change
proposal so "compatible" is verifiable rather than aspirational.

Gap between microagent2 today and the target:

| Feature                                    | Today | Target | Change |
|---|---|---|---|
| `SKILL.md` frontmatter (`---` YAML)        | ✓ | ✓ | — |
| Required `name` + `description`            | ✓ | ✓ | — |
| `allowed-tools` field parsed               | ✓ | ✓ | — |
| `allowed-tools` **enforced**               | — | ✓ | 2 |
| One-level scan for skill directories       | ✓ | ✓ | — |
| Subdirectory preservation                  | — | ✓ | 1 |
| Skill-relative file access (progressive)   | — | ✓ | 1 |
| Script execution                           | — | ✓ | 3+4 |
| Per-skill dependency installation          | — | ✓ | 3+4 |

Four sequenced OpenSpec changes cover the gap (§8).

## 3. Architecture overview

```
┌────────────────────────────────────────────────────────────────────────┐
│                       MICROAGENT2 DEPLOYMENT                           │
│                                                                        │
│  ┌──────────────────┐   HTTP    ┌─────────────────────────────────┐   │
│  │   main-agent     │──────────▶│          exec                    │   │
│  │   (Go)           │           │   Go service in container        │   │
│  │                  │           │   with Python, uv, bash, git,    │   │
│  │  Tool registry   │           │   Playwright, curl, jq, ...      │   │
│  │  with gating:    │           │                                  │   │
│  │  - base (always) │           │   HTTP API:                      │   │
│  │  - active skill  │           │     POST /v1/run                 │   │
│  │    narrows scope │           │     POST /v1/install             │   │
│  │                  │           │     GET  /v1/health              │   │
│  └──────────────────┘           │                                  │   │
│          │                      │   /skills  ro  (mounted)         │   │
│          │                      │   /workspace rw (per-invocation) │   │
│          │                      │   /cache    rw (venvs, browsers) │   │
│          ▼                      └─────────────────────────────────┘   │
│   ┌────────────────┐                           ▲                       │
│   │ skills/        │───────────────────────────┘                       │
│   │  code-review/  │   mounted read-only                               │
│   │  mcp-builder/  │                                                   │
│   │    scripts/    │                                                   │
│   │    reference/  │                                                   │
│   └────────────────┘                                                   │
└────────────────────────────────────────────────────────────────────────┘
```

`exec` is deliberately **not** named `skill-runner`. It is a general-purpose
sandboxed code-execution service. Skills are the first consumer. Later
consumers (retro-agent analysis scripts, curiosity/initiative research,
self-improvement evals) can reuse the same primitive without a rename or
architectural redesign.

Two surfaces change in main-agent:
- The tool registry gains gating per active skill (§4.3).
- New built-in tools wrap `exec` HTTP calls (§6.2).

## 4. Skill semantics

### 4.1 Activation: implicit with stack

Loading a skill activates it. There is no separate `activate_skill` tool —
calling `read_skill("foo")` both returns `foo`'s body and makes `foo` the
active skill. A subsequent `read_skill("bar")` replaces `foo` with `bar`.
An empty or sentinel skill name clears the stack back to base.

```
read_skill("foo")      → foo active,  base + foo.allowed-tools visible
read_skill("bar")      → bar active,  base + bar.allowed-tools visible
read_skill("")         → no active,   base only
```

Rationale. Implicit-with-stack is the simplest model that matches Anthropic's
runtime behavior (loading ≈ committing). A separate activate/deactivate pair
(a) adds a tool for an operation the model already implies, (b) invites
confusion over whether `read_skill` was inspect or commit. The stack shape
prevents the "the model browsed and accidentally narrowed itself" footgun
that pure implicit has: reading again always replaces, never stacks deeper.

### 4.2 Scope: per session

"Active skill" is session-scoped state. The session is identified by
`session_id` in the turn payload. Active-skill state lives in the session
envelope (exact location is a per-change decision; likely an in-process map
keyed by session id, or a small record in the context-manager's assembled
context). It is **not** stored in Hindsight or any long-term substrate — a
skill is an intra-session role, not a durable fact about the user.

A new session starts with no active skill. Ending a session clears the state
implicitly.

### 4.3 Base toolset

When no skill is active, the registry exposes the **medium base**:

```
list_skills        read_skill        read_skill_file     run_skill_script
current_time       + all configured MCP tools
```

Rationale. Minimal base (only `list_skills` + `read_skill`) forces the model
to load a skill before doing anything useful, which is strong least-privilege
but obstructive in practice. Maximal base (no gating unless a skill is active)
weakens the whole point of `allowed-tools`. Medium strikes the balance: skill
plumbing is always available, MCP-configured capability is always available
(the operator decided those belong on the box), and activating a skill
*narrows* — it never blocks essentials.

When a skill is active, `Registry.Schemas()` returns:

```
base_tools ∪ active_skill.allowed-tools
```

A skill whose `allowed-tools` is empty effectively narrows to just the base
(useful for instruction-only skills). A skill that lists tools outside the
registry's known set logs a warning at activation and those entries are
ignored.

### 4.4 Progressive disclosure

Anthropic skills layer their content:
- **Frontmatter** (name, description) is always visible to the model via
  `<available_skills>` injection.
- **`SKILL.md` body** is loaded on demand via `read_skill`.
- **Additional files** (scripts, reference docs) are loaded when the body
  directs the model to them (e.g. "See `reference/mcp_best_practices.md`").

microagent2 supports each layer:
- Layer 1: existing `<available_skills>` injection in main-agent
- Layer 2: existing `read_skill(name)` returns the body
- Layer 3: new `read_skill_file(skill, relative_path)` tool (change 1)

`read_skill_file` normalizes the relative path, rejects traversal attempts
(`..`, absolute paths, symlink escape), and returns file contents bounded to
a response-size cap. Path must resolve within the skill's directory.

## 5. Security model

### 5.1 Trust boundary

Skills are **checked-in code**, reviewed at install via pull request. The
install-time review is the primary trust boundary. At runtime, the operator
is assumed to trust what they have installed; fine-grained runtime network
allowlisting and dynamic policy are out of scope for v1.

This trust model has two implications that must be honored:

1. The system does **not** support hot-installing skills from arbitrary remote
   sources at runtime. Adding a skill is a deliberate repository change.
2. Operators who do not trust an installed skill should remove it, not
   attempt to sandbox it through runtime config. Runtime config exists for
   operational tuning, not as a security primitive against untrusted code.

### 5.2 Sandbox invariants

The `exec` container enforces these invariants unconditionally:

| Invariant | Mechanism |
|---|---|
| Non-root subprocess user | Dockerfile `USER` directive; subprocess inherits |
| Dropped capabilities | `--cap-drop=ALL` on container |
| Read-only skills mount | `/skills` mounted `ro` |
| Writable workspace, bounded | `/workspace` tmpfs with size cap |
| No inherited secrets | `exec` process has its own env; does not see main-agent's |
| Resource caps | cgroup CPU/memory limits; per-subprocess ulimit |
| Timeout enforcement | HTTP deadline + SIGKILL after grace |
| Container cannot reach internal services | No valkey/broker/proxy creds in env; no DNS entries for internal hosts |

The "no inherited secrets" invariant is the single most important defense
against prompt-injection-triggered exfiltration. main-agent holds
`ANTHROPIC_AUTH_TOKEN`; a subprocess in exec must not.

### 5.3 Network policy

Network policy is **operator-declared, centrally**. A skill's SKILL.md does
not declare network needs; microagent2 does not extend Anthropic's frontmatter
with a network field. The decision model:

```
DEFAULT:   allow (full network at runtime)
OVERRIDE:  operator policy per skill can deny

exec.yml (or equivalent):
  network:
    default: allow
    overrides:
      suspicious-skill: deny
```

Rationale. Curated-skills-dir trust model means the operator has reviewed
every skill in the repo before it runs. Default-deny would force the operator
to configure each skill twice — once in the PR that adds it, once in a
central policy file. Default-allow with a central override list matches the
"trust what's installed" stance and keeps the policy surface minimal.

The two modes are binary: `allow` (full network) or `deny` (empty netns, no
network at all). Granular per-domain allowlists via HTTP proxy or DNS
filtering are explicitly out of scope for v1; they can be layered in later
without a design change since they are a property of the `exec` container,
not the skill contract.

The **install phase** has its own network policy, separate from runtime:
`exec` has outbound network to a hardcoded list of package registries
(PyPI, npm, Playwright CDN) during dependency installation. This is a
property of the runner, not configurable per skill.

## 6. Execution contract

### 6.1 HTTP API (exec service)

```
POST /v1/run
Request:
  {
    "skill":    "mcp-builder",
    "script":   "scripts/evaluation.py",
    "args":     ["--target", "github"],
    "stdin":    "optional input",
    "timeout_s": 120,
    "session_id": "sess-abc"     // for workspace scoping
  }
Response:
  {
    "exit_code":         0,
    "stdout":            "first N KB",
    "stdout_truncated":  false,
    "stderr":            "...",
    "stderr_truncated":  false,
    "workspace_dir":     "/workspace/sess-abc/inv-xyz/",
    "outputs":           ["/workspace/sess-abc/inv-xyz/screenshot.png"],
    "duration_ms":       1240,
    "timed_out":         false
  }

POST /v1/install
Request:
  { "skill": "mcp-builder" }
Response:
  { "status": "ok" | "error", "duration_ms": 12345, "error": "..." }

GET /v1/health
Response:
  { "status": "ok", "prewarmed_skills": [...], "ready": true }
```

Transport is HTTP for operator-debuggability, fit with Go stdlib, and
consistency with microagent2's other service boundaries. gRPC would offer
typed contracts at the cost of iteration speed; Valkey messaging would
align with the existing async inter-service pattern but introduces
request/response complexity that HTTP handles natively.

### 6.2 Built-in tools (main-agent → exec)

Changes 1 and 4 add three built-in tools to main-agent's registry:

| Tool | Added by | Semantics |
|---|---|---|
| `read_skill_file(skill, path)` | change 1 | Reads a file within a skill's directory. Path must resolve inside the skill root. Response bounded to size cap. |
| `run_skill_script(skill, script, args?, stdin?, timeout_s?)` | change 4 | HTTP POST to `exec /v1/run`. Returns the structured output envelope verbatim. |
| `current_time(format?)` | change 2 (as part of base toolset) | Returns current UTC time. No exec dependency. |

`list_skills` and `read_skill` remain unchanged.

### 6.3 Output envelope

The `/v1/run` response is a **hybrid** shape: capped inline streams plus a
persistent workspace path.

```
OUTPUT SHAPE
│
├── stdout / stderr inline
│     Capped at N KB per stream (default 16 KB stdout, 8 KB stderr).
│     `truncated: true` when cap hit; full content in workspace file.
│
├── workspace_dir
│     Per-invocation scratch dir.
│     Persisted 1h after invocation, then GC'd.
│     Model can read files via read_skill_file (extended to cover workspace)
│     or follow-up run_skill_script calls that reference the path.
│
├── outputs array
│     exec scans workspace for non-text files (MIME-detected) and
│     returns their paths. Binary content is NEVER inlined.
│     v1 stops here; broker/proxy-side vision plumbing is a later change.
│
└── metadata
      exit_code, duration_ms, timed_out — always present.
```

### 6.4 Workspace lifecycle

Per-invocation scratch directory with 1h retention:

```
/workspace/
  sess-<session_id>/
    inv-<invocation_id>/
      (script-generated files)
      .metadata.json   (exec-owned; run start, args, script hash)
```

Skills that genuinely need caching (Playwright's ~200MB browser binaries,
mcp-builder's dep cache) use `/cache/<skill>/`, a separate mount, shared
across invocations, installed at prewarm or first-use. `/workspace/` is
tmpfs-bounded; `/cache/` is persistent disk.

1h retention lets the model make follow-up tool calls that reference output
paths without re-running the script. After 1h, a background GC sweep
reclaims. The exec service exposes no API for extending retention in v1;
long-lived state belongs in Hindsight or the memory service, not exec
workspaces.

### 6.5 Resource caps (defaults)

```
stdout_inline_cap:      16 KB
stderr_inline_cap:       8 KB
workspace_size_cap:     64 MB  (tmpfs)
script_timeout:        120 s
workspace_retention:     1 h
response_size_cap:      32 KB  (total tool result to LLM)
```

These are operator-configurable exec settings, not skill frontmatter.
Skills that legitimately need higher caps argue for them in PR review;
operators raise defaults if that becomes common.

## 7. Dependency management

### 7.1 Install phases

A skill's dependencies (`scripts/requirements.txt`) install in two possible
modes, both handled by exec:

```
PREWARM (at exec startup)
────────────────────────
Skills with `x-microagent.prewarm: true` in SKILL.md frontmatter are
installed during exec container startup. Startup waits for prewarm to
complete before reporting healthy. Suitable for skills used frequently
or with heavy deps (Playwright browsers).

LAZY (first invocation)
───────────────────────
All other skills install on first `run_skill_script` call for that
skill. The invocation that triggers install pays the latency cost;
subsequent calls hit the cached venv.
```

This is the "hybrid" dependency decision. The prewarm field is the one
place microagent2 extends the Anthropic frontmatter with an `x-microagent:`
namespaced block. Namespacing keeps skills portable to other runtimes
(they ignore unknown top-level keys).

### 7.2 Dependency resolver

Python: `uv` (astral). Fast, deterministic, handles venvs cleanly. Install
command inside exec: `uv pip install -r scripts/requirements.txt
--python .cache/<skill>/venv/bin/python`. Venv cached at
`/cache/<skill>/venv/`.

Non-Python deps (bash scripts, Node via `package.json`) are out of scope
for v1. Documented limitation. If a skill ships `package.json`, exec
logs a warning and proceeds as if only Python deps existed.

### 7.3 Install failures

Install failure is not fatal to exec, only to the skill. The skill is
marked "install failed" in `/v1/health`, and `run_skill_script` returns
a structured error when called for it. Operator sees the failure in
health output and in logs. Skill remains in the registry (the body and
reference files are still useful for prose-consumption) but scripts
cannot run until the install is fixed.

## 8. Change sequence

```
┌────────────────────────────────────────────────────────────────────┐
│ 1. skills-format-subdirs                                            │
│    Preserve subdirectories at scan. Add read_skill_file tool.       │
│    Path normalization + sandbox to skill root.                      │
│    Value: Anthropic doc/reference skills importable immediately.    │
│                                                                     │
│ 2. skills-tool-gating                                              │
│    Honor allowed-tools. Define base toolset.                        │
│    Implement active-skill-with-stack session state.                 │
│    current_time built-in.                                           │
│    Value: least-privilege lever + basic utility before toolkit      │
│           expansion.                                                │
│                                                                     │
│ 3. exec-service                                                     │
│    New Go service in cmd/exec/.                                     │
│    Docker image with Python/uv/bash/Playwright.                     │
│    HTTP API (/v1/run, /v1/install, /v1/health).                     │
│    Per-skill venv cache, install phases, workspace lifecycle,       │
│    resource caps, sandbox invariants.                               │
│    Value: general-purpose code execution primitive.                 │
│                                                                     │
│ 4. skills-script-execution                                          │
│    run_skill_script tool in main-agent.                             │
│    HTTP client for exec.                                            │
│    Output envelope handling, truncation signals.                    │
│    Value: Anthropic scripts actually run.                           │
└────────────────────────────────────────────────────────────────────┘

Dependency graph:
  1 ─────────────────┐
                     ▼
  2 ──────► (uses 1 for subdir semantics) ──┐
                                            ▼
  3 ──────► (independent — can land in parallel with 1 or 2) ─┐
                                                              ▼
                                                              4
```

Each change has its own OpenSpec proposal with per-change design.md. The
per-change design.md cites this document rather than re-deriving the
cross-cutting contract.

## 9. Deferred

Decisions explicitly deferred beyond v1:

- **Vision plumbing for image outputs.** exec v1 returns paths. Wiring
  images into broker/proxy responses for vision-capable LLMs is a separate
  change, likely alongside a broader "binary artifact handling" story.
- **Granular network allowlists.** v1 is binary allow/deny. Per-domain,
  per-port, or HTTP-proxy-based filtering is v2.
- **Non-Python dependency managers.** `package.json`, `Gemfile`, etc. are
  out of scope. Only Python `requirements.txt` via uv.
- **Remote skill installation.** No `microagent2 skills install <url>`.
  Skills are added to the repository manually via PR.
- **Skill discovery beyond filesystem.** Registry-backed or network-fetched
  skill sources are not contemplated.
- **Versioning and upgrade paths.** A skill's contract is the version
  checked into the repo. No skill version field, no upgrade handshake.
- **Per-skill resource cap overrides.** v1 uses exec-global defaults.
  Skills that legitimately need larger caps wait for a v2 mechanism.
- **Active-skill persistence across session.** Active skill is in-memory.
  Replaying a session from persistence does not restore the previously
  active skill; the replay starts clean.

## 10. Open questions these proposals will resolve

Questions that are *shape* rather than *contract*, to be answered in the
per-change design.md:

- Where does active-skill state live concretely — in-process map in
  main-agent, the context-manager's assembled context, or a session record
  in valkey? (change 2)
- What is the HTTP client retry/timeout policy for the main-agent → exec
  call? (change 4)
- How is `x-microagent.prewarm` validated and what happens on malformed
  values? (change 3)
- Does `exec` have a single shared uv resolver cache, or per-skill? What
  happens on partial upgrade of a shared cache? (change 3)
- Should `run_skill_script` carry a correlation id matching the main-agent
  tool-invocation log, or generate its own invocation id? (change 4)

These belong in the changes, not here.
