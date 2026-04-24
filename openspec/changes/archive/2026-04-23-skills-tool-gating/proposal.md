## Why

microagent2's `SKILL.md` frontmatter has had an `allowed-tools` field since slice-2, parsed into the `Manifest` but never consulted at runtime. The same is true of the broader "skill as a role" model described in `docs/skills-runtime-design.md` §4: today every turn exposes the full tool registry regardless of which skill the model has loaded. This change finishes the dangling frontmatter contract and lands the minimum active-skill + base-toolset + schema-filtering machinery needed so subsequent changes (skills-script-execution and future capability skills) can rely on least-privilege as a real primitive. Without it, expanding the built-in toolkit (change 4 and beyond) creates unbounded token cost per turn and unbounded blast radius per skill.

## What Changes

- `tools.Registry` gains schema filtering. The main-agent tool loop obtains per-turn schemas via a new `SchemasFor(activeSkill *skills.Manifest)` method that returns `base_tools ∪ activeSkill.allowed-tools` (or just `base_tools` when no skill is active).
- Main-agent tracks one active skill per session. State is stored in valkey at `session:<id>:active-skill` with a 24h TTL (matches the existing response-store session TTL pattern).
- Main-agent reads the active skill at the start of each turn, filters `Registry.Schemas` accordingly, and updates it when the tool loop invokes `read_skill` with a known skill name. Replacement is implicit — each successful `read_skill("foo")` replaces any previously active skill for that session (the "stack" described in `docs/skills-runtime-design.md` §4.1 is trivially a stack of depth 1).
- Main-agent gates tool invocation: if the model calls a tool that is not in the per-turn filtered set, the registry call is skipped and a structured `{"error":"tool not available under active skill: <name>"}` is returned instead. Prevents models from reaching for tools they should not see.
- `read_skill`'s user-visible behavior is unchanged (body is still returned on hit; error on miss). The activation is a side-effect of the tool invocation, observable to the operator only through a new structured log line.
- New built-in tool `current_time` added to the base toolset. Takes optional `format` argument (default: RFC3339 UTC). This is a cheap, useful, deterministic tool that belongs in the base per `docs/skills-runtime-design.md` §4.3.
- `<available_skills>` manifest injection is unchanged — the model continues to see every registered skill so it can choose one to activate. Filtering happens at the tool schema layer, not the skill discovery layer.
- Invalid entries in a skill's `allowed-tools` (names that match no registered tool) are logged at WARN at activation time and ignored in the filtered schema; activation still succeeds.
- A skill with an empty `allowed-tools` list activates to the base set only — useful for instruction-only skills that just want their body loaded and do not need extra tools.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `tool-invocation`: gains active-skill tracking, schema filtering, invocation-time gating, the `current_time` built-in, and the updated registration/base-set contract. The existing requirements for `list_skills`, `read_skill`, and `read_skill_file` are preserved; the registration-order requirement is updated to include `current_time`.
- `skills-store`: no behavioural change. The `allowed-tools` field was already parsed and exposed; this change is strictly the consumer side.

## Impact

- Code:
  - `internal/tools/tool.go` — add `SchemasFor(activeSkill *skills.Manifest) []ToolSchema`. `Schemas()` stays for the zero-active-skill case and for existing callers.
  - `internal/tools/current_time.go` — new built-in tool.
  - `internal/tools/builtin_skills.go` — no change to `read_skill`; main-agent does activation out-of-band.
  - `cmd/main-agent/main.go` — register `current_time` after `read_skill_file`. Add active-skill helpers.
  - `cmd/main-agent/loop.go` (new file or in `main.go`) — introduce per-turn active-skill read/update plus invoke-gate.
  - `internal/sessionskill/` (new tiny package) — valkey get/set/clear helpers keyed by session_id. Keeps main-agent free of redis plumbing details.
- Specs: deltas on `tool-invocation`.
- No new services, no container changes, no external dependencies. Valkey is already a core dependency.
- No breaking changes at the tool-schema wire level — `Schemas()` still exists and still returns the full registry for callers that do not track active skills (tests, smoke scripts).
- Dashboard/status observability: a new structured log line `active_skill_changed` is emitted when the active skill flips. No new panel wiring.
- Unblocks change 4 (`skills-script-execution`): when `run_skill_script` lands, it will be added as a base tool and will inherit gating for free.
