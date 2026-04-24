## Context

Every long-running consumer in the system uses the same read-loop shape, which looks roughly like:

```go
if err := client.EnsureGroup(ctx, stream, group); err != nil {
    return err // startup-only; never called again
}
for {
    select { case <-ctx.Done(): return ctx.Err(); default: }
    msgs, ids, err := client.ReadGroup(ctx, stream, group, consumer, 10, 2*time.Second)
    if err != nil {
        continue // swallows every error class — redis.Nil, NOGROUP, timeouts, wire errors
    }
    for i, msg := range msgs {
        handleRequest(ctx, msg)
        _ = client.Ack(ctx, stream, group, ids[i])
    }
}
```

Live observation during `fix-slot-lifecycle` verification:

- Turn 1 executes cleanly (gateway → context-manager → main-agent → broker → llama-server → tokens home).
- Turn 2 with `previous_response_id` arrives ~80 ms later. Gateway logs `gateway_request_published`.
- `XINFO` on `stream:gateway:requests` and `cg:context-manager` shows `entries-read=2, pending=0, last-delivered-id=<turn2 id>`.
- Context-manager logs `context_handle_request_start` exactly once. Never fires for turn 2.
- This reproduces on pre-existing `TestEndToEnd` against base code — so the bug predates the slot-lifecycle change and is orthogonal to it.

The contradiction is that Valkey says the message was delivered and ack'd, but the service never called `XACK` (our code path doesn't reach that line) and never ran `handleRequest`. Three non-exclusive explanations:

1. **go-redis / Valkey interaction**: `XReadGroup` with `Block` may be advancing the consumer group's `last-delivered-id` while returning `redis.Nil`. We have not confirmed this. Valkey 8 + go-redis v9.
2. **Silent error swallow**: After `FLUSHDB` (which wipes the group), `XReadGroup` returns a `NOGROUP` error. Our loop treats it identically to `redis.Nil` and retries forever. `EnsureGroup` was only called once at startup, so the group is never recreated. This one we *have* confirmed reproduces when `FLUSHDB` is run against a live service.
3. **Race with a leftover consumer goroutine from a prior test/restart**: `docker compose restart` without clearing in-memory client state has sometimes left behind a goroutine in an earlier broker. This is harder to prove but is suspicious given the observed idle-consumer state.

This change addresses (2) and (3) directly, surfaces (1) as a loud WARN (phantom-consume detection), and leaves a well-scoped investigation artifact for (1) if the symptom persists after we ship.

Primary stakeholders: context-manager, main-agent, retro-agent, broker — essentially every Go service in the repo except the gateway HTTP handler.

## Goals / Non-Goals

**Goals:**
- After `FLUSHDB` or a Valkey restart that wipes consumer groups, every long-running consumer loop recovers without requiring a service bounce.
- Any error other than `redis.Nil` is logged at WARN the first time it occurs (and at DEBUG for subsequent consecutive occurrences to avoid log flood).
- If the group's `entries-read` advances past what the service has locally handled (phantom consume), that delta appears in logs within one snapshot interval.
- A regression test reliably reproduces the "publish N → fewer than N handleRequest calls" pattern on broken code and passes on fixed code.

**Non-Goals:**
- Patching go-redis or Valkey. If the root cause of the phantom-consume is inside either library, capture it in Open Questions and open a separate investigation.
- Replacing the stream-based messaging architecture.
- Changing the slot-lifecycle protocol introduced in `fix-slot-lifecycle`.
- Solving every class of Valkey outage. If Valkey is unreachable entirely, the services are allowed to be unusable — that's a deployment concern, not a library-resilience concern.

## Decisions

### 1. New resilient helper: `Client.ConsumeStream`

**Decision:** Add a new function in `internal/messaging/client.go` with this signature:

```go
// ConsumeStream runs a resilient read loop for the given stream + consumer group.
// The handler is invoked for each decoded message; the loop handles error classes,
// consumer-group recovery, and acks returns. Returns only on ctx.Done() or
// unrecoverable error.
func (c *Client) ConsumeStream(
    ctx context.Context,
    stream, group, consumer string,
    count int64,
    block time.Duration,
    handler func(ctx context.Context, msg *Message) error,
) error
```

