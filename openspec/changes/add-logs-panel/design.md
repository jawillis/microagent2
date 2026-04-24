## Context

Every microagent2 service logs structured JSON via `slog.NewJSONHandler(os.Stdout, nil)`. Docker captures stdout and operators use `docker compose logs <svc>` to inspect. This works but has sharp edges:

- Cross-service correlation requires grepping by correlation_id across many services' logs simultaneously
- No in-dashboard view; operators leave the UI to debug
- `docker` access is required, which isn't available to everyone who has dashboard access

Valkey is in our stack and streams are native. Services are structured-JSON already. The cheapest viable path to a logs UI is fanning every log line to both stdout and a per-service Valkey stream, then reading from streams in the dashboard.

Two collection approaches were considered:

```
 A: slog.Handler wrapper           B: external log shipper (Vector, Fluent Bit)
    (per-service, opt-in)             (sidecar or sink, collects from docker)
    ──────────────────────            ───────────────────────────────────────
    one-line change per service       no service changes
    no extra containers               one sidecar per service, or one central
    direct JSON → Valkey              parses docker log format
    correlation_id preserved          time-windowed, more features, more ops
    our logging pipeline is ours      industry-standard but heavier
```

A is right for where we are. One handler, no new infra, structured preservation of our correlation fields.

## Goals / Non-Goals

**Goals:**

- Dashboard has a Logs panel that shows recent entries from any combination of services
- Filtering by correlation_id works end-to-end — a turn's full journey visible in one view
- Live tail via SSE so new log lines appear as they occur
- Services fail safe: Valkey outage does not lose stdout logs; stdout never blocks on stream writes
- Bounded storage — streams are capped; old entries age out automatically

**Non-Goals:**

- Full log aggregation platform (no historical storage beyond the cap, no cold-tier archiving, no structured querying beyond basic filters)
- Log shipping outside microagent2 (external SIEM, Loki, etc.)
- Authenticated access to logs (same admin-surface as the rest of the dashboard)
- Exact-once delivery guarantees (drops are acceptable during Valkey overload; counted)
- Cross-service log aggregation into a single unified stream (services remain distinct; the gateway merges at read time)

## Decisions

### Decision 1: Per-service stream, merged at read

**Choice:** One Valkey stream per service: `log:<service_id>`. Merging across services happens at the read endpoint by scanning each stream and interleaving entries by timestamp.

**Rationale:** Isolation — a misbehaving service can't flood others' logs or knock out the shared stream. Retention caps apply per-service, making sizing predictable. Read-side merging is cheap for the filter patterns we expect (most queries are "one service, recent N" or "all services, correlation_id X").

**Alternatives considered:**
- *Single shared stream* — one cap, one consumer group, no per-service sizing; but cross-service fair-share is hard; evicting one chatty service evicts others
- *Stream per level* — over-engineered; level filter is trivial at read time

### Decision 2: slog.Handler wrapper, not a separate logger

**Choice:** Wrap the existing `slog.JSONHandler` with a `logstream.Handler` that delegates to the stdout handler for rendering AND enqueues the JSON line for publication to the service's Valkey stream. Every service's `slog.New(...)` call is updated to construct the wrapper; zero call-site changes.

**Rationale:** Our services are already structured-logging-native. Adding a second logger would require every `logger.Info(...)` call site to decide which sink to target. The handler wrapper model (delegate) is standard and composable.

**Alternatives considered:**
- *Separate "audit" logger alongside the existing one* — doubled call sites, two logging idioms, perpetual "should this go to both?" decision
- *Tee on stdout externally* — requires log-shipper infrastructure we don't have

### Decision 3: Async publish with bounded queue + drop

**Choice:** The wrapper handler buffers log lines in a small channel (default size 1000) that a single goroutine drains into Valkey via `XADD`. If the channel is full (Valkey overloaded), new entries are dropped and a drop counter is incremented; every N seconds the handler emits a single synchronous stderr warning summarizing drops since the last warning.

**Rationale:** stdout logging MUST not block on Valkey. If Valkey is overloaded or unavailable, the service continues to log normally and sheds stream writes. The drop-counter + periodic warning pattern ensures the operator notices without flooding stderr.

**Alternatives considered:**
- *Blocking publish with backpressure on stdout* — unacceptable; turns a logging outage into a service outage
- *Unbounded queue* — same bad failure mode (OOM instead)
- *Retry on failure* — compounds the backpressure problem

### Decision 4: Streams are capped by entry count, not time

**Choice:** `XADD` uses `MAXLEN ~ <cap>` (approximate trim). Default cap 10_000 entries per service. Configurable via `LOG_STREAM_MAXLEN`.

**Rationale:** Predictable storage sizing independent of traffic patterns. With ~1 KB per entry average, 10_000 entries × 7 services ≈ 70 MB worst case — trivial for Valkey. Operators who want more retention bump the cap; those who want less (memory-constrained deployments) lower it.

**Alternatives considered:**
- *Time-based retention (`MINID` trim by timestamp)* — harder to cap storage; requires a periodic trim job
- *No cap* — unbounded growth

