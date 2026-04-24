## 1. `internal/logstream` package

- [ ] 1.1 Create `internal/logstream/handler.go` with a `Handler` type wrapping a delegate `slog.Handler` and a Valkey client
- [ ] 1.2 Implement `slog.Handler` interface: `Enabled`, `Handle`, `WithAttrs`, `WithGroup`; `Handle` delegates to wrapped handler AND enqueues the rendered JSON into a bounded channel
- [ ] 1.3 Goroutine drains the channel to Valkey via `XADD` with `MAXLEN ~ <cap>`
- [ ] 1.4 Bounded queue (buffered channel) with drop-on-full + drop counter; periodic stderr warning goroutine
- [ ] 1.5 Per-entry size cap (16 KB default) with truncation + `truncated_bytes` field
- [ ] 1.6 `LOG_STREAM_ENABLED=false` disables the fan-out path entirely (wrapper becomes a passthrough)
- [ ] 1.7 `logstream.NewLogger(serviceID, delegate)` helper constructs the wrapped handler + returns a `*slog.Logger`
- [ ] 1.8 Unit tests: both-destinations happy path, Valkey-unreachable path, queue-full drop + warn, oversize truncation, disabled mode passthrough

## 2. Service adoption

- [ ] 2.1 `cmd/gateway/main.go` swap: `slog.New(slog.NewJSONHandler(...))` → `logstream.NewLogger("gateway", ...)`
- [ ] 2.2 `cmd/context-manager/main.go` swap with service_id `context-manager`
- [ ] 2.3 `cmd/main-agent/main.go` swap with service_id `main-agent`
- [ ] 2.4 `cmd/retro-agent/main.go` swap with service_id `retro-agent`
- [ ] 2.5 `cmd/llm-broker/main.go` swap with service_id `llm-broker`
- [ ] 2.6 `cmd/llm-proxy/main.go` swap with service_id `llm-proxy`
- [ ] 2.7 `cmd/memory-service/main.go` swap with service_id `memory-service`
- [ ] 2.8 All services pass their existing Valkey client reference to the wrapper

## 3. Gateway log-reading endpoints

- [ ] 3.1 New `internal/gateway/logs.go` with reader helpers that use `XRANGE` for history and `XREAD BLOCK` for live tail
- [ ] 3.2 `GET /v1/logs/services` returns `{"services": [...]}` — merges the registered-agents list with gateway itself to produce the list of live stream IDs
- [ ] 3.3 `GET /v1/logs/stream?services=a,b&since=<id>&limit=<n>&correlation_id=<id>&level=<level>` returns a JSON array of recent entries matching filters, merged and sorted by timestamp
- [ ] 3.4 `GET /v1/logs/tail?services=a,b&correlation_id=<id>&level=<level>` opens an SSE stream; on each new entry matching filters, writes `data: <json>\n\n`
- [ ] 3.5 Query parameter parsing: comma-separated services, correlation_id prefix/exact, level hierarchy (info shows warn+error too)
- [ ] 3.6 Graceful request cancellation on context cancel (client disconnect)
- [ ] 3.7 Integration test with fake Valkey streams: post N entries across 2 streams, fetch with filter, validate ordering + filtering

## 4. Descriptor schema extension

- [ ] 4.1 In `internal/dashboard`, add `LogsSection` struct with `Title`, `TailURL`, `HistoryURL`, `ServicesURL`, `DefaultServices`, `DefaultLevel`
- [ ] 4.2 Extend section discriminator unmarshal to recognize `kind: "logs"`
- [ ] 4.3 Extend `ValidateDescriptor` to require the fields for the logs kind
- [ ] 4.4 Unit tests for the new validation branch

## 5. Gateway built-in Logs panel descriptor

- [ ] 5.1 Append a Logs panel descriptor to the gateway's built-in descriptor list with `order: 80`, one logs section with URLs pointing at `/v1/logs/tail`, `/v1/logs/stream`, `/v1/logs/services`, default level `info`
- [ ] 5.2 Verify it appears in `GET /v1/dashboard/panels` after a fresh gateway start

## 6. Dashboard rendering

- [ ] 6.1 In `app.js`, add a renderer for `kind: "logs"` that emits the filter row + scrollable list
- [ ] 6.2 Filter row: service multi-select populated from `services_url` fetch; level dropdown; correlation_id input; free-text input; auto-scroll checkbox
- [ ] 6.3 On mount: optionally fetch `history_url` to pre-populate; then open `EventSource(tail_url + "?" + currentFilters)`
- [ ] 6.4 On entry received: if matches current filters, prepend/append to list; if DOM count > 500, FIFO evict
- [ ] 6.5 Each rendered entry: compact row with timestamp, level badge, service, correlation_id prefix, msg; click to expand full JSON
- [ ] 6.6 Filter changes: close existing EventSource, open a new one with updated query params
- [ ] 6.7 Connection status indicator (connected / reconnecting / disconnected)

## 7. Ops + env

- [ ] 7.1 Document `LOG_STREAM_MAXLEN`, `LOG_STREAM_ENABLED` in `.env.example`
- [ ] 7.2 No docker-compose changes required

## 8. Validation

- [ ] 8.1 `go build ./...` green
- [ ] 8.2 `go test ./...` green
- [ ] 8.3 `openspec validate add-logs-panel --strict` green
- [ ] 8.4 Manual: bring up docker compose; open dashboard → Logs tab; see lines from all services; filter by a correlation_id from a just-made turn; verify you can trace the turn across gateway → context-manager → main-agent → broker → llm-proxy
- [ ] 8.5 Manual: stop Valkey briefly; confirm stdout logging continues; confirm periodic `log_stream_drops` warning appears on stderr; restart Valkey; confirm new lines land on streams and dashboard resumes live tail
