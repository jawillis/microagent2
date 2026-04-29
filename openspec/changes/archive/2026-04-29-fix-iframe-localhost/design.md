## Context

Dashboard panel descriptors are built at service startup and baked into the registry. The Hindsight Control Plane iframe URL defaults to `http://localhost:9999` via `MEMORY_SERVICE_CP_URL`. This works when the browser runs on the Docker host, but breaks when the operator accesses the dashboard from another machine — `localhost` resolves to the browser's machine, not the Docker host.

The iframe URL is inherently a browser-side concern: the correct hostname depends on what the operator typed into their address bar, which is unknowable at service startup.

## Goals / Non-Goals

**Goals:**
- Iframe sections with `localhost` URLs automatically resolve to the correct host when accessed from any machine on the network
- Explicitly-configured URLs (non-localhost) are never modified
- Zero configuration burden for the common Docker Compose case

**Non-Goals:**
- Changing the panel descriptor schema or backend code
- Handling reverse proxy path rewriting (operators set `MEMORY_SERVICE_CP_URL` explicitly for that)
- Supporting iframe URLs on entirely different hosts (that's what the env var override is for)

## Decisions

### Decision 1: Browser-side hostname substitution in `renderIframeSection`

**Choice:** When `renderIframeSection` encounters a URL whose hostname is `localhost` or `127.0.0.1`, it replaces that hostname with `window.location.hostname` before setting `iframe.src`. The port and path are preserved.

**Rationale:** The correct hostname is only knowable in the browser. This is a ~3-line change in one function, requires no schema changes, and automatically works for any access pattern (LAN IP, hostname, etc.).

**Alternatives considered:**
- *Template syntax in descriptor URL* (`http://${HOST}:9999`) — requires schema awareness, parser in JS, and documentation. Over-engineered for this.
- *Gateway proxies Hindsight CP* — robust but adds operational complexity (proxy config, CORS, path rewriting) for a problem solvable in 3 lines of JS.
- *Config-only fix* (set `MEMORY_SERVICE_CP_URL` in `.env`) — works but pushes the burden to every operator and breaks if access patterns change.

### Decision 2: Use URL API for safe hostname replacement

**Choice:** Parse the iframe URL with `new URL(...)`, check `.hostname`, replace it, and use `.toString()` for the result. No regex string manipulation.

**Rationale:** Handles edge cases (ports, paths, query params) correctly without fragile string ops.

## Risks / Trade-offs

- **[Assumption] All published ports are on the same host as the gateway** — True for any single-host Docker Compose deployment, which is the only supported topology. If services are ever split across hosts, the operator must set `MEMORY_SERVICE_CP_URL` explicitly — but that's already the case today. → No mitigation needed.
- **[Edge case] Operator sets `MEMORY_SERVICE_CP_URL=http://localhost:9999` explicitly** — Substitution still fires, which is the desired behavior (they'd only set localhost explicitly if they're on the same machine, in which case substitution is a no-op). → Acceptable.
