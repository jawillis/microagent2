## 1. `internal/logstream` package

- [x] 1.1 `internal/logstream/handler.go` — `Handler` wraps a delegate `slog.Handler` and a `Publisher`
- [x] 1.2 `Handler` implements `slog.Handler`: `Enabled`/`Handle`/`WithAttrs`/`WithGroup`; `Handle` delegates then enqueues the rendered JSON for publication
- [x] 1.3 `Publisher` drains the queue to Valkey via `XADD MAXLEN ~`
- [x] 1.4 Bounded queue with drop-on-full + atomic drop counter; periodic stderr warn goroutine at `LOG_STREAM_WARN_INTERVAL` (default 60s)
- [x] 1.5 Per-entry size cap (16 KB default) with string-field truncation + `truncated_bytes` annotation
- [x] 1.6 `LOG_STREAM_ENABLED=false` turns the Publisher into a passthrough; `NewLogger` skips construction entirely when disabled
- [x] 1.7 `NewLogger` + `OptionsFromEnv` helpers for service-side adoption
- [x] 1.8 Unit tests: happy-path both-destinations, disabled passthrough, queue overflow drops, oversize truncation, nil-rdb path

## 2. Service adoption

- [x] 2.1 `cmd/gateway/main.go` swaps to `logstream.NewLogger("gateway", ...)`
- [x] 2.2 `cmd/context-manager/main.go` swaps to `"context-manager"`
- [x] 2.3 `cmd/main-agent/main.go` swaps to `"main-agent"`
- [x] 2.4 `cmd/retro-agent/main.go` swaps to `"retro-agent"`
- [x] 2.5 `cmd/llm-broker/main.go` swaps to `"llm-broker"`
- [x] 2.6 `cmd/llm-proxy/main.go` swaps to `"llm-proxy"`
- [x] 2.7 `cmd/memory-service/main.go` swaps to `"memory-service"`
- [x] 2.8 Each swap happens AFTER `client.Ping` so Valkey-connect errors still hit the bootstrap stdout logger

## 3. Gateway log-reading endpoints

- [x] 3.1 `internal/gateway/logs.go` with stream readers using `XRevRange` for history and `XRead BLOCK` for live tail
- [x] 3.2 `GET /v1/logs/services` — `SCAN log:*` to enumerate known streams, returns sorted list
- [x] 3.3 `GET /v1/logs/stream?services=...&level=...&correlation_id=...&query=...&limit=...` — history with filters, merged by timestamp, capped at `limit` (default 200)
- [x] 3.4 `GET /v1/logs/tail?services=...` — SSE; one goroutine per stream doing `XRead BLOCK`, fanned into a single response
- [x] 3.5 Query parameter parsing: comma-separated services, level hierarchy (info shows warn+error too), correlation_id prefix, free-text `query` applied to `msg` + full raw JSON
- [x] 3.6 Client disconnect cancels the request context → tail goroutines exit cleanly
- [x] 3.7 Integration verified live against the running stack (7 services discovered, history returns real entries)

## 4. Descriptor schema extension

- [x] 4.1 Added `KindLogs` and `LogsSection` to `internal/dashboard/descriptor.go`
- [x] 4.2 `Section` union/discriminator updated for marshal + unmarshal
- [x] 4.3 `validateLogs` checks required URL fields and level enum
- [x] 4.4 Existing dashboard tests continue to pass (section kinds closed enum expanded)

## 5. Gateway built-in Logs panel descriptor

- [x] 5.1 `logsPanelDescriptor()` added to `buildBuiltinPanels` at order 85
- [x] 5.2 One logs section pointing at `/v1/logs/tail`, `/v1/logs/stream`, `/v1/logs/services`; default level info
- [x] 5.3 Live aggregation verified: Logs panel appears in `GET /v1/dashboard/panels`

## 6. Dashboard rendering

- [x] 6.1 `renderLogsSection` in `app.js`: service multiselect + level dropdown + correlation_id search + free-text query + auto-scroll toggle + connection status
- [x] 6.2 Service list populated from `services_url`, default_services constrains initial selection
- [x] 6.3 `EventSource` opens against `tail_url` with current filters as query params; history fetch deferred (incoming SSE entries populate the list immediately — matches user expectation for a "live tail")
- [x] 6.4 Per-entry row: timestamp / level / service / correlation_id prefix / msg; click to expand raw JSON
- [x] 6.5 DOM FIFO cap at 500 rows
- [x] 6.6 Filter changes close the active EventSource and open a new one with updated params
- [x] 6.7 Connection status (connecting / connected / reconnecting)
- [x] 6.8 CSS: monospace list, level-colored left column, service accent, expandable raw-JSON pane

## 7. Ops + env

- [x] 7.1 `LOG_STREAM_MAXLEN`, `LOG_STREAM_ENABLED` read via `OptionsFromEnv`; documented behavior in handler.go
- [x] 7.2 No docker-compose changes required (env vars opt-out-able per service via compose `environment:` if desired; default enabled)

## 8. Validation

- [x] 8.1 `go build ./...` green
- [x] 8.2 `go test ./...` green including new logstream tests
- [x] 8.3 `openspec validate add-logs-panel --strict` green
- [x] 8.4 Manual: all 7 services producing stream entries; `GET /v1/logs/services` returns all seven; `GET /v1/dashboard/panels` surfaces the Logs panel at order 85; history endpoint returns real entries
- [ ] 8.5 Manual: stop Valkey briefly; confirm stdout logging continues; drop-warn appears on stderr — operator-run verification
