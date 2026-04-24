## Context

This is change 2 of the four-change sequence toward Anthropic agent-skills runtime parity; cross-cutting contract lives in `docs/skills-runtime-design.md`. This document covers the mechanics of: (a) tracking a single active skill per session, (b) filtering tool schemas per turn, (c) gating invocation to the filtered set, and (d) adding `current_time` to the base set.

Current state:
- `internal/tools/tool.go:Registry.Schemas()` returns the full registered schema slice. Every main-agent turn sees every tool.
- `internal/skills/manifest.go:Manifest.AllowedTools` is parsed from SKILL.md frontmatter but no runtime consumer reads it.
- Main-agent is stateless between turns apart from its slot bookkeeping and Valkey-backed artifacts in other services. No per-session map exists in-process.
- The architecture permits multiple main-agent instances (consumer-group distribution on `stream:agent:main-agent:requests`), so any new per-session state must live in shared storage, not in-process.
- `docker-compose.yml` currently declares a single main-agent service, but the consumer-group contract is what matters — correctness under the contract is the target.

Constraints:
- Must not break existing `tool-invocation` requirements. `list_skills`, `read_skill`, `read_skill_file`, registration order, slot lifecycle, tool-call/tool-result pub/sub — all byte-identical.
- Cannot introduce new services or external dependencies. Valkey and slog are already in the tree; use them.
- Must preserve the existing `Schemas()` signature for tests and the smoke tool that exercise the registry without a session context.
- Active-skill state must survive a turn ending and next turn beginning (session-scoped per `docs/skills-runtime-design.md` §4.2).

## Goals / Non-Goals

**Goals:**
- Land a per-session active-skill record in Valkey, read and written by main-agent only.
- Add `SchemasFor(activeSkill)` that returns `base_tools ∪ activeSkill.allowed-tools`, or `base_tools` when `activeSkill == nil`.
- Add invoke-time gating so tools outside the filtered set return a structured error without executing.
- Add `current_time` built-in to the base set.
- Emit a structured `active_skill_changed` log line when activation flips for a session.
- Preserve identical behaviour of `read_skill` (body content unchanged on hit; error envelope unchanged on miss). Activation is a side-effect of a successful hit, observable via logs only.

**Non-Goals:**
- Explicit "clear active skill" operation. The shared doc's sketch suggested `read_skill("")` could clear; we defer this — `read_skill` continues to reject empty names per the existing spec. If experience demonstrates a need to deactivate without loading a different skill, add `clear_active_skill` in a follow-up.
- Stack depth greater than 1. "Implicit with stack" degenerates to "one slot, replaced on each activation" because no one requested deeper history tracking. Honest, minimal.
- Changing `<available_skills>` manifest injection. The model still sees every skill to discover them; filtering is one layer deeper at tool schemas.
- Script execution, exec service, or anything that requires the `exec` container. Those are changes 3 and 4.
- Per-skill model override (`model:` frontmatter field). Remains parsed-but-unused; `docs/skills-runtime-design.md` §9 keeps this deferred.
- Per-tool rate limits, audit trails, or richer policy. Gating is binary: visible/invocable or not.
- UI surfacing of the active skill in the dashboard. Out of scope; logs are the observability surface for v1.

## Decisions

### Decision 1: Storage in Valkey, not in-process

Active-skill state lives at `session:<id>:active-skill` — a redis string whose value is the skill name (empty value or missing key both mean "no active skill"). 24h TTL matches `internal/response/store.go:defaultSessionHashTTL`.

Alternative considered: in-process map in main-agent. Rejected because `cmd/main-agent/main.go` consumes requests via a consumer group (`cg:agent:main-agent` on `stream:agent:main-agent:requests`); with N>1 main-agent replicas, consecutive turns for a session can land on different instances. An in-process map would silently lose state across instances. Valkey makes correctness independent of instance count at the cost of one get+set round-trip per turn, which is in the noise relative to the LLM round-trip.

Alternative considered: piggyback on `ContextAssembledPayload` from context-manager. Rejected because context-manager has no authoritative view of what the model just decided to activate during the current turn; the activation signal lives in main-agent. Making context-manager derive active-skill from message history is possible but fragile (it would have to parse conversation content for past `read_skill` calls).

### Decision 2: New `internal/sessionskill/` package

A small package with three functions:

```go
package sessionskill

// Key returns the Valkey key for a session's active-skill state.
func Key(sessionID string) string

// Get reads the active skill name; empty string means no active skill.
func Get(ctx context.Context, rdb *redis.Client, sessionID string) (string, error)

// Set writes the active skill name with the configured TTL. Empty name clears.
func Set(ctx context.Context, rdb *redis.Client, sessionID, name string, ttl time.Duration) error
```

Alternative considered: put helpers in `cmd/main-agent` directly. Rejected because `read_skill` and future tools may eventually need to read session state too (e.g. `clear_active_skill` if added), and a package is the right shape for cross-caller use. Keeping it tiny and scoped to skill state now avoids the "generic session-state package" that would accumulate unrelated concerns.

