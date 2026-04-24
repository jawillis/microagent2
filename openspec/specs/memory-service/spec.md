### Requirement: HTTP API for memory operations
The memory-service SHALL expose an HTTP API at a configurable address (`MEMORY_SERVICE_HTTP_ADDR`, default `:8083`) with the following endpoints: `POST /retain`, `POST /recall`, `POST /reflect`, `POST /forget`, `POST /hooks/hind/retain`, `POST /hooks/hind/consolidation`, and `GET /health`.

#### Scenario: Retain accepts a memory item
- **WHEN** a client POSTs a JSON body with `content`, optional `tags`, optional `metadata`, optional `context`, optional `entities`, and optional `observation_scopes` to `/retain`
- **THEN** memory-service SHALL apply provenance defaulting, call Hindsight's retain endpoint, and return HTTP 200 with the Hindsight retain response body

#### Scenario: Recall returns memory summaries
- **WHEN** a client POSTs a JSON body with `query`, optional `limit`, optional `tags`, and optional `types` (defaulting to `["observation"]`) to `/recall`
- **THEN** memory-service SHALL call Hindsight's recall endpoint and return HTTP 200 with a JSON body containing `memories` (the results array translated to a memory-service shape) and, when observation-type results are present, `source_facts`

#### Scenario: Reflect performs synthesis
- **WHEN** a client POSTs a JSON body with `query` to `/reflect`
- **THEN** memory-service SHALL call Hindsight's reflect endpoint for the configured bank and return the synthesized response

#### Scenario: Forget deletes a memory
- **WHEN** a client POSTs a JSON body with either `memory_id` or `query` (best-match) to `/forget`
- **THEN** memory-service SHALL resolve the target memory ID (by direct ID or by recall-best-match) and call Hindsight's DELETE /memories/{id}, returning HTTP 200 on success

#### Scenario: Health endpoint returns service state
- **WHEN** a client GETs `/health`
- **THEN** memory-service SHALL return HTTP 200 with a JSON body `{"status": "ok", "hindsight": "reachable", "bank": "<configured-bank-id>"}` when Hindsight is reachable; HTTP 503 with `hindsight: "unreachable"` otherwise

### Requirement: Provenance metadata defaulting on retain
The memory-service SHALL ensure every retained memory carries `metadata.provenance`. When a `/retain` request does not specify provenance, the service SHALL default it to `explicit`. Valid values SHALL be one of `explicit`, `implicit`, `inferred`, or `researched`.

#### Scenario: Missing provenance defaults to explicit
- **WHEN** a `/retain` request arrives with `metadata` that does not include a `provenance` key, or with no `metadata` at all
- **THEN** memory-service SHALL set `metadata.provenance` to `"explicit"` before forwarding to Hindsight

#### Scenario: Caller-specified provenance preserved
- **WHEN** a `/retain` request includes `metadata.provenance` with a valid value (`explicit`, `implicit`, `inferred`, or `researched`)
- **THEN** memory-service SHALL forward that value unchanged to Hindsight

#### Scenario: Invalid provenance rejected
- **WHEN** a `/retain` request includes `metadata.provenance` with a value other than the four valid options
- **THEN** memory-service SHALL return HTTP 400 with a structured error body and SHALL NOT call Hindsight

#### Scenario: Numeric metadata serialized as strings
- **WHEN** a `/retain` request includes numeric metadata fields like `confidence` or `salience`
- **THEN** memory-service SHALL serialize them as strings in the Hindsight request body (Hindsight metadata is `Map<string,string>`)

### Requirement: Startup sync of bank, missions, and directives from YAML
The memory-service SHALL load bank configuration, mission text, and directives from YAML files under `deploy/memory/` at startup and SHALL sync them to Hindsight idempotently.

#### Scenario: Bank created if absent
- **WHEN** memory-service starts and the configured bank (`MEMORY_BANK_ID`, default `microagent2`) does not exist in Hindsight
- **THEN** memory-service SHALL create the bank via Hindsight's banks endpoint, with `disposition` and initial config drawn from `deploy/memory/bank.yaml`

#### Scenario: Bank config PATCHed to match YAML
- **WHEN** memory-service starts and the configured bank exists
- **THEN** memory-service SHALL GET the current bank config, compare each field defined in `deploy/memory/bank.yaml` (including `retain_mission`, `observations_mission`, `reflect_mission`, `retain_extraction_mode`, and other overridable fields), and PATCH only the fields that differ

#### Scenario: Directives reconciled against YAML
- **WHEN** memory-service starts
- **THEN** for each directive file in `deploy/memory/directives/`, memory-service SHALL create the directive if no directive with the same name exists, update it if the content or priority differs, and leave other directives (not defined in YAML) untouched

#### Scenario: Sync failure logged and surfaced
- **WHEN** any step of the startup sync fails (Hindsight unreachable, invalid YAML, PATCH rejected)
- **THEN** memory-service SHALL log at ERROR with a structured message identifying the failing step, SHALL mark the health endpoint as unhealthy until sync succeeds, and SHALL retry on an internal timer (default 30s)

