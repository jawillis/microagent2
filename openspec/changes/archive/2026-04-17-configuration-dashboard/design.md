## Context

microagent2 is a multi-service LLM orchestration system (gateway, context-manager, llm-broker, main-agent, retro-agent) communicating over Valkey streams. All tunable parameters are either hardcoded or set via environment variables at deploy time. There is no runtime configuration UI, no session visibility, and no way to trigger retrospection jobs on demand. The operator (who is also the end user) must redeploy containers or connect to Valkey directly to change behavior.

The gateway already serves an OpenAI-compatible HTTP API. All services already depend on Valkey via go-redis.

## Goals / Non-Goals

**Goals:**
- Centralized config store in Valkey that all services read from, with env var fallback
- Web dashboard served from the gateway for config editing, session management, health monitoring, and retro job triggering
- Hybrid session ID strategy so clients can maintain conversation continuity
- Config changes take effect on service restart (no hot-reload complexity)

**Non-Goals:**
- Multi-user authentication or authorization on the dashboard
- Hot-reload of config changes without restart
- Real-time WebSocket push for live dashboard updates (polling is sufficient)
- Custom agent creation or deployment from the dashboard
- Log viewing or log aggregation

## Decisions

### 1. Config lives in Valkey, not a file

**Decision**: Store tunable config in Valkey `config:<section>` keys as JSON hashes. Services read Valkey first, fall back to env vars.

**Why**: Valkey is already a dependency for every service. No new infrastructure needed. JSON hashes allow partial updates without read-modify-write on the entire config. The gateway can read/write config without filesystem access to other containers.

**Alternatives considered**:
- Config file (YAML/JSON) mounted into containers — requires shared volume or config distribution mechanism, adds deployment complexity
- Dedicated config service — over-engineered for a single-user system
- Environment variables only — no runtime editing without redeployment

**Config keyspace**:
```
config:chat    → {"system_prompt": "...", "model": "default", "request_timeout_s": 120}
config:memory  → {"recall_limit": 5, "recall_threshold": 0.5, "max_hops": 2, "prewarm_limit": 3, "vault": "default", "store_confidence": 0.9}
config:broker  → {"slot_count": 4, "preempt_timeout_ms": 5000}
config:retro   → {"inactivity_timeout_s": 300, "skill_dup_threshold": 0.85, "min_history_turns": 4, "curation_categories": ["preference","fact","context","skill"]}
```

### 2. Dashboard embedded in the gateway

**Decision**: Serve dashboard static files and config/session/health APIs from the existing gateway service. No separate dashboard service.

**Why**: The gateway already handles HTTP. Adding routes is simpler than adding a sixth container. The dashboard is a thin read/write layer over Valkey and health checks — no complex business logic.

**Alternatives considered**:
- Separate dashboard service — adds deployment complexity, another container, another port. Warranted for multi-tenant but not for single-user.
- CLI-only config tool — functional but poor UX for iterative tuning

### 3. Vanilla HTML/CSS/JS dashboard (no framework)

**Decision**: Build the dashboard as a static SPA using vanilla HTML, CSS, and JS. No React, no build step.

**Why**: The dashboard is five panels of forms and tables. A framework adds build complexity, node_modules, and a JS toolchain to a Go project — all for CRUD forms. Vanilla JS with fetch() is sufficient. The entire dashboard can ship as a handful of files embedded in the gateway binary.

**Alternatives considered**:
- React/Vue/Svelte — overkill for the complexity level, adds build toolchain
- Server-rendered templates (Go html/template) — viable but less interactive, harder to do tabbed navigation without full page reloads

### 4. Hybrid session ID via optional request field

**Decision**: Add optional `session_id` field to the chat completions request. If absent, gateway generates a UUID. The session ID is returned in the `X-Session-ID` response header and in the response body.

**Why**: Stays OpenAI-compatible (unknown fields are ignored by conforming clients). Clients that care about sessions can provide an ID. Clients that don't get auto-generated sessions with continuity across the same conversation.

**Session ID generation when absent**: UUIDv4 (random). The current SHA1-based derivation is replaced entirely.

### 5. Retro job locking via Valkey keys with TTL

**Decision**: Before running a retro job, acquire a lock key `retro:lock:<session>:<job_type>` with a TTL equal to a reasonable max job duration (e.g., 5 minutes). If the key exists, the job is already running and the trigger is rejected.

**Why**: The retro agent's inactivity trigger and the dashboard's manual trigger could fire simultaneously for the same session. A simple SET NX with TTL prevents duplicate work without a distributed lock library.

**Alternatives considered**:
- Distributed mutex (Redlock) — over-engineered for a single-instance system
- In-memory lock in the retro agent — doesn't work when the trigger comes from the gateway (different process)

### 6. Config resolution order

**Decision**: Each service at startup reads config in this order:
1. Load env vars as defaults
2. Read corresponding `config:*` key from Valkey
3. For each field: Valkey value wins if present, otherwise env var, otherwise hardcoded default

**Why**: Preserves backward compatibility — existing env-var-based deployments keep working. Dashboard writes override env vars without touching container config. Clear precedence avoids confusion.

## Risks / Trade-offs

**[Config drift between dashboard and env vars]** → The dashboard value silently overrides the env var. Mitigation: The System panel shows both the effective value and the env var value, so the operator can see when they diverge.

**[Restart required for config changes]** → Acceptable per user decision. Mitigation: The dashboard clearly labels settings as "takes effect on restart." Future iteration could add hot-reload for specific settings.

**[Retro job lock TTL too short]** → If a retro job takes longer than the TTL, the lock expires and a duplicate could start. Mitigation: Set TTL conservatively (5 minutes). Jobs that complete normally delete the lock early. Log a warning if a lock is found during cleanup.

**[No authentication on dashboard]** → Anyone with network access to the gateway port can change config. Acceptable for a self-hosted single-user system. Mitigation: Document that the gateway should not be exposed to untrusted networks. Future iteration could add basic auth.

**[Static dashboard in Go binary increases binary size]** → Embedding HTML/CSS/JS via `embed.FS` adds a few hundred KB at most. Negligible trade-off.
