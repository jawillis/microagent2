## ADDED Requirements

### Requirement: Services publish logs to per-service Valkey streams
Each microagent2 service SHALL publish its structured log output to a Valkey stream named `log:<service_id>` in addition to continuing to write structured JSON to stdout.

#### Scenario: Log line appears on both destinations
- **WHEN** a service emits a structured log line at any level
- **THEN** the line SHALL be written to stdout as JSON (unchanged from current behavior) AND asynchronously published to `log:<service_id>`

#### Scenario: Valkey unavailable does not block stdout
- **WHEN** Valkey is unreachable or slow
- **THEN** stdout logging SHALL continue at full speed; the service SHALL NOT block on the stream write

#### Scenario: Log publish is opt-out via env
- **WHEN** a service is started with `LOG_STREAM_ENABLED=false`
- **THEN** it SHALL skip stream publishes entirely and continue writing only to stdout

### Requirement: Bounded stream retention
Stream writes SHALL use `XADD MAXLEN ~ <cap>` to cap entries per stream. The cap SHALL be configurable per service via `LOG_STREAM_MAXLEN` (default 10_000).

#### Scenario: Cap from environment
- **WHEN** a service starts
- **THEN** it SHALL read `LOG_STREAM_MAXLEN` (default 10_000) and use that value as the `MAXLEN ~` argument on every `XADD`

#### Scenario: Old entries aged out
- **WHEN** the stream exceeds its cap
- **THEN** Valkey SHALL trim older entries automatically (approximate trim); newer entries SHALL replace them

### Requirement: Bounded publish queue with drop + warn
The stream-publish path SHALL use a bounded in-memory queue. When the queue is full, new log lines SHALL be dropped; the service SHALL increment a drop counter and emit a periodic stderr warning summarizing drops.

#### Scenario: Queue overflow drops entries
- **WHEN** the publish queue is full and a new log line arrives
- **THEN** the line SHALL be dropped (still emitted to stdout), the drop counter incremented

#### Scenario: Periodic drop warning
- **WHEN** drops have occurred since the last warning
- **THEN** every N seconds (default 60) the service SHALL emit a single stderr line `log_stream_drops count=<n>` and reset the counter

#### Scenario: Default queue size
- **WHEN** a service starts without overriding queue size
- **THEN** the queue SHALL hold 1000 pending entries

### Requirement: Entry size cap with truncation
The handler wrapper SHALL enforce a per-entry size cap of 16 KB. Entries larger than the cap SHALL be truncated at the JSON boundary and include a `truncated_bytes: <n>` field.

#### Scenario: Small entry passes through
- **WHEN** a log entry is under 16 KB
- **THEN** it SHALL be written to the stream unchanged

#### Scenario: Oversize entry truncated
- **WHEN** a log entry exceeds 16 KB
- **THEN** the value portions of string fields SHALL be truncated to fit, and a `truncated_bytes` numeric field SHALL be added indicating the bytes dropped

### Requirement: Stream entry format
Stream entries SHALL use a Valkey stream field named `json` whose value is the single-line JSON representation of the log entry, identical to the stdout JSON.

#### Scenario: Entry shape
- **WHEN** a log entry is published
- **THEN** the `XADD` call SHALL use `json=<JSON_LINE>` as its sole field/value pair