### Decision 5: Gateway aggregates at read time, no dedicated collector

**Choice:** The gateway opens `XRANGE` reads against each requested service's stream on demand. For live tail (`GET /v1/logs/tail`), it opens `XREAD BLOCK` per stream concurrently and fans events into the SSE response. No separate "log collector" service.

**Rationale:** Reads are infrequent (operator has the panel open). Concurrent `XREAD BLOCK` across 7 streams is trivial. A dedicated collector would be more code to run, more failure modes, and no benefit at this scale.

**Alternatives considered:**
- *Dedicated log collector that writes merged entries to a single consumer stream* — doubles storage; adds a failure domain
- *Dashboard subscribes to Valkey directly via a WebSocket to the Valkey Pub/Sub* — requires Valkey to be reachable from the browser; major auth/security concern

### Decision 6: SSE for live tail, not WebSocket

**Choice:** `GET /v1/logs/tail` is a Server-Sent Events stream; the client connects with `EventSource`. WebSocket is not used.

**Rationale:** Logs are one-way (server → client). SSE is simpler, works through reverse proxies without upgrade negotiation, handles reconnect natively, and fits the data flow perfectly. WebSocket would add bidirectional machinery we don't need.

**Alternatives considered:**
- *WebSocket* — full-duplex not needed; more complex
- *Long-polling* — works but worse UX (gaps between polls)

### Decision 7: New `logs` section kind in the descriptor schema

**Choice:** Extend `dashboard-panel-registry`'s closed section-kind enum with a new `logs` kind. The Logs panel's descriptor declares: `kind: "logs"`, `tail_url` (string, path to SSE endpoint), `history_url` (string, path to history endpoint), `services_url` (string, path to services list endpoint), `default_services` (array of service IDs, optional), `default_level` (enum, default `info`).

**Rationale:** Forms/iframes/status weren't enough. A live log viewer is a distinct UI idiom that warrants its own first-class kind. Declaring the URLs in the descriptor keeps the dashboard shell generic — it doesn't hardcode `/v1/logs/...`.

**Alternatives considered:**
- *Implement as a `status` section polling periodically* — poor UX for live data
- *Embed as an iframe from a separate logs UI server* — adds a service

## Risks / Trade-offs

- **Risk:** Valkey stream writes under sustained high log volume cause the drop path to engage repeatedly. → **Mitigation:** drop counter + periodic warning. Operators reduce log verbosity or scale Valkey.
- **Risk:** A service writes extremely large log entries (stack dumps); stream storage balloons despite MAXLEN on entry count. → **Mitigation:** cap entry size at 16 KB in the handler wrapper; oversize entries get truncated with a `truncated_bytes` field added. Prevents pathological stream size.
- **Risk:** SSE connection over the docker-compose network + mitmweb reverse-proxy chain misbehaves. → **Mitigation:** verify during implementation; SSE is well-supported but reverse proxies sometimes buffer. If mitmweb buffers, document the fallback (connect directly to gateway port).
- **Risk:** Logs panel with live tail on an idle dashboard tab accumulates unbounded DOM nodes. → **Mitigation:** cap the in-DOM log line count (default 500) with FIFO eviction; older entries drop out of view (still queryable via history endpoint).
- **Trade-off:** Stream writes add ~100 µs per log line (network round-trip to Valkey). Negligible for normal operation.
- **Trade-off:** The `logs` section kind is a schema extension to a capability we just introduced in `add-dashboard-panel-registry`. Adds a second section kind this change has to thread through both specs. Cleaner than implementing logs in the dashboard shell without going through the descriptor system.

## Migration Plan

1. Land `internal/logstream` package with the handler wrapper + stream-read helpers.
2. Wire each service's `main.go` to wrap its slog handler. Low-risk; stdout logging continues unchanged even if the Valkey write fails.
3. Land gateway's log-reading endpoints (`/v1/logs/services`, `/v1/logs/stream`, `/v1/logs/tail`).
4. Extend `dashboard-panel-registry`'s section-kind schema with `logs` (backward-compatible — old descriptors without this kind are unaffected).
5. Gateway registers its built-in Logs panel descriptor with the new section kind.
6. Dashboard shell gains a renderer for the `logs` section kind.
7. Smoke: open dashboard → Logs tab → see live lines from all services → filter by correlation_id of a just-made turn → see the end-to-end flow.

**Rollback:** each service's handler wrap can be toggled via `LOG_STREAM_ENABLED=false` without code change. Hard rollback is a revert; stdout logging never stopped working.

## Open Questions

- Should the Logs panel support exporting filtered results to a file? Operators may want to share a filtered log excerpt. Out of scope for first landing; easy follow-up.
- Do we want structural parsing of common log keys (correlation_id, session_id, slot_id) so they render as distinct columns rather than inline JSON? Yes, but as a UX polish step during implementation.
- Mitmweb is currently the external reverse proxy on port 8082 for the gateway. If SSE breaks through it, do we document operator workarounds or wire SSE through a direct gateway port? Needs implementation spike.