The package exposes no storage-specific types; it takes a `*redis.Client` directly, consistent with `internal/mcp/manager.go:NewManager` and `internal/response/store.go:NewStore`.

### Decision 3: Base toolset list is explicit, not derived

Main-agent and the registry both need to agree on "which tools are always in the base set." Rather than scatter this across the codebase, `SchemasFor` takes an explicit list of "always-on" tool names (the base set) in addition to the active skill. Main-agent computes the base list at startup and passes it in on each call.

```go
func (r *Registry) SchemasFor(base []string, allowed []string) []ToolSchema
```

- `base` is the always-on list, provided by the caller. Schemas for names in this list are always included.
- `allowed` is the skill's allowed-tools list. Empty means "only base."
- The union preserves registration order (so the wire contract stays stable).

Alternative considered: store the base set on the registry itself (`Registry.SetBase([]string)`). Rejected because the registry is shared across callers in tests and smoke tools that do not want to think about a base set — they can keep calling `Schemas()` unchanged. Passing the base per call keeps the registry stateless and the call sites self-documenting.

### Decision 4: Base set membership for v1

```
list_skills
read_skill
read_skill_file
current_time
<all MCP tools by mcp__<server>__<tool> name>
```

`run_skill_script` lands in change 4 and joins the base set then. MCP tools are included because the operator enabled them; they are part of the always-available surface per `docs/skills-runtime-design.md` §4.3.

Implementation-wise, main-agent computes the base as "every currently-registered built-in tool name plus every MCP tool name." Since built-ins are registered before MCP tools at startup and the registry order is stable, this is a one-time computation after `mcpMgr.Start()` returns.

Alternative considered: read the base set from config. Rejected for v1 because it adds a configuration surface without a concrete use case. The hardcoded list is clear and easy to change in a follow-up when operators need granular control.

### Decision 5: `SchemasFor` returns union preserving registration order

```go
// Pseudocode
func (r *Registry) SchemasFor(base, allowed []string) []ToolSchema {
    visible := make(map[string]bool, len(base)+len(allowed))
    for _, n := range base { visible[n] = true }
    for _, n := range allowed { visible[n] = true }
    out := make([]ToolSchema, 0, len(visible))
    for _, name := range r.order {
        if visible[name] && r.tools[name] != nil {
            out = append(out, r.tools[name].Schema())
        }
    }
    return out
}
```

Order is `r.order` (registration order), not `base`-then-`allowed`. This keeps the wire contract stable regardless of which skill is active (LLM request bodies for the same effective set remain byte-identical). Unknown names in `allowed` are silently ignored at the per-turn level — the warn log for unknown names is emitted separately at activation time (Decision 7), not per turn.

### Decision 6: Invoke-time gating is enforced by main-agent, not the registry

The registry's `Invoke` remains stateless. Main-agent computes the per-turn visible set once (same set it used to build schemas) and checks each tool_call name against it before calling `Registry.Invoke`. If the name is not in the set, main-agent constructs the error envelope directly and appends it as the tool-result — no registry call, no side effects.

Alternative considered: push the gate into `Registry.Invoke` by passing the visible set. Rejected for the same reason as Decision 3: tests and the smoke tool use `Invoke` directly without thinking about sessions. A main-agent-owned gate keeps session-awareness concentrated in the place that already owns the session id.

Structured log when the gate trips:

```
INFO  tool_invoked  correlation_id=... tool_name=... outcome=gated  active_skill=...
```

`outcome=gated` is a new value alongside the existing `ok`, `error`, `panic`.

### Decision 7: Activation is a side-effect of a successful `read_skill`

Main-agent inspects each tool_call's name and the corresponding tool_result:

```
if call.Function.Name == "read_skill" and result.Outcome == "ok":
    parse args.name from call.Function.Arguments
    if name is non-empty and stores.Get(name).ok == true:
        sessionskill.Set(ctx, rdb, session_id, name, 24h)
        logger.Info("active_skill_changed", session_id=..., active_skill=name)
        update in-turn visibleSet for subsequent iterations
```

Activation only fires when `read_skill` succeeded (body returned). Misses (`skill not found`) do not change active-skill state. This is the minimum-surprise rule and keeps the tool envelope unchanged.

The in-turn update matters: if iteration 1 activates skill X, iteration 2's `SchemasFor` call must already reflect X. So main-agent holds the current active-skill as a local variable during a turn and syncs to Valkey after each change.

### Decision 8: Invalid `allowed-tools` entries log at activation, ignored silently in filter

When active-skill is (re)read at the start of a turn, main-agent cross-checks `activeSkill.AllowedTools` against registered tool names and logs a WARN for each unknown entry:

```
WARN  skill_allowed_tool_unknown  active_skill=code-review  unknown_tool=non-existent
```

The filter pass (Decision 5) is strictly set-membership and does not re-log. The rationale: the spec is "honor what can be honored"; a skill referencing a tool that does not exist is an authoring bug, not a runtime failure.

