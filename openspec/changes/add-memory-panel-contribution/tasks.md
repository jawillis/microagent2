## 1. memory-service registration + heartbeat

- [x] 1.1 Add registration goroutine to `cmd/memory-service/main.go` using `registry.NewAgentRegistrar` pattern from main-agent/retro-agent
- [x] 1.2 Construct `messaging.RegisterPayload` with `agent_id: "memory-service"`, `capabilities: ["memory"]`, `heartbeat_interval_ms` from env (default 3000), `preemptible: false`
- [x] 1.3 Register at startup, deregister on SIGINT/SIGTERM before exit
- [x] 1.4 Heartbeat goroutine publishing to `channel:heartbeat:memory-service` (via AgentRegistrar.RunHeartbeat)
- [x] 1.5 Verify memory-service appears in registry/panels response after deploy (live: order 200 panel present)

## 2. Panel descriptor construction

- [x] 2.1 New `internal/memoryservice/panel.go` with `BuildPanelDescriptor(cpURL, bankID)`
- [x] 2.2 Title `"Memory"`, order `200`, version from `dashboard.CurrentDescriptorVersion`
- [x] 2.3 Form section with config_key `memory`, six fields as specified (recall_limit, prewarm_limit, recall_default_types enum, default_provenance enum, tag_taxonomy string, memory_bank_id readonly)
- [x] 2.4 Iframe section reads `MEMORY_SERVICE_CP_URL` env (default `http://localhost:9999`) at startup; height `800px`
- [x] 2.5 Attach descriptor to `RegisterPayload` before publishing
- [x] 2.6 Unit tests in `internal/memoryservice/panel_test.go`: descriptor passes ValidateDescriptor; fields match spec; readonly bank_id surfaces configured value; iframe URL round-trips; enum values match config helpers

## 3. Config extensions

- [x] 3.1 Extend `internal/config/config.go` `MemoryConfig` with `RecallDefaultTypes` (string), `DefaultProvenance` (string), `TagTaxonomy` (string)
- [x] 3.2 `DefaultMemoryConfig` sets `observation`, `explicit`, `identity,preferences,technical,home,ephemera` respectively (via `DefaultRecallTypes`, `DefaultProvenance`, `DefaultTagTaxonomy` constants)
- [x] 3.3 `ResolveMemory` reads the new keys from Valkey with defaults falling back to `DefaultMemoryConfig`; env vars `RECALL_DEFAULT_TYPES`, `DEFAULT_PROVENANCE`, `TAG_TAXONOMY` honored at bootstrap
- [x] 3.4 Mark `Vault`, `MaxHops`, `StoreConfidence`, `RecallThreshold` deprecated via struct comment; kept for backward-compat; removed from `DefaultMemoryConfig` and env-reading paths
- [x] 3.5 Config tests cover new keys, fallback behavior, and silent tolerance of deprecated keys in Valkey
- [x] 3.6 Added `ValidProvenance()` and `ValidRecallTypes()` helpers for the panel descriptor to reference

## 4. Memory-service handler wiring

- [x] 4.1 Pass a `Resolver func(ctx) config.MemoryConfig` into `memoryservice.Server` via Config (runtime-tunable lookup per request)
- [x] 4.2 `handleRecall` reads default via `s.defaultRecallTypes(ctx, corrID)`; maps `"observation" → ["observation"]`, `"world_experience" → ["world","experience"]`, `"all" → ["observation","world","experience"]`; invalid value falls back to observation with WARN
- [x] 4.3 `handleRetain` reads default via `s.defaultProvenance(ctx, corrID)`; invalid configured value falls back to `"explicit"` with WARN
- [x] 4.4 Unit tests for each recall-type mapping, provenance default, invalid-value fallback, caller-override precedence
- [x] 4.5 `memory_bank_id` readonly descriptor field reflects `cfg.BankID` at panel build time

## 5. Docker compose + env

- [x] 5.1 Added `MEMORY_SERVICE_CP_URL=http://localhost:${HINDSIGHT_CP_PORT:-9999}` to memory-service in `docker-compose.yml`
- [x] 5.2 Added `VALKEY_ADDR` and `HEARTBEAT_INTERVAL_MS` envs to memory-service (needed for registry)
- [x] 5.3 Documented via compose defaults; `.env.example` already carries `HINDSIGHT_CP_PORT`
- [x] 5.4 Added `valkey: service_healthy` to memory-service's depends_on

## 6. Iframe compatibility spike

- [x] 6.1 Verified Hindsight's CP at `http://localhost:9999` responds without `X-Frame-Options` or `Content-Security-Policy: frame-ancestors` headers — embeds cleanly in a cross-origin iframe with our sandbox attributes (`allow-scripts allow-same-origin allow-forms`)
- [x] 6.2 No fallback needed; iframe section renders as designed

## 7. Validation

- [x] 7.1 `go build ./...` green
- [x] 7.2 `go test ./...` green; new `internal/memoryservice/panel_test.go` + new resolver-driven handler tests green; config tests updated for the new shape
- [x] 7.3 `openspec validate add-memory-panel-contribution --strict` green
- [x] 7.4 Manual: deploy; `/v1/dashboard/panels` returns memory-service's panel at order 200 with form+iframe sections; Hindsight CP iframe URL resolves to `http://localhost:9999`
- [ ] 7.5 Manual: stop memory-service; after heartbeat timeout, Memory tab disappears from the aggregation — deferred operator-run verification, requires browser-level check