#### Scenario: YAML wins over live config
- **WHEN** a bank field has been modified via Hindsight's UI or API outside memory-service
- **THEN** the next memory-service startup sync SHALL overwrite that field with the YAML value (YAML is the authoritative source of truth)

### Requirement: Webhook registration at startup
The memory-service SHALL register webhook endpoints with Hindsight at startup for the events `retain.completed` and `consolidation.completed`.

#### Scenario: Webhooks registered on startup
- **WHEN** memory-service starts
- **THEN** it SHALL register (or update, if already registered) a webhook pointing to its own `/hooks/hind/retain` and `/hooks/hind/consolidation` URLs, with the HMAC signing secret from `HINDSIGHT_WEBHOOK_SECRET`, and event_types set appropriately

#### Scenario: Webhook URL uses externally-reachable memory-service address
- **WHEN** memory-service registers webhooks
- **THEN** the `url` field SHALL be constructed from `MEMORY_SERVICE_EXTERNAL_URL` (default: `http://memory-service:8083`) so Hindsight can reach memory-service from inside the docker-compose network

### Requirement: Webhook handlers verify HMAC and ack
The memory-service SHALL verify the HMAC-SHA256 signature on every incoming webhook request before accepting it. On successful verification, the handler SHALL log the event and return HTTP 200.

#### Scenario: Valid signature accepted
- **WHEN** a webhook request arrives with a valid HMAC-SHA256 signature computed using `HINDSIGHT_WEBHOOK_SECRET`
- **THEN** memory-service SHALL log the event at INFO with `msg: "memory_webhook_received"` and fields `{event, bank_id, operation_id}`, and return HTTP 200

#### Scenario: Invalid signature rejected
- **WHEN** a webhook request arrives with a missing or invalid signature
- **THEN** memory-service SHALL return HTTP 401, log at WARN with `msg: "memory_webhook_signature_invalid"`, and SHALL NOT process the payload

#### Scenario: Stub handler ack is sufficient
- **WHEN** a webhook passes signature verification
- **THEN** memory-service SHALL acknowledge (HTTP 200) regardless of whether downstream dispatch is implemented — downstream curiosity/proactive handlers arrive in follow-up proposals

### Requirement: Hindsight client lives in `internal/hindsight`
The memory-service SHALL consume Hindsight via a Go client package at `internal/hindsight` whose methods mirror the Hindsight REST API endpoints used by microagent2: Retain, Recall, Reflect, Delete, GetMemoryHistory, ListBanks, GetBank, PatchBankConfig, ListDirectives, CreateDirective, UpdateDirective, ListWebhooks, CreateWebhook, UpdateWebhook.

#### Scenario: Client honors context cancellation
- **WHEN** any `internal/hindsight` method is called with a context that is later cancelled
- **THEN** the in-flight HTTP request SHALL be cancelled and the method SHALL return the context error

#### Scenario: Client surfaces non-2xx responses
- **WHEN** Hindsight returns a non-2xx status
- **THEN** the client method SHALL return an error that includes the status code and response body

### Requirement: Structured logging for every memory operation
The memory-service SHALL emit structured INFO log lines for every inbound request and every Hindsight call, tagged with a correlation ID propagated from the inbound request.

#### Scenario: Retain logged
- **WHEN** memory-service handles a `/retain` request
- **THEN** it SHALL emit `msg: "memory_retain_received"` at arrival and `msg: "memory_retain_completed"` on Hindsight response, each with `{correlation_id, bank, provenance, tag_count, elapsed_ms, outcome}`

#### Scenario: Recall logged
- **WHEN** memory-service handles a `/recall` request
- **THEN** it SHALL emit `msg: "memory_recall_received"` and `msg: "memory_recall_completed"` with `{correlation_id, query_hash, limit, types, memory_count, elapsed_ms, outcome}`

#### Scenario: Hindsight errors logged with context
- **WHEN** a Hindsight call returns an error or non-2xx
- **THEN** memory-service SHALL log at ERROR with `msg: "memory_hindsight_error"` and fields `{correlation_id, endpoint, status, error}`

### Requirement: Configuration surface
The memory-service SHALL read its configuration from environment variables at startup.

#### Scenario: Required env vars
- **WHEN** memory-service starts
- **THEN** it SHALL read `HINDSIGHT_ADDR` (required), `HINDSIGHT_API_KEY` (optional), `HINDSIGHT_WEBHOOK_SECRET` (required for webhook registration), `MEMORY_BANK_ID` (default `microagent2`), `MEMORY_SERVICE_HTTP_ADDR` (default `:8083`), `MEMORY_SERVICE_EXTERNAL_URL` (default `http://memory-service:8083`), and `MEMORY_YAML_DIR` (default `/etc/microagent2/memory`)

#### Scenario: Missing required env vars fail startup
- **WHEN** memory-service starts without `HINDSIGHT_ADDR` or without `HINDSIGHT_WEBHOOK_SECRET`
- **THEN** it SHALL log a structured ERROR identifying the missing var and SHALL exit non-zero
