## Why

The muninndb backend has material bugs that make retrieval unpredictable and hard to debug (see `docs/memory-system-design.md` §2). The exploration toward a homegrown Postgres+pgvector substrate was set aside in favor of Hindsight (vectorize-io/hindsight), which implements vector+keyword+graph+temporal retrieval, conflict-driven observation refinement, missions/directives as a customization surface, and webhooks — all of which we would otherwise have to build.

Today, two services (`context-manager`, `retro-agent`) and the integration tests each instantiate their own `MuninnClient` and hit muninndb directly. That pattern leaks memory-substrate concerns into every consumer and blocks the upcoming webhook-driven agents (curiosity, research, proactive). This proposal introduces `memory-service` as the single HTTP boundary for memory operations, cuts the existing services over to it, stands up Hindsight in-repo via docker-compose (routed through llm-proxy from the prerequisite proposal), and deletes muninndb.

**BREAKING**: `MuninnClient` and all muninndb infrastructure are removed. Existing muninndb data is not migrated — the new bank starts clean. This is defensible because muninndb retrieval behavior was unreliable enough that porting its contents would pollute the new substrate.

## What Changes

- New `cmd/memory-service` — Go HTTP service exposing `POST /retain`, `POST /recall`, `POST /reflect`, `POST /forget`, webhook sinks at `/hooks/hind/retain` and `/hooks/hind/consolidation` (stub handlers that log-and-ack for now), and `GET /health`
- New `internal/hindsight` Go client for Hindsight's REST API (Retain, Recall, Reflect, Directives, MentalModels, BankConfig, Webhooks, MemoryHistory, Entities)
- New `deploy/memory/` YAML seed files for the `microagent2` bank, its missions (`retain_mission`, `observations_mission`, `reflect_mission`), and directives; memory-service syncs these to Hindsight at startup via idempotent PATCH
- Hindsight container added to `docker-compose.yml` (not the reference instance at `192.168.10.125`; a fresh deployment local to this repo) — with container memory limit and restart policy to mitigate known memory leak (#996). Hindsight's `llm_provider` points at `llm-proxy` (from the prerequisite proposal)
- Provenance metadata convention: every `Retain` call emits `metadata.provenance` (one of `explicit` / `implicit` / `inferred` / `researched`); numeric metadata (`confidence`, `salience`) serialized as strings per Hindsight's `Map<string,string>` metadata constraint
- Cutover: `internal/context/manager.go`, `internal/retro/job.go`, and `tests/integration_test.go` stop importing `MuninnClient` and call `memory-service` over HTTP instead
- Curation job in `internal/retro/job.go` shrinks dramatically — most `Consolidate` / `Evolve` / `Link` logic moves into Hindsight via the `observations_mission`; the job becomes a consolidation trigger + a periodic mental-model refresh
- **BREAKING** — `MuninnClient` and `internal/context/muninn.go` deleted; muninndb service removed from `docker-compose.yml`; `MUNINN_*` env vars retired
- Single bank, per-tag observation scopes (no multi-bank split); initial tags: `identity`, `preferences`, `technical`, `home`, `corrections`, `inferred`, `ephemera`

## Capabilities

### New Capabilities

- `memory-service`: HTTP gateway for all memory operations against Hindsight; owns bank/mission/directive configuration sync; terminates Hindsight webhooks; enforces write-time conventions (provenance metadata, tag taxonomy)

### Modified Capabilities

- `context-management`: recall path changes from direct `MuninnClient.Recall` to HTTP call against memory-service; prewarm path same; no muninn-specific fields leak through
- `retrospection`: memory extraction `Store` path goes through memory-service; curation job simplifies (Consolidate/Evolve/Link become Hindsight-side observation refinement); skill recall path goes through memory-service

## Impact

- New binary: `cmd/memory-service`
- New package: `internal/hindsight`
- New deploy artifacts: `deploy/memory/*.yaml`
- **Deleted**: `internal/context/muninn.go`, `internal/context/muninn_test.go`, muninndb container definition, `MUNINN_*` env vars
- Modified: `internal/context/manager.go` (replace MuninnClient with memory-service HTTP client), `internal/retro/job.go` (replace MuninnClient; drastically simplify CurationJob), `internal/gateway/server.go` (drop `muninnAddr` field and related wiring), `cmd/retro-agent/main.go` (drop muninn wiring), `cmd/context-manager/main.go` (drop muninn wiring), `tests/integration_test.go` (use memory-service)
- New env vars: `MEMORY_SERVICE_ADDR` (consumers), `HINDSIGHT_ADDR` + `HINDSIGHT_API_KEY` (memory-service only), `MEMORY_BANK_ID` (memory-service, default `microagent2`), `HINDSIGHT_WEBHOOK_SECRET` (shared HMAC secret)
- Test surface: unit tests for `internal/hindsight` client against a fake Hindsight; unit tests for memory-service handlers; integration test exercising the full chain (agent → memory-service → Hindsight → llm-proxy → llama-server) with Hindsight running in docker-compose
- Data: no migration; fresh bank. Existing muninndb memories are discarded at cutover
- Operational: Hindsight adds one more container; memory-service adds one more binary; total compose footprint grows by two
