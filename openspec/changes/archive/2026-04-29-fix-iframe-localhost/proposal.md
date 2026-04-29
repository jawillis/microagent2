## Why

Dashboard iframe sections (e.g. the Hindsight Control Plane in the Memory panel) embed a URL that is baked into the panel descriptor at service startup. The default for `MEMORY_SERVICE_CP_URL` is `http://localhost:9999`, which only works when the operator's browser runs on the same machine as the Docker host. Accessing the dashboard from any other machine on the network renders the iframe unreachable because `localhost` resolves to the browser's machine, not the Docker host.

## What Changes

- The dashboard JS will resolve iframe URLs at render time: when the URL's hostname is `localhost`, substitute `window.location.hostname` so the iframe points at the same host the operator used to reach the dashboard.
- Explicitly-configured URLs (non-localhost) are left untouched, preserving the override path for reverse proxies or split-host deployments.
- No schema changes, no new env vars, no backend changes.

## Capabilities

### New Capabilities

_(none)_

### Modified Capabilities

- `dashboard-ui`: iframe rendering will perform localhost-to-browser-hostname substitution at render time

## Impact

- `internal/gateway/web/app.js` — `renderIframeSection` function
- Affects any panel descriptor that declares an `iframe` section with a `localhost` URL
- No API changes, no breaking changes, no new dependencies