Alternative considered: reject activation entirely if any entry is unknown. Rejected — would make skill deployment brittle when a skill references an optional MCP tool that happens not to be configured on a given deployment.

### Decision 9: `current_time` tool

```go
// Schema
{
  "type": "function",
  "function": {
    "name": "current_time",
    "description": "Return the current UTC time. Optional format argument accepts a Go time-layout string (e.g. '2006-01-02 15:04:05'); default is RFC3339.",
    "parameters": {
      "type": "object",
      "properties": {
        "format": {"type": "string", "description": "Optional Go time layout; defaults to RFC3339."}
      }
    }
  }
}

// Invoke
args := {Format string}
layout := strings.TrimSpace(args.Format); if layout == "" { layout = time.RFC3339 }
return time.Now().UTC().Format(layout), nil
```

Returns a plain string (not JSON) on success, matching the `read_skill` body contract. Returns `{"error":"..."}` on argument parse failure. Deterministic source-of-truth for "now" — deliberately not dependent on the host timezone.

## Risks / Trade-offs

- **Risk:** Valkey fetch on every turn adds latency. → Mitigation: the existing turn already does several Valkey round-trips (slot request/reply, token pub/sub). One more GET is noise. If measurement shows it isn't, a small in-process cache with short TTL can be added later without changing the spec.
- **Risk:** Active-skill state stale after operator removes a skill from disk. At the next turn, `SchemasFor` is passed the active skill's `allowed-tools` from in-memory `Manifest`; if the skill is gone from the `skills.Store` (e.g. restart + skill removed), `Store.Get(name)` returns `false` and main-agent treats the session as having no active skill (also silently clears the Valkey record). → Mitigation: the "silent clear on missing skill" is explicit in the spec so operators know the behaviour.
- **Risk:** Model discovers a gated tool via the `<available_skills>` manifest or prior conversation and calls it anyway, expecting success. → Accepted. The `outcome=gated` tool-result envelope is structured enough for the model to self-correct on the next iteration. The alternative (silently executing) defeats the purpose of gating.
- **Risk:** `SchemasFor` vs `Schemas()` split could drift — callers that should be filtering might stay on `Schemas()` and re-open the gap. → Mitigation: the main-agent loop is the only production caller for turn execution. Tests and the smoke tool legitimately use `Schemas()`. Code review on future changes should keep callers honest; consider deprecating `Schemas()` once change 4 lands.
- **Risk:** When multiple main-agent instances handle the same session, the Valkey round-trip ordering matters (two instances flipping active-skill concurrently would produce a last-write-wins race). → Accepted. A session's turns serialize at the gateway → context-manager → main-agent pipeline; concurrent active-skill flips for the same session are not a realistic concern in the current architecture. If the pipeline changes later to allow concurrency, revisit.
- **Trade-off:** `current_time` is a base tool even though few skills need it. Token cost is a handful of bytes per turn and the utility is high enough (model knowing "now" is useful for time-aware reasoning, memory writes with accurate timestamps, etc.) that the cost is paid. Can be demoted to "only on request" if token costs become a concern.
- **Trade-off:** No explicit clear operation. Documented as a v1 non-goal. If a user or operator genuinely wants to go back to base without loading a different skill, the workaround is to start a new session. Acceptable for an initial slice.

## Migration Plan

No data migration required:

- New Valkey keys `session:<id>:active-skill` begin appearing for sessions that load a skill. Sessions that never load one produce no keys. Older sessions from before this change continue to work (no key = no active skill = base toolset only, same as today).
- `Schemas()` remains functional; callers that do not opt into filtering keep their current behaviour.
- Rollback: reverting the code is sufficient. The leftover Valkey keys TTL out in 24h and consume negligible memory (one string per active session).

Operational notes:

- The `active_skill_changed` and `skill_allowed_tool_unknown` log lines are new. Update log dashboards if any exist; the shared doc `docs/skills-runtime-design.md` §5.2 covers the trust boundary, not observability.
- No config changes required. `SKILLS_DIR`, `SKILL_FILE_MAX_BYTES`, and the existing env vars continue to behave identically.
- No new dependencies.

## Open Questions

- Should `Schemas()` be formally deprecated in favour of `SchemasFor`, or kept as a back-compat shim? Likely keep for now; revisit after change 4 or when a third caller appears.
- Does the gate-hit `outcome=gated` log line warrant its own dashboard metric? Unlikely for v1 — the existing `tool_invoked` counter aggregated by `outcome` is enough. Revisit if operators ask.
- When the active skill is cleared because the underlying skill disappeared from the filesystem, is a distinct log line (`active_skill_vanished`) useful, or is the existing `active_skill_changed: name=""` sufficient? Likely sufficient; flag in implementation if it turns out to be noisy.
- `current_time` layout argument accepts arbitrary Go layouts. Should we validate against a whitelist to prevent weird strings (e.g. `time.RFC3339` passed as the actual layout string)? Probably not — the behaviour is self-evident and the failure mode (funny-looking timestamps) is harmless.
