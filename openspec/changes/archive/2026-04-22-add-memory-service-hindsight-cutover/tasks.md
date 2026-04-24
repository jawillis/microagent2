## 1. Hindsight Go client (`internal/hindsight`)

- [x] 1.1 Create `internal/hindsight` package with a typed `Client` struct (addr, api-key, http.Client, logger)
- [x] 1.2 Implement `Retain(ctx, bankID, RetainRequest) (*RetainResponse, error)` matching Hindsight's `POST /memories` RetainRequest schema (items, async, document_tags)
- [x] 1.3 Implement `Recall(ctx, bankID, RecallRequest) (*RecallResponse, error)` for `POST /memories/recall` including `types`, `tags`, `limit`, returning both `results` and `source_facts`
- [x] 1.4 Implement `Reflect(ctx, bankID, ReflectRequest) (*ReflectResponse, error)` for `POST /reflect`
- [x] 1.5 Implement `DeleteMemory(ctx, bankID, memoryID) error` for `DELETE /memories/{id}`
- [x] 1.6 Implement `GetMemoryHistory(ctx, bankID, memoryID) (*MemoryHistoryResponse, error)` for `GET /memories/{id}/history`
- [x] 1.7 Implement `ListBanks(ctx)`, `GetBank(ctx, bankID)`, `CreateBank(ctx, CreateBankRequest)`, `GetBankConfig(ctx, bankID)`, `PatchBankConfig(ctx, bankID, BankConfigUpdate)`
- [x] 1.8 Implement `ListDirectives`, `CreateDirective`, `UpdateDirective`, `DeleteDirective`
- [x] 1.9 Implement `ListWebhooks`, `CreateWebhook`, `UpdateWebhook`, `DeleteWebhook`
- [x] 1.10 Implement `Consolidate(ctx, bankID) (*ConsolidateResponse, error)` for `POST /consolidate`
- [ ] 1.11 Implement `RefreshMentalModel(ctx, bankID, modelID)` for `POST /mental-models/{id}/refresh` — deferred (Mental Models arrive with the curiosity/proactive follow-up proposal; surface added then rather than speculatively)
- [x] 1.12 All methods respect `ctx` cancellation; non-2xx responses return structured errors that include status + body
- [x] 1.13 Unit tests against `httptest.Server` for every method, covering success + error paths

## 2. `cmd/memory-service` skeleton

- [x] 2.1 Create `cmd/memory-service/main.go` with env-driven config per spec (`HINDSIGHT_ADDR`, `HINDSIGHT_API_KEY`, `HINDSIGHT_WEBHOOK_SECRET`, `MEMORY_BANK_ID`, `MEMORY_SERVICE_HTTP_ADDR`, `MEMORY_SERVICE_EXTERNAL_URL`, `MEMORY_YAML_DIR`)
- [x] 2.2 Create `internal/memoryservice/server.go` with http mux, slog logger, and a `hindsight.Client`
- [x] 2.3 `GET /health` handler returning reachability status of Hindsight
- [x] 2.4 Graceful shutdown: drain in-flight requests, log shutdown
- [x] 2.5 Fail startup with structured ERROR if required env vars are missing

## 3. Memory-service handlers

- [x] 3.1 `POST /retain` handler: parse MemoryServiceRetainRequest, apply provenance defaulting (explicit), validate provenance enum, normalize numeric metadata to strings, call `hindsight.Retain`, return response
- [x] 3.2 `POST /recall` handler: parse request, default `types` to `["world","experience"]`, call `hindsight.Recall`, translate results into `MemorySummary[]` + optional `source_facts`
- [x] 3.3 `POST /reflect` handler: pass-through with bank ID
- [x] 3.4 `POST /forget` handler: accept either `memory_id` or `query`; for `query`, call recall to find best match; then call `hindsight.DeleteMemory`
- [x] 3.5 Structured logging per request (correlation ID propagated from `X-Correlation-ID` header or generated)
- [x] 3.6 Tests: handler-level unit tests with a fake `hindsight.Client` for retain defaults, provenance validation, recall types default, forget by id and by query

## 4. Startup YAML sync