The handler returns `error`. On success, the message is ack'd. On error, the message is *not* ack'd (enters PEL) and an ERROR log fires with correlation_id and error. Callers wrap existing `handleRequest` functions in a closure that converts their `void` return into `error`.

Inside, the loop:
- Calls `EnsureGroup` once at start.
- Each iteration calls `XReadGroup` with block. On `redis.Nil`, continue silently.
- On `NOGROUP` / "no such key": log WARN `"consumer_group_missing_recovering"`, call `EnsureGroup`, continue.
- On any other error: log WARN `"consume_stream_error"` with error string, sleep `backoff` (starts 100ms, caps 5s, resets on success), continue.
- Track a per-loop `handledCount`. Every N seconds (default 30), fetch `XINFO GROUPS` and compare `entries-read` to `handledCount`; if delta > 0, log WARN `"phantom_consume_detected"` with delta. This is the diagnostic surface for root cause (1) above.

**Rationale:** Centralizes the resilience contract in one place so all four consumer call sites get the same behavior and the test coverage applies once. The helper signature is close enough to the existing loop shape that migration is mechanical.

**Alternatives considered:**
- *Each service keeps its own loop, we just document the pattern*: tried once, doesn't stick. Evidence: all four call sites drifted to the same broken pattern.
- *Wrap it as a struct with methods (Consumer type)*: more Go-idiomatic, but overkill given the loop is stateless and each call site calls it exactly once at startup.

### 2. Error classification

**Decision:** Three buckets.

- `redis.Nil` (or the wrapped equivalent from go-redis) — **normal**: block timed out with no messages. Silent, no log.
- `NOGROUP` / `ERR no such key` — **recoverable**: the group is missing. Log WARN, call `EnsureGroup`, continue.
- All others — **unknown**: log WARN, sleep with exponential backoff, continue. If the same error class repeats N times (default 10), escalate to ERROR with a "consumer loop degraded" message. This is the class that caught us flat-footed — we never want to silently loop on an error we don't recognize.

**Rationale:** The historic pattern was "treat all errors the same because we don't know which are recoverable." That produces silent hangs. Explicit classification makes the trade-off visible.

**Alternatives considered:**
- *Panic on unknown errors*: too blast-radius-heavy. A transient connection blip shouldn't take down the service.
- *Break the loop on unknown errors and let the orchestrator restart the service*: considered, but our current deployment (docker-compose) has no automatic restart policy; and the regular Go defer of `cancel()` may leave the process running but unusable. Logging + continuing is safer.

### 3. Phantom-consume detection

**Decision:** Add a small ticker inside `ConsumeStream`. Every `phantomCheckInterval` (default 30s), it calls `XINFO GROUPS` on the consumer group and compares the reported `entries-read` (how many messages Valkey has delivered to this group) against a local atomic counter of how many messages the handler has been invoked for. If the delta is positive (group ahead of handler), log WARN `"phantom_consume_detected"` with delta, stream, group, entries_read, handled_count.

**Rationale:** This is the *only* way to detect the class of failure where go-redis returns success-with-no-messages but the group's state advances. We have not yet reproduced it in isolation, but if it is real, the diagnostic is what will let us prove it. If it is not real, the diagnostic costs us almost nothing (one XINFO per 30s per consumer).

**Alternatives considered:**
- *Skip the check and trust the loop*: defeats the purpose. The whole point is to surface the failure mode we can't currently see.
- *Put the check in a separate goroutine*: same effect, more plumbing. The ticker inside `ConsumeStream` owns the lifetime cleanly via the same `ctx`.

### 4. Counter + observability contract

**Decision:** `ConsumeStream` accepts a `*ConsumeStats` pointer (optional; nil is fine) that callers can introspect via a method. For now the fields are:

