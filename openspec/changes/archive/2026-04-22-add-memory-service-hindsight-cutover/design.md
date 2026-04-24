## Context

The full memory-layer reasoning lives in `docs/memory-system-design.md`. This design doc assumes that context and focuses on the architectural and implementation decisions specific to standing up `memory-service`, cutting over from muninndb, and running Hindsight as an in-repo container.

Today's memory access pattern: `context-manager` and `retro-agent` each instantiate a `MuninnClient` (`internal/context/muninn.go`, 414 LOC) and hit muninndb over HTTP. The retro curation job does the heaviest manipulation — Consolidate / Evolve / Link / GetOutgoingLinks — driving a custom abstraction pipeline. Muninn's bugs, documented in `docs/memory-system-design.md` §2, make retrieval unreliable enough that the design doc concludes we should discard its data rather than port it.

Hindsight is a different substrate: it consolidates facts into observations automatically (driven by `observations_mission`), refines observations when new evidence arrives, computes freshness trends, and supports directives and mental models as first-class tunable resources. Much of what the retro curation job does today becomes *Hindsight's* job, driven by mission text we author.

This change introduces `memory-service` as the single HTTP boundary so every consumer of memory — `context-manager`, `retro-agent`, and future curiosity/research/proactive agents — speaks one wire protocol and inherits a uniform policy layer (provenance tagging, bank/mission/directive sync, webhook termination).

## Goals / Non-Goals

**Goals:**

- Memory-service is the only process that speaks to Hindsight; every other service speaks to memory-service over HTTP
- Hindsight runs in this repo's docker-compose, isolated from the reference instance at `192.168.10.125`; its LLM provider points at `llm-proxy` (from the prerequisite proposal) so LLM calls do not thrash agent KV cache
- Memory-service is idempotent at startup: bank / missions / directives synced from versioned YAML in `deploy/memory/`
- Every retained memory carries `metadata.provenance`; extraction missions reference provenance conventions
- Webhooks (`retain.completed`, `consolidation.completed`) terminate at memory-service; handlers for this proposal are stubs that log + ack (real handlers arrive with curiosity/proactive proposals)
- Cutover is clean: `MuninnClient`, muninn tests, and the muninndb container are deleted in the same change
- Existing agent-facing behavior is preserved: the shape of data returned to `context-manager` and `retro-agent` is compatible with their current consumers

**Non-Goals:**

- Curiosity watcher, research agent, proactive agent (follow-up proposals)
- `hypothesize` Mental Model + inferred-fact retain loop (follow-up)
- Salience scoring heuristic (convention exists; scorer lands later)
- Hard-pruning background job (deferred until the bank accumulates enough history to matter)
- Contradiction surfacing UX (behavioral layer, not memory layer)
- Migration of existing muninndb memories (discarded; see proposal)
- Per-call priority signaling from memory-service to llm-proxy for user-facing reflect (open question in design doc §10)
- `POST /v1/memory/correct` endpoint (subsumed by observation refinement)

## Decisions

### Decision 1: Memory-service is a dedicated HTTP service, not a library

**Choice:** New `cmd/memory-service`. All memory consumers speak HTTP/JSON.

**Rationale:** The curiosity/research/proactive agents are webhook-driven. Webhook termination needs a single HTTP endpoint, and that endpoint naturally co-locates with the policy layer (provenance tagging, salience, directive enforcement, bank config sync). Shared-library would force every consumer to host its own webhook endpoint or subscribe to a separate notification bus — much more plumbing.

**Alternatives considered:**
- *Shared `internal/memory` library imported by each service* — less latency, but every consumer needs its own webhook listener and its own Hindsight credentials; scales poorly with more agents
- *Extend llm-broker with memory-service responsibilities* — conflates transport paradigms and muddies the broker's role

### Decision 2: HTTP API is rich rather than thin

**Choice:** Memory-service exposes `/retain`, `/recall`, `/reflect`, `/forget`, and webhook sinks. Handlers apply write-time policy (provenance default, tag routing, metadata normalization) before forwarding to Hindsight. The API is not a blind pass-through.

**Rationale:** The whole point of the boundary is to put policy somewhere consistent. A thin pass-through would push provenance and salience conventions into every caller, defeating the purpose.

**Alternatives considered:**
- *Thin pass-through, callers set provenance* — drifts; every consumer tags differently
- *Rich but also expose raw Hindsight* — two ways to do the same thing; encourages bypass

### Decision 3: Bank / mission / directive seed lives as YAML in the repo, synced at startup