- [x] 4.1 Define Go structs for the YAML shape in `deploy/memory/`: `bank.yaml` (bank id, disposition, config overrides), `missions/*.yaml` (named mission text), `directives/*.yaml` (name, content, tags, priority)
- [x] 4.2 YAML loader in `internal/memoryservice/config.go` — reads `MEMORY_YAML_DIR`, parses bank + missions + directives, validates schema
- [x] 4.3 Bank sync: if bank missing, `CreateBank`; else diff config fields against `GetBankConfig` and PATCH changed
- [x] 4.4 Directive sync: list existing directives; create if name missing, update if content or priority differs, leave others untouched
- [x] 4.5 Webhook registration: list existing webhooks, create/update per spec (urls for `/hooks/hind/retain` and `/hooks/hind/consolidation`, HMAC secret, event types)
- [x] 4.6 Sync retried on a 30s timer if Hindsight unreachable; health endpoint reports sync status
- [x] 4.7 Sync idempotency test: run sync twice, assert no unnecessary PATCH calls on the second run

## 5. Webhook handlers (stubs)

- [x] 5.1 `POST /hooks/hind/retain` handler: verify HMAC-SHA256, log at INFO, return 200
- [x] 5.2 `POST /hooks/hind/consolidation` handler: verify HMAC-SHA256, log at INFO, return 200
- [x] 5.3 Shared HMAC verification helper; reject with 401 on missing/invalid signature and log WARN
- [x] 5.4 Tests: valid signature accepted, invalid rejected, missing rejected

## 6. Initial bank / mission / directive YAML

- [x] 6.1 `deploy/memory/bank.yaml` — `bank_id: microagent2`, disposition values, placeholder mission references
- [x] 6.2 `deploy/memory/missions/retain_mission.yaml` — extraction guidance aligned with microagent2's extraction intelligence (concept phrasing, three-layer tags, memory-type constraints, confidence from certainty)
- [x] 6.3 `deploy/memory/missions/observations_mission.yaml` — consolidation guidance (merge/abstract/evolve distinctions, abstract-protection rules, mixed-provenance reflection)
- [x] 6.4 `deploy/memory/missions/reflect_mission.yaml` — synthesis guidance for microagent2's user-facing personality
- [x] 6.5 `deploy/memory/directives/*.yaml` — initial directives for user-subjective authority, external-facts-require-sources, researched-claims-cite-sources, inferred-memories-require-ratification (per design doc §7)
- [x] 6.6 YAML validated by memory-service loader on first startup; checked-in samples for test fixtures

## 7. Consumer go client (`internal/memoryclient`)

- [x] 7.1 Create `internal/memoryclient` package with typed client (base URL, http.Client, logger)
- [x] 7.2 Methods: `Retain(ctx, RetainRequest) (*RetainResponse, error)`, `Recall(ctx, RecallRequest) (*RecallResponse, error)`, `Reflect(ctx, ReflectRequest) (*ReflectResponse, error)`, `Forget(ctx, ForgetRequest) error`
- [x] 7.3 `MemorySummary` type with `ID`, `Content`, `Score`, `Tags`, `Metadata` fields — what consumers actually need
- [x] 7.4 Correlation ID propagation via `X-Correlation-ID` header
- [x] 7.5 Unit tests with `httptest.Server` covering every method and error-response translation

## 8. Cutover: `context-manager`

- [x] 8.1 In `internal/context/manager.go`, replace `*MuninnClient` with `*memoryclient.Client`; call `Recall(ctx, RecallRequest{Query, Limit})` instead of `muninn.Recall`
- [x] 8.2 Translate `MemorySummary` → internal shape used by existing recall-assembly; keep the `<context>` block rendering unchanged
- [x] 8.3 Prewarm path calls `Recall` against the last assistant message (same shape as today)
- [x] 8.4 In `cmd/context-manager/main.go`, drop muninn wiring; read `MEMORY_SERVICE_ADDR` and construct memoryclient
- [x] 8.5 Update `internal/context/manager_test.go` (if present) and `tests/integration_test.go` context-manager cases to use a fake memoryclient
- [x] 8.6 All context-manager tests green

## 9. Cutover: `retro-agent`

