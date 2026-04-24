## 1. memory-service registration + heartbeat

- [ ] 1.1 Add registration goroutine to `cmd/memory-service/main.go` using `registry.NewAgentRegistrar` pattern from main-agent/retro-agent
- [ ] 1.2 Construct `messaging.RegisterPayload` with `agent_id: "memory-service"`, `capabilities: ["memory"]`, `heartbeat_interval_ms` from env (default 3000), `preemptible: false`
- [ ] 1.3 Register at startup, deregister on SIGINT/SIGTERM before exit
- [ ] 1.4 Heartbeat goroutine publishing to `channel:heartbeat:memory-service`
- [ ] 1.5 Verify memory-service appears in `GET /v1/status` registered-agents list after deploy

## 2. Panel descriptor construction

- [ ] 2.1 New `internal/memoryservice/panel.go` that builds a `dashboard.PanelDescriptor` for memory-service
- [ ] 2.2 Title `"Memory"`, order `200`, version `1`
- [ ] 2.3 Form section with config_key `memory`, six fields as specified (recall_limit, prewarm_limit, recall_default_types enum, default_provenance enum, tag_taxonomy string, memory_bank_id readonly)
- [ ] 2.4 Iframe section reads `MEMORY_SERVICE_CP_URL` env (default `http://localhost:9999`) at descriptor build time; height `800px`
- [ ] 2.5 Attach descriptor to `RegisterPayload` before publishing
- [ ] 2.6 Unit test validating the descriptor shape matches the spec (fields, defaults, enum values)

## 3. Config extensions

- [ ] 3.1 In `internal/config/config.go`, extend `MemoryConfig` with `RecallDefaultTypes` (string), `DefaultProvenance` (string), `TagTaxonomy` (string)
- [ ] 3.2 `DefaultMemoryConfig` sets `observation`, `explicit`, `identity,preferences,technical,home,ephemera` respectively
- [ ] 3.3 `ResolveMemory` reads the new keys from Valkey with defaults falling back to `DefaultMemoryConfig`
- [ ] 3.4 Mark `Vault`, `MaxHops`, `StoreConfidence`, `RecallThreshold` with `// deprecated: no longer used after add-memory-panel-contribution` comment; keep fields for backward-compat
- [ ] 3.5 Config tests covering new keys and fallback behavior

## 4. Memory-service handler wiring

- [ ] 4.1 Pass `ResolveMemory` result (or a `configStore` reference) into the `memoryservice.Server`
- [ ] 4.2 `handleRecall` reads `recall_default_types` per request; map to Hindsight `types` list: `"observation" → ["observation"]`, `"world_experience" → ["world","experience"]`, `"all" → ["observation","world","experience"]`
- [ ] 4.3 `handleRetain` reads `default_provenance` per request; if invalid, fall back to `"explicit"` with WARN log
- [ ] 4.4 Unit tests covering each recall-type mapping and the provenance default + invalid-value-fallback behavior
- [ ] 4.5 Ensure the `memory_bank_id` readonly field in the descriptor reflects the actual configured bank (pull from `cfg.BankID`)

## 5. Docker compose + env

- [ ] 5.1 Add `MEMORY_SERVICE_CP_URL=http://localhost:${HINDSIGHT_CP_PORT:-9999}` to `memory-service` service in `docker-compose.yml`
- [ ] 5.2 Document new env vars in `.env.example`
- [ ] 5.3 Add heartbeat interval env (reuse `HEARTBEAT_INTERVAL_MS`)

## 6. Iframe compatibility spike

- [ ] 6.1 Verify Hindsight's CP renders inside an iframe from a different origin (localhost → localhost:9999). Check for X-Frame-Options or CSP frame-ancestors headers
- [ ] 6.2 If CP refuses to embed, document the finding in the design doc's Open Questions, fall back to a link-out pattern (still in the descriptor but rendered as a prominent button rather than iframe). This is a spec-level fallback — the iframe section kind supports a `fallback: "link"` attribute in that case, added as a descriptor extension

## 7. Validation

- [ ] 7.1 `go build ./...` green
- [ ] 7.2 `go test ./...` green; new memoryservice panel + config tests green
- [ ] 7.3 `openspec validate add-memory-panel-contribution --strict` green
- [ ] 7.4 Manual: deploy; visit `/`; Memory tab appears; form loads current values; Save persists to Valkey; memory-service's next recall reflects new defaults; Hindsight CP iframe loads and is interactive
- [ ] 7.5 Manual: stop memory-service; after heartbeat timeout, Memory tab disappears on dashboard refresh