**Choice:** `deploy/memory/bank.yaml`, `deploy/memory/missions/*.yaml`, `deploy/memory/directives/*.yaml`. Memory-service loads these at startup and syncs to Hindsight: GET current config, compute diff, PATCH changed fields; create missing directives; update diverging directives. Idempotent.

**Rationale:** Config-as-code. Reviewable in git. Reproducible across environments. No hidden runtime state that gets lost on a rebuild.

**Alternatives considered:**
- *One-time provisioning script* — not reproducible; drift over time
- *Configure Hindsight via its own UI* — opaque to the repo; impossible to review changes

### Decision 4: Fresh bank, no muninndb migration

**Choice:** `microagent2` bank starts empty. Existing muninndb memories are discarded.

**Rationale:** Muninndb retrieval was unreliable. Porting a bad-data substrate's contents into a new substrate pollutes the new substrate with all of the old one's quality issues. Trust would be re-built from user interactions; the hit is finite because muninndb's stored memories had limited utility.

**Alternatives considered:**
- *Best-effort migration script* — implementation cost, unclear data quality, deferred cleanup risk
- *Parallel bank with migrated data* — increases operational complexity; doesn't solve the quality problem

### Decision 5: Single bank with `observation_scopes: per_tag`

**Choice:** One bank (`microagent2`). Per-topic differentiation comes from `observation_scopes: "per_tag"` at Retain time. Initial tag taxonomy: `identity`, `preferences`, `technical`, `home`, `corrections`, `inferred`, `ephemera`.

**Rationale:** Hindsight v0.5.0 does not expose per-bank freshness/decay thresholds, so multi-bank never would have bought us topic-dependent decay (see design doc §4 and §11). `per_tag` scopes produce one observation set per tag, which is the differentiation we actually need.

**Alternatives considered:**
- *Multi-bank* — operational overhead, no decay benefit at this Hindsight version
- *No scope discrimination* — preferences and technical facts collapse into each other in observation space

### Decision 6: Provenance metadata is a convention, not a schema

**Choice:** `metadata.provenance` is always set on `Retain` calls (defaulting to `explicit` for user-sourced content, `implicit` for observed behavior, `inferred` for agent-derived, `researched` for web-sourced). Hindsight's `metadata` is `Map<string,string>`; numeric fields (`confidence`, `salience`) serialize as strings.

Mixed-provenance handling in observations lives in `observations_mission` phrasing: "When consolidating facts of mixed provenance, the observation SHALL reflect the least-confident provenance among its sources." Reader code (future curiosity/proactive) can inspect `source_facts` returned with `types:["observation"]` recalls to see the actual mix.

**Rationale:** Cheap to add from day one; retrofitting would require re-extracting or backfilling. Convention-not-schema keeps it simple and matches Hindsight's `Map<string,string>` constraint.

**Alternatives considered:**
- *Wait until curiosity/proactive need it* — creates a database of unlabeled memories; expensive to backfill
- *Invent a richer provenance schema in Hindsight* — out of scope; convention suffices for now

### Decision 7: Curation job becomes a trigger, not a manipulator

**Choice:** `internal/retro/job.go`'s `CurationJob` no longer calls Consolidate / Evolve / Link. Instead, it:

1. Periodically triggers Hindsight consolidation (if not already triggered by Retain)
2. Refreshes Mental Models on a configured cadence
3. Emits structured logs of pre/post state for observability

**Rationale:** Hindsight's observation refinement subsumes Consolidate/Evolve; the graph is built from entities during Retain (no explicit Link needed). The curation job's remaining role is scheduling, not content manipulation.

**Alternatives considered:**
- *Delete the curation job entirely* — loses the scheduling and observability logic we still want
- *Keep explicit Consolidate/Evolve calls to Hindsight* — duplicates Hindsight's built-in refinement

### Decision 8: Webhook handlers stub in this proposal, full in follow-ups

**Choice:** `/hooks/hind/retain` and `/hooks/hind/consolidation` endpoints are registered, validate the HMAC signature, log the event, and return 200. No downstream dispatch yet.

**Rationale:** Registering the endpoints now means the Hindsight webhook configuration is in place from day one and in-repo, and the HMAC verification path is exercised. When curiosity/proactive land, they become subscribers to memory-service internal events emitted by these handlers — no external reconfiguration needed.

**Alternatives considered:**
- *Skip webhook registration* — adds configuration drift when curiosity lands
- *Ship full curiosity dispatch here* — blows up scope

### Decision 9: Consumer API shape — memory-service owns response types

