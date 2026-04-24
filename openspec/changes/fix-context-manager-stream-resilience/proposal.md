## Why

After the `fix-slot-lifecycle` change shipped, live verification revealed that `/v1/responses` still hangs on the second turn when `previous_response_id` is set. The slot leak is gone and structured logging confirms it, but a different failure mode surfaces: the gateway publishes turn 2 to `stream:gateway:requests`, Valkey confirms the message was delivered and ack'd by the `cg:context-manager` consumer group (`XLEN=2, entries-read=2, pending=0, last-delivered-id` matches turn 2's XADD id), yet the context-manager's `handleRequest` is never called — no `context_handle_request_start` log fires for turn 2. The pre-existing `TestEndToEnd` integration test reproduces the identical pattern on base code, so this is not caused by the slot-lifecycle change; it was hidden by it. Every long-running consumer loop in the codebase — context-manager, main-agent, broker (slot requests and LLM requests) — shares the same `if err != nil { continue }` read-loop shape, which flattens every error class (genuine empty result, NOGROUP after `FLUSHDB`, connection blip, serialization bug) into the same silent retry. Operators see a stuck system with no signal; state is never recoverable without a service restart.

## What Changes

- New resilient read-loop helper in `internal/messaging` that distinguishes `redis.Nil` (normal no-messages) from real errors, auto-recreates the consumer group on `NOGROUP` / "no such key" errors, and surfaces everything else at WARN with exponential backoff.
- Apply the resilient read-loop to every long-running consumer: `context.Manager.Run`, `cmd/main-agent/main.go` worker loop, `cmd/retro-agent/main.go` retro-trigger loop, `broker.consumeSlotRequests`, `broker.consumeLLMRequests`.
- Add a *phantom-consume* diagnostic: when a consumer loop goes idle but `XINFO` shows `entries-read` has advanced beyond what the service has locally processed (tracked via a small per-loop counter), log a WARN with the delta so this class of failure is visible rather than silent.
- Add a regression test harness: publish N messages to a stream, assert exactly N `handleRequest` invocations fire and exactly N `XACK` calls happen, against a fresh Valkey instance. Should fail reliably on current code and pass after the fix.
- Consumer groups self-heal across `FLUSHDB`, Valkey restart, or other consumer-group disruption without needing a service bounce.

## Capabilities

### New Capabilities
- None.

### Modified Capabilities
- `inter-service-messaging`: read-loop semantics gain a resilience contract. A consumer that loses its group (NOGROUP) recovers without operator intervention. `redis.Nil` is no longer conflated with recoverable errors.
- `context-management`: resilient read-loop; visible WARN logs on non-`redis.Nil` errors; phantom-consume detection.
- `agent-runtime`: resilient read-loop applied to main-agent and retro-agent worker loops.
- `llm-broker`: resilient read-loop applied to the slot-requests and llm-requests consumers.

## Impact

- **Failure mode visibility**: Operators see a WARN log the moment a read loop is in trouble, rather than discovering via user-reported hangs minutes or hours later.
- **Self-healing**: `FLUSHDB`-induced NOGROUP states no longer require restarting every service. The read loop observes the error, calls `EnsureGroup`, and resumes. Same for Valkey restarts that wipe stream state.
- **Backwards compatibility**: No message-schema change, no new streams, no new external dependencies. No user-facing API change.
- **Latency**: One extra `EnsureGroup` call on the rare NOGROUP path. Negligible next to normal request latency.
- **Phantom-consume guard**: Even if the underlying root cause turns out to be a go-redis/Valkey interaction (out of scope for this change), the diagnostic turns an invisible hang into a loud one — giving us a real signal for the follow-up investigation.
- **Non-goal**: Replacing go-redis, rewriting the messaging architecture, or changing the slot-lifecycle protocol. If the root cause is inside go-redis or the Valkey Streams implementation, this change provides the instrumentation to pin it down; the fix for that library-level issue is tracked separately.
