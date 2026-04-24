## ADDED Requirements

### Requirement: Resilient stream consumer helper
The messaging client SHALL expose a `ConsumeStream` function that runs a long-running consumer loop with explicit error classification, automatic consumer-group recovery, and phantom-consume detection, so that every long-running consumer in the system shares one resilience implementation.

#### Scenario: Handler invoked once per decoded message
- **WHEN** a caller runs `ConsumeStream` and N messages are published to the stream
- **THEN** the handler SHALL be invoked exactly N times, once per message, with each message decoded from the stream entry

#### Scenario: Successful handler acks the message
- **WHEN** the handler returns nil for a message
- **THEN** `ConsumeStream` SHALL call `XACK` for that message's id before reading the next entry

#### Scenario: Failing handler is logged loudly but still acked
- **WHEN** the handler returns a non-nil error for a message
- **THEN** `ConsumeStream` SHALL log at ERROR with the correlation_id and error, SHALL increment the `HandlerErrors` counter, and SHALL still call `XACK` for that message — the queue MUST NOT stall on a single poisonous message. `XREADGROUP >` does not redeliver PEL entries, so leaving messages unacked achieves nothing useful beyond head-of-line blocking.

### Requirement: Consumer-group recovery on NOGROUP
The `ConsumeStream` helper SHALL detect when the consumer group no longer exists (Valkey returns `NOGROUP` or "no such key") and SHALL recreate the group and continue, without requiring a service restart.

#### Scenario: NOGROUP after FLUSHDB
- **WHEN** `FLUSHDB` is executed against the Valkey instance while `ConsumeStream` is running
- **AND** a new message is subsequently published to the stream
- **THEN** `ConsumeStream` SHALL log at WARN with `msg: "consumer_group_missing_recovering"`, call `EnsureGroup` to recreate the consumer group, and SHALL process the new message via the handler

#### Scenario: Repeated recovery does not spam logs
- **WHEN** NOGROUP errors occur repeatedly within a single 10-second window
- **THEN** only the first recovery in that window SHALL be logged at WARN; subsequent ones SHALL be logged at DEBUG

### Requirement: Error classification for ReadGroup results
The `ConsumeStream` helper SHALL classify errors returned from `XReadGroup` into three buckets and act on each distinctly.

#### Scenario: redis.Nil is a normal idle
- **WHEN** `XReadGroup` returns `redis.Nil` (block elapsed with no new messages)
- **THEN** `ConsumeStream` SHALL continue the loop without logging

#### Scenario: Unknown error triggers backoff and warning
- **WHEN** `XReadGroup` returns any error other than `redis.Nil` or NOGROUP
- **THEN** `ConsumeStream` SHALL log at WARN with `msg: "consume_stream_error"` and the error text, sleep for an exponential backoff (starts 100ms, caps 5s, resets on the next successful read), and continue

#### Scenario: Consecutive unknown errors escalate to ERROR
- **WHEN** `XReadGroup` has returned the same unknown error class N times in a row (default N=10)
- **THEN** `ConsumeStream` SHALL additionally log at ERROR with `msg: "consumer_loop_degraded"` so the degraded state surfaces to operators

### Requirement: Phantom-consume detection
The `ConsumeStream` helper SHALL periodically check whether the consumer group's `entries-read` count exceeds the local count of handler invocations and SHALL emit a WARN log when a persistent delta is detected, so that go-redis / Valkey interactions that silently advance the group without delivering messages to the handler are observable.

#### Scenario: Group-ahead-of-handler logged
- **WHEN** a second consecutive `XINFO GROUPS` check shows `entries-read` greater than the local handler-invocation count
- **THEN** `ConsumeStream` SHALL log at WARN with `msg: "phantom_consume_detected"` and fields `{stream, group, entries_read, handled_count, delta}`

#### Scenario: Transient delta does not fire warning
- **WHEN** a single check shows a delta but the next check shows handler has caught up
- **THEN** no `phantom_consume_detected` log SHALL fire

#### Scenario: Configurable check cadence
- **WHEN** the `CONSUME_PHANTOM_CHECK_S` environment variable is set
- **THEN** `ConsumeStream` SHALL use that value (in seconds) as the phantom-check interval, defaulting to 30 when unset

### Requirement: Observable consume stats
The `ConsumeStream` helper SHALL accept an optional stats pointer that tracks handler invocations, handler errors, recoveries, and phantom-consume events, and SHALL emit a periodic INFO snapshot for operators.

#### Scenario: Stats snapshot emitted periodically
- **WHEN** the phantom-check interval fires
- **THEN** `ConsumeStream` SHALL log at INFO with `msg: "consume_stream_stats"` and fields `{stream, group, handled, handler_errors, recoveries, phantom_logs}`