**Choice:** `context-manager` and `retro-agent` stop importing `internal/context.Memory` / `internal/context.StoredMemory`. Memory-service defines `MemorySummary` and `RetainSpec` shapes; a small `internal/memoryclient` package (Go client for memory-service, NOT for Hindsight) provides typed access for Go consumers.

**Rationale:** Clean break from muninn's types. Future substrate swaps (unlikely but possible) don't ripple into every consumer.

**Alternatives considered:**
- *Reuse `internal/context.Memory`* — carries muninn-era assumptions (engram IDs, relation types we no longer emit)
- *Re-export Hindsight's types directly* — couples every consumer to Hindsight's API

## Risks / Trade-offs

- **Risk:** Hindsight observation consolidation is asynchronous. Immediately after a Retain, the new memory is retrievable as a raw fact but may not yet appear in observations. → **Mitigation:** Default recall `types` to `world`+`experience`; consumers that need observations pass `types:["observation"]` explicitly and accept that recency lag.
- **Risk:** Hindsight memory leak (issue #996, ~1GB/hr in containers). → **Mitigation:** Container memory limit (default 2GB) + `restart: unless-stopped` policy; memory-service health endpoint surfaces `hindsight_restart_count`; docker-compose alerts visible in logs.
- **Risk:** Mission text drift between YAML and live Hindsight config. → **Mitigation:** Memory-service always PATCHes to match YAML at startup — YAML wins; any ad-hoc edits via Hindsight UI are overwritten on restart.
- **Risk:** Memory-service becomes a single point of failure for memory access. → **Mitigation:** Stateless service, fast startup, restart policy. Outages fail memory operations loudly rather than silently corrupting state. Health endpoint surfaces Hindsight connectivity.
- **Risk:** Webhook HMAC secret rotation. → **Mitigation:** Secret in env var (`HINDSIGHT_WEBHOOK_SECRET`); memory-service reads on startup; operator rotates both ends and restarts both services in lockstep.
- **Trade-off:** Extra network hop for every memory call (consumer → memory-service → Hindsight, vs. consumer → muninndb). Acceptable because memory calls are not on the hot per-token path; they happen once per user turn for recall and a few times per retro cycle for store.
- **Trade-off:** Deleting muninndb means a brief period with no historical memory at cutover. Acceptable because muninndb memories were of questionable quality anyway.

## Migration Plan

This is a cutover, not a rolling migration. Steps:

1. **Prerequisite confirmed**: `add-llm-proxy-slot-classes` is landed; llm-proxy is running with `HINDSIGHT_SLOT_COUNT > 0`.
2. **Deploy new infrastructure**: add Hindsight + memory-service to docker-compose. Start both. Verify health endpoints.
3. **Seed sync**: memory-service reads `deploy/memory/*.yaml` and PATCHes Hindsight bank/missions/directives. Verify via logs that sync is idempotent on a second restart.
4. **Cutover consumers**: flip `context-manager`, `retro-agent`, and `tests/integration_test.go` to use memory-service. Run tests green.
5. **Verify live traffic**: smoke an agent turn → retro cycle → observation consolidation. Spot-check memory-service logs for provenance tagging, Hindsight connectivity.
6. **Delete muninndb**: remove `MuninnClient`, `internal/context/muninn.go`, muninndb container from docker-compose, `MUNINN_*` env vars from `.env.example`.
7. **Rollback**: if any step fails, revert the change; muninndb is still available until step 6. After step 6, rollback requires re-adding the muninndb container and reverting the cutover — a separate change, not a same-day rollback.

## Open Questions

- **Reflect priority for user-facing recalls.** When memory-service issues a `/reflect` during a user-facing turn, it wants user-priority latency rather than background. Two shapes: (a) Hindsight forwards provider-passthrough headers so memory-service can signal priority to llm-proxy per-call; (b) memory-service bypasses Hindsight's reflect for user-facing synthesis and calls llm-proxy directly. Requires spiking Hindsight's passthrough behavior. Not in scope for this proposal; defer until the first proactive/curiosity work surfaces concrete latency pain.
- **Mental Model cadence.** The curation job refreshes Mental Models on a schedule. What's the right interval? Start conservative (hourly?); tune once we observe actual consolidation and usage patterns.
- **Consumer-side error handling for Hindsight transient failures.** Should memory-service retry 5xx responses from Hindsight on behalf of consumers, or surface them? Lean retry-with-timeout for idempotent operations (recall, reflect) and surface for mutations (retain, forget).