- [x] 9.1 In `internal/retro/job.go`, replace `*appcontext.MuninnClient` field on `MemoryExtractionJob` and `SkillCreationJob` with `*memoryclient.Client`
- [x] 9.2 Rework `MemoryExtractionJob.Store` call: build `RetainRequest` with tags (including taxonomy tag), `metadata.provenance`, string-serialized `confidence`, `observation_scopes: "per_tag"`
- [x] 9.3 Rework `SkillCreationJob`: Recall-duplicate check → memoryclient.Recall; store with `skill` tag + `provenance: implicit`
- [x] 9.4 Rewrite `CurationJob`: delete the `merge` / `evolve` / `delete` action-execution paths; add consolidation-trigger method calling memoryclient (which proxies to Hindsight's `/consolidate`); add mental-model-refresh cycle on a cadence env var (`RETRO_MENTAL_MODEL_REFRESH_S`, default 3600)
- [x] 9.5 Remove `curationMuninn` interface
- [x] 9.6 In `cmd/retro-agent/main.go`, drop muninn wiring; construct memoryclient from `MEMORY_SERVICE_ADDR`
- [x] 9.7 Update `internal/retro/job_test.go` to use a fake memoryclient; delete tests specifically for merge/evolve/delete action execution; add tests for consolidation triggering and mental-model refresh
- [x] 9.8 All retro-agent tests green

## 10. Cutover: integration tests + gateway

- [x] 10.1 Update `tests/integration_test.go` to construct a real memory-service-backed test harness (or a fake memoryclient) in place of `MuninnClient`
- [x] 10.2 In `internal/gateway/server.go`, drop the `muninnAddr` field and related wiring; remove from `Server` constructor and `cmd/gateway/main.go`
- [x] 10.3 Remove muninn-related dashboard fields from `internal/gateway/web/app.js` if present
- [x] 10.4 Full test suite green: `go test ./...`

## 11. docker-compose and deploy

- [x] 11.1 Add `hindsight` service to `docker-compose.yml` — image, volume, memory limit (default 2GB), `restart: unless-stopped`, env for LLM provider pointing at llm-proxy (`http://llm-proxy:8082` or the configured port)
- [x] 11.2 Add `memory-service` service to `docker-compose.yml` with env for Hindsight, bank ID, external URL, HTTP addr, yaml dir (volume-mounted to `deploy/memory/`)
- [ ] 11.3 Configure memory-service to depend on hindsight via compose healthcheck — deferred: Hindsight's container does not document a standard healthcheck endpoint. Current compose uses `service_started` depends_on; operator can add a healthcheck once they know which image they're running.
- [x] 11.4 Add `MEMORY_SERVICE_ADDR` env to `context-manager` and `retro-agent` services; remove `MUNINN_*` env
- [x] 11.5 Update `.env.example` with the new env vars; remove `MUNINN_*` entries
- [ ] 11.6 Smoke: `docker compose up`, verify memory-service logs show bank sync + webhook registration + health ok — operator-run verification; needs `HINDSIGHT_IMAGE` set in `.env`

## 12. Muninn deletion

- [x] 12.1 Delete `internal/context/muninn.go` and `internal/context/muninn_test.go`
- [x] 12.2 Delete `muninndb` (or equivalent) service definition from `docker-compose.yml` — muninndb was never a compose service (external dependency at host.docker.internal); removed env references instead
- [x] 12.3 Remove all `MUNINN_*` references from `.env.example`, compose files, docs
- [x] 12.4 Remove `appcontext.MuninnClient` / `appcontext.Memory` / `appcontext.StoredMemory` types and any dead helper types tied to muninn
- [x] 12.5 `go build ./...` green, `go test ./...` green, no stale references to muninn in source grep (archived changes excluded)

## 13. Validation

- [x] 13.1 `go build ./...` green
- [x] 13.2 `go test ./...` green
- [x] 13.3 `openspec validate add-memory-service-hindsight-cutover --strict` green
- [ ] 13.4 Manual end-to-end: bring up full docker-compose, send a user turn through gateway, verify — retain writes a memory with provenance=explicit; recall on a later turn returns it; retro curation cycle triggers a Hindsight consolidation; health endpoint reports `hindsight: reachable` — operator-run, requires `HINDSIGHT_IMAGE` and a real Hindsight deployment
- [ ] 13.5 Verify webhook delivery: cause a retain, watch memory-service logs for `memory_webhook_received` with valid signature — operator-run
- [ ] 13.6 Verify YAML-wins semantics: manually edit a mission in Hindsight, restart memory-service, confirm YAML value is restored — operator-run
- [ ] 13.7 Regression: agent turn latency not materially worse than with muninn; llm-proxy slot snapshot shows hindsight-class activity during extraction and consolidation — operator-run