```go
type ConsumeStats struct {
    // atomic fields
    handled       atomic.Uint64
    handlerErrors atomic.Uint64
    recoveries    atomic.Uint64
    phantomLogs   atomic.Uint64
}
```

Stats are also logged every `phantomCheckInterval` at INFO: `consume_stream_stats`. Consumers that already log their own per-turn events (context-manager, main-agent) keep those — this is a belt-and-suspenders count.

**Rationale:** Gives us a handle for future testing and future HTTP `/v1/debug/streams` exposure without committing to that now. Keeps the new code self-auditing.

### 5. Regression test

**Decision:** Add `internal/messaging/consume_stream_test.go` that:

1. Spins up a `miniredis`-backed or real-Valkey `Client`.
2. Starts `ConsumeStream` with a handler that records every received correlation_id into a sync.Map.
3. Publishes N messages with unique correlation_ids.
4. Waits up to 2s for `handled == N`.
5. Asserts `XPENDING` is 0 and all correlation_ids appear exactly once.
6. Second test: after the `ConsumeStream` is running, call `FLUSHDB` (or `XGROUP DESTROY`), then publish N more messages. Assert recovery: all N messages still handled after the transient disruption.

**Rationale:** Directly tests the contract the helper is supposed to provide. If the underlying go-redis / Valkey bug turns out to be real, test 1 should pin it: we'd see `handled < N` despite `entries-read == N`. If we can never reproduce it, test 2 at least confirms NOGROUP recovery, which is the confirmed case.

**Alternatives considered:**
- *miniredis only*: miniredis's Streams support is limited and may not reproduce Valkey-specific semantics. Use real Valkey if available via a `testcontainers` flag or the existing `localhost:6379` test harness.

## Risks / Trade-offs

- [Handler returns error but message body is poison] → Infinite retry on the same message. *Mitigation:* after 3 consecutive handler errors on the same stream-id, log ERROR `"handler_poison_message"` and ack the message anyway, then continue. Poison-pill behavior should be loud but should not stall the queue.
- [Phantom-consume detection produces false positives when delta is transient] → Log spam. *Mitigation:* only log when delta persists across two consecutive checks, or when delta grows. First check is a no-op baseline.
- [Backoff masks a real incident] → A Valkey outage that the loop quietly waits through while the rest of the system is hung. *Mitigation:* the "consumer loop degraded" ERROR escalation after N consecutive unknown errors. That's the loud signal operators need.
- [XINFO GROUPS adds load to Valkey] → Negligible at 30s cadence with our current fan-out. Monitor if we scale to dozens of consumers.
- [ConsumeStream signature change pushes work on every caller] → Mechanical refactor. Four call sites. All are touched by this change; no external consumers.

## Migration Plan

1. Merge this change.
2. Build new images for all Go services. No Valkey-side migration.
3. `docker compose up -d --build`. Because the new loop self-heals, no special-order restart.
4. Verify: after startup, send a two-turn conversation via `/v1/responses`. Turn 2 should succeed end-to-end.
5. Chaos test: while services are running, `docker compose exec valkey valkey-cli FLUSHDB`. Then re-send a request. Expect a WARN `"consumer_group_missing_recovering"` log from at least context-manager and broker, followed by normal request handling. No service restart required.

Rollback: revert the merge and redeploy. No data migration either direction.

## Open Questions

- Does go-redis's `XReadGroup` with `Block` ever return `redis.Nil` *while* advancing the consumer group's `last-delivered-id`? If the phantom-consume detector starts firing, that is strong evidence, and we open a follow-up change: `investigate-go-redis-xreadgroup` or similar, possibly with a reproduction repo for the library maintainers.
- Should the `ConsumeStream` helper also support `XAutoClaim` for consumer liveness? Not for this change — we have no failure case that needs it yet. If a future change adds multiple consumers per group, revisit.
- What's the right phantom-check cadence? 30s is a guess. If the symptom repeats in production, we might want 5s. Make it configurable via env var (`CONSUME_PHANTOM_CHECK_S`, default 30).
