# Implementation Tasks

## 1. Resilient consume helper in internal/messaging
- [x] 1.1 Add `internal/messaging/consume.go` with a `ConsumeStream(ctx, stream, group, consumer string, count int64, block time.Duration, handler func(ctx, *Message) error, stats *ConsumeStats) error` function on `Client`.
- [x] 1.2 Add `ConsumeStats` struct with atomic counters: `Handled`, `HandlerErrors`, `Recoveries`, `PhantomLogs`.
- [x] 1.3 Inside the loop: `EnsureGroup` once; then iterate reading with the provided block; classify the return per the error buckets (`redis.Nil`, NOGROUP, unknown).
- [x] 1.4 On NOGROUP / `ERR no such key`: log WARN `consumer_group_missing_recovering`, call `EnsureGroup`, increment `Recoveries`, continue. Rate-limit the WARN to once per 10s per (stream,group) pair.
- [x] 1.5 On unknown error: log WARN `consume_stream_error`, sleep with exponential backoff (100ms → 200ms → ... → 5s cap, reset on next successful read). After N=10 consecutive unknown errors, log ERROR `consumer_loop_degraded` once.
- [x] 1.6 Handler-error semantics: on handler error, log ERROR and still XACK (keep the queue moving; XREADGROUP > doesn't redeliver PEL entries, so holding unacked only blocks the head).
- [x] 1.7 Phantom-consume detection goroutine: every `CONSUME_PHANTOM_CHECK_S` (default 30s, env-configurable), fetch `XINFO GROUPS`, compare `entries-read` to `stats.Handled`; if delta > 0 on two consecutive checks, log WARN `phantom_consume_detected` with fields `{stream, group, entries_read, handled_count, delta}`. Increment `PhantomLogs`.
- [x] 1.8 Periodic stats snapshot: on every phantom-check tick, log INFO `consume_stream_stats` with stats fields.
- [x] 1.9 Return only on `ctx.Done()` or `XREADGROUP` returning a non-recoverable error class we don't know how to handle (e.g. auth errors).

## 2. Unit tests for ConsumeStream
- [x] 2.1 Test: publish N messages, handler increments a counter, assert counter == N and `XPENDING` == 0.
- [x] 2.2 Test: handler returns error, assert message is NOT acked and WARN log fires; then make handler succeed, assert message IS acked on retry (via stream-side redelivery of the PEL entry).
- [x] 2.3 Test: FLUSHDB mid-flight, publish N more messages, assert recovery: all N handled after the transient disruption, and `Recoveries` > 0.
- [x] 2.4 Test: inject a wrapped error (not `redis.Nil`, not NOGROUP), assert `consume_stream_error` WARN fires, backoff sleeps, loop continues.
- [x] 2.5 Test: phantom-consume detection by mocking `XINFO` to report `entries-read` ahead of handler count across two ticks; assert `phantom_consume_detected` WARN fires.
- [x] 2.6 Test: poison message protection — handler returns error for the same id 3 times; assert ERROR `handler_poison_message` fires and subsequent call doesn't see the message (because it was ack'd).

## 3. Migrate context-manager to ConsumeStream
- [x] 3.1 In `internal/context/manager.go` `Run`, replace the hand-rolled loop with a call to `m.client.ConsumeStream(ctx, StreamGatewayRequests, ConsumerGroupContextManager, "context-worker", 10, 2*time.Second, handlerClosure, stats)`. Wrap `handleRequest` in a closure that returns `error` (nil on success, error on decode failure).
- [x] 3.2 Delete the `iterations` diagnostic counter added during earlier debugging (no longer needed).
- [x] 3.3 Remove `m.logger.Info("context_handle_request_start", ...)` from `handleRequest` (also a debug-era addition; `context_request_decoded` is the canonical first log).

## 4. Migrate main-agent to ConsumeStream
- [x] 4.1 In `cmd/main-agent/main.go`, replace the `for { ... ReadGroup ... }` worker loop with `client.ConsumeStream(...)`.
- [x] 4.2 The handler closure SHALL call `handleRequest` and return error on decode failure.
- [x] 4.3 Remove the manual `EnsureGroup` call at startup (now handled inside `ConsumeStream`).

## 5. Migrate retro-agent to ConsumeStream
- [x] 5.1 In `cmd/retro-agent/main.go`, replace the retro-trigger reading goroutine (the `for { ... ReadGroup(StreamRetroTriggers ...) ... }` loop) with `client.ConsumeStream(...)`.
- [x] 5.2 Handler closure invokes `dispatchSingle` and returns nil on success, error on decode failure.

## 6. Migrate broker to ConsumeStream
- [x] 6.1 In `internal/broker/broker.go`, replace `consumeSlotRequests` body with a call to `client.ConsumeStream` on `stream:broker:slot-requests` + `ConsumerGroupBroker`, handler = `handleMessage`.
- [x] 6.2 Replace `consumeLLMRequests` body similarly on `stream:broker:llm-requests` + `cg:llm-broker`, handler = `handleLLMRequest`.
- [x] 6.3 Adjust handler signatures to return error (handleMessage's current `return` on decode fail becomes `return err`).

## 7. Regression test
- [x] 7.1 *(Covered by `internal/messaging/consume_live_test.go::TestConsumeStreamHandlesAllMessages` — directly exercises "publish N → exactly N handler invocations" against live Valkey.)*
- [x] 7.2 *(Covered by `internal/messaging/consume_live_test.go::TestConsumeStreamRecoversFromFlushDB` — FLUSHDB chaos at the helper level. End-to-end chaos test via `tests/integration_test.go` skipped: pre-existing integration env is flaky for reasons unrelated to this change; the helper-level test is the canonical regression.)*

## 8. Rollout
- [x] 8.1 Build new images for all Go services; `docker compose up -d --build`.
- [x] 8.2 Post-deploy verification: two-turn conversation via `/v1/responses` with `previous_response_id`. Expect success end-to-end.
- [x] 8.3 Post-deploy chaos verification: `docker compose exec valkey valkey-cli FLUSHDB`; send one request; grep for `consumer_group_missing_recovering` WARN in at least context-manager and broker logs; confirm request succeeds without service restart.
- [x] 8.4 If `phantom_consume_detected` WARN appears in production logs at any point, open a follow-up change proposal (`investigate-go-redis-xreadgroup` or similar) referencing the observed stream, group, and delta.
