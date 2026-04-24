## Why

Debugging microagent2 today means running `docker compose logs` across multiple services and grepping by correlation ID. That's fine for an operator at a terminal but hostile for anyone else. The dashboard should surface structured logs with live filtering, especially by correlation ID so an entire turn's journey across gateway → context-manager → main-agent → broker → llama-server is visible in one pane.

Every service already logs structured JSON via `slog.NewJSONHandler` and Valkey is in our stack. The cheapest path to a usable logs viewer is having services also publish their log lines to a per-service Valkey stream; the dashboard subscribes and displays. No external log aggregator, no docker-socket access, no new infrastructure.

## What Changes

- New `internal/logstream` helper package exposing a `slog.Handler` that fans out log lines to stdout (as today) AND to a Valkey stream `log:<service_id>`. Services adopt it by wrapping their existing `slog.JSONHandler` — one-line change per service main.
- Each service's log events land on `log:<service_id>` (e.g. `log:gateway`, `log:main-agent`, `log:memory-service`, `log:llm-broker`, `log:llm-proxy`, `log:retro-agent`, `log:context-manager`). Stream entries are the raw JSON log body.
- Streams are bounded: each write uses `XADD MAXLEN ~ <cap>` to cap the stream at a configurable number of entries (default 10_000). Retention is size-based, not time-based. Operators can change `LOG_STREAM_MAXLEN` per service.
- New gateway endpoint `GET /v1/logs/stream?services=a,b,c&since=<id>&limit=<n>&correlation_id=<id>&level=<level>` that reads from the specified streams, applies filters, and returns JSON-lines. `since` defaults to `$` (tail); omitted `services` means all known streams.
- New gateway endpoint `GET /v1/logs/tail?services=a,b,c&...` as a Server-Sent Events stream (SSE) delivering new log entries as they arrive. Browser consumes and appends to the dashboard.
- New built-in dashboard panel `Logs` contributed by the gateway (via the panel descriptor registry), ordered `order: 85` (after System, near the end). Panel has:
  - Service multi-select (defaults to all)
  - Level filter (info/warn/error)
  - Correlation ID search box (exact match or prefix)
  - Free-text search within `msg` and other string fields
  - Auto-scroll toggle
  - Log lines displayed in a virtualized list (or capped at N) with timestamp, level, service, correlation_id prefix, msg, and expandable JSON detail
- gateway.api.logs is the new capability; builds on `dashboard-panel-registry`.
- A `GET /v1/logs/services` endpoint returns the list of active log streams (derived from the registered agents list plus gateway itself).

## Capabilities

### New Capabilities

- `log-streaming`: the contract for services publishing structured logs to `log:<service_id>` Valkey streams with bounded retention, and the gateway HTTP endpoints that consume those streams for the dashboard's Logs panel.

### Modified Capabilities

- `dashboard-ui`: the dashboard gains a Logs panel (gateway-built-in descriptor).

## Impact

- **All services** get a one-line init change: wrap the existing slog handler. Stream publish is fire-and-forget; a Valkey outage does NOT block stdout logging (that's still emitted).
- **`internal/logstream`** new package containing the `slog.Handler` wrapper, `Client` for stream reads (used by the gateway), and the SSE session utilities.
- **Gateway routes**: `GET /v1/logs/services`, `GET /v1/logs/stream`, `GET /v1/logs/tail` (SSE). Responses documented.
- **Gateway panel descriptor**: built-in Logs panel added to the gateway's synthesized descriptor list with a new section kind `logs` in the descriptor schema (since forms/iframes/status don't fit a live log viewer).
- **New section kind** `logs` in `dashboard-panel-registry` schema: descriptor declares the SSE URL, list of services, and default filters. This is a schema addition to the capability from `add-dashboard-panel-registry`.
- **docker-compose**: no changes required.
- **Env vars** per service: `LOG_STREAM_MAXLEN` (default 10_000), `LOG_STREAM_ENABLED` (default true; lets operators disable the stream publish without code changes).
- **Performance**: stream writes are async (fire via goroutine per log line); back-pressure bounded by the publish queue length. If Valkey is slow, log lines queue briefly then start dropping with a stderr warning (drop count reported periodically).
- **Security**: the logs endpoints are unauthenticated, same as the rest of the dashboard API — admin-only surface. Raw log content MAY include correlation IDs, session IDs, and user prompts. Operators should treat the dashboard as privileged.
- **Test surface**: unit tests for the handler wrapper (output goes to both destinations, outages don't block), stream reader + filter tests, SSE endpoint integration test, panel descriptor validation for the new `logs` section kind.
