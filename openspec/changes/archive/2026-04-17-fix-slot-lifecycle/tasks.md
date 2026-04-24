# Implementation Tasks

## 1. Messaging schema: SlotAssignedAck
- [x] 1.1 In `internal/messaging/message.go` (or the types file), add `TypeSlotAssignedAck` constant.
- [x] 1.2 In `internal/messaging/payloads.go`, add `SlotAssignedAckPayload{AgentID string; SlotID int}`.
- [x] 1.3 Add a unit test covering encode/decode round-trip.

## 2. Broker: two-phase slot assignment
- [x] 2.1 In `internal/broker/slots.go`, extend `SlotState` with `SlotProvisional`. Update `FindUnassigned` so it only returns slots in `SlotUnassigned` state (provisional counts as occupied).
- [x] 2.2 Add `SlotTable.AssignProvisional(slotID int, agentID string, priority int, correlationID string) bool` that sets state to `Provisional`, records the correlation ID, and returns false if the slot is not currently unassigned.
- [x] 2.3 Add `SlotTable.CommitAssignment(correlationID string) (slotID int, ok bool)` that promotes a provisional entry matching the correlation ID to `SlotAssigned`. Returns `false` if no provisional match exists (ack arrived after reclaim).
- [x] 2.4 Add `SlotTable.RevertProvisional(correlationID string) (slotID int, ok bool)` that returns a provisional entry to `SlotUnassigned` and returns the freed slot ID.
- [x] 2.5 Add `SlotTable.Snapshot() []SlotSnapshotEntry` returning `{SlotID, AgentID, Priority, State, AssignedAt}` for each slot for periodic logging.
- [x] 2.6 Update `handleSlotRequest` in `internal/broker/broker.go` to call `AssignProvisional`, publish the reply, and schedule a reclaim goroutine at `provisionalTimeout`.
- [x] 2.7 Add `handleSlotAssignedAck` in `internal/broker/broker.go` and wire it into the `handleMessage` type switch for `TypeSlotAssignedAck`. On ack, call `CommitAssignment`; if it returns `ok=false`, log and do nothing (slot was already reclaimed).
- [x] 2.8 Update `assignFromQueue` to use the same provisional protocol as `handleSlotRequest`.
- [x] 2.9 Unit test: happy path (request → provisional → ack → assigned).
- [x] 2.10 Unit test: ack timeout reclaim (request → provisional → no ack → revert → assignFromQueue fires).
- [x] 2.11 Unit test: late ack (request → provisional → reclaim → ack arrives → no-op).

## 3. Broker: defensive release by agent ID
- [x] 3.1 Update `handleSlotRelease` in `internal/broker/broker.go` to handle `SlotID == -1` by calling `SlotTable.ReleaseByAgent(agentID)` (already implemented) and logging the released slot(s).
- [x] 3.2 Unit test: release with `slot_id == -1` releases the agent's owned slot.
- [x] 3.3 Unit test: release with `slot_id == -1` and no owned slot is a no-op.

## 4. Broker: LLM request slot-ownership validation
- [x] 4.1 Add `SlotTable.IsOwnedBy(slotID int, agentID string) bool` returning true only if the slot is in `SlotAssigned` state owned by that agent.
- [x] 4.2 In `handleLLMRequest`, before calling `ProxyLLMRequest`, verify `IsOwnedBy(payload.SlotID, msg.Source)` and, on false, publish a done token carrying an error on the reply stream and log the rejection. Do NOT proxy the request.
- [x] 4.3 Unit test: request with owned slot proceeds.
- [x] 4.4 Unit test: request with unowned slot is rejected and the reply stream carries a done+error token.

## 5. Broker: remove slot 0 pin
- [x] 5.1 Delete the `b.slots.PinSlot(0, "main-agent", 0)` call and associated log at `internal/broker/broker.go:56-57`.
- [x] 5.2 Delete `SlotTable.PinSlot` in `internal/broker/slots.go` and any associated tests.
- [x] 5.3 Update or delete any test that asserts slot 0 is pre-pinned.

## 6. Broker: periodic slot-table snapshot
- [x] 6.1 Read `SLOT_SNAPSHOT_INTERVAL_S` (default 30) in `cmd/llm-broker/main.go` and pass the resulting `time.Duration` into `broker.New`.
- [x] 6.2 Read `PROVISIONAL_TIMEOUT_MS` (default 2000) in `cmd/llm-broker/main.go` and pass into `broker.New`.
- [x] 6.3 Add a goroutine in `Broker.Run` that ticks at the snapshot interval and logs `{"msg":"slot_table_snapshot","slots":[...]}` using `SlotTable.Snapshot`.
- [x] 6.4 Unit or integration test: snapshot log contains one entry per slot and reflects current state.

## 7. Broker: reply-publish and assign-outcome logging
- [x] 7.1 In `sendSlotAssigned`, check the error from `client.Publish`. On success, log INFO `slot_assigned_reply_published`. On error, log ERROR `slot_assigned_reply_failed` and call `RevertProvisional`.
- [x] 7.2 Apply the same pattern to the reply publish inside `assignFromQueue`.
- [x] 7.3 Wherever `SlotTable.Assign`/`AssignProvisional` is called, log WARN `slot_assign_collision` when it returns false.

## 8. Agent runtime: ack on slot assignment + defensive release
- [x] 8.1 In `internal/agent/runtime.go` `RequestSlot`, after `WaitForReply` succeeds, publish a `SlotAssignedAck` message (new `TypeSlotAssignedAck`) to `stream:broker:slot-requests` with a payload containing `AgentID` and `SlotID`. Use the same correlation ID as the original slot request. Do this before storing `r.slotID`.
- [x] 8.2 If `WaitForReply` returns any error (including `ErrTimeout`), publish a best-effort `SlotRelease{AgentID: r.agentID, SlotID: -1}` to `stream:broker:slot-requests`. Log at WARN with the correlation ID. Return the original error to the caller.
- [x] 8.3 Unit test: normal path sends one slot-request and one ack.
- [x] 8.4 Unit test: timeout path sends one slot-request and one defensive `SlotRelease` with `slot_id == -1`.

## 9. Agent runtime / main loop: structured logging
- [x] 9.1 In `cmd/main-agent/main.go` `handleRequest`, log `message_received` on entry with `{correlation_id, session_id}`.
- [x] 9.2 Log `slot_request_result` after `RequestSlot` returns with `{correlation_id, outcome, slot_id, elapsed_ms}`.
- [x] 9.3 After publishing to `stream:broker:llm-requests` inside `Runtime.Execute`, log `llm_request_published` with `{correlation_id, slot_id}`. Thread `correlationID` into `Runtime.Execute` — add it as an argument.
- [x] 9.4 On `Execute` return, log `execute_done` with `{correlation_id, slot_id, elapsed_ms, outcome}`.
- [x] 9.5 In the deferred `ReleaseSlot`, log `slot_released` with `{correlation_id, slot_id}` (or ERROR if the publish failed).
- [x] 9.6 Apply equivalent logging to `cmd/retro-agent/main.go`.

## 10. Gateway: structured logging
- [x] 10.1 In `internal/gateway/responses.go` `handleCreateResponse`, log `gateway_request_received` on entry with `{correlation_id, path, session_id, previous_response_id, stream, input_items}`.
- [x] 10.2 After `client.Publish(stream:gateway:requests)`, log `gateway_request_published` with `{correlation_id, session_id}`.
- [x] 10.3 In `handleResponsesStreaming`, log `gateway_stream_subscribed` after the `PubSubSubscribe` call.
- [x] 10.4 On first token seen in the for-select loop, log `gateway_stream_first_token` with `{correlation_id, elapsed_ms_since_published}`.
- [x] 10.5 On emitting `response.completed` (streaming) or writing the body (non-streaming), log `gateway_request_completed` with `{correlation_id, session_id, response_id, elapsed_ms}`.
- [x] 10.6 On timeout (504 path) or `ctx.Done()` in the streaming loop, log `gateway_request_timeout` or `gateway_client_disconnected` respectively.
- [x] 10.7 Mirror the logging in `handleChatCompletions`, `handleChatCompletionsStreaming`, and `handleChatCompletionsNonStreaming`.

## 11. Context-manager: structured logging
- [x] 11.1 In `internal/context/manager.go` `handleRequest`, log `context_request_decoded` after payload decode with `{correlation_id, session_id, message_count}`.
- [x] 11.2 Wrap the `muninn.Recall` call so both branches (ok/err) log `context_muninn_recall` with `{correlation_id, elapsed_ms, memory_count, outcome}`.
- [x] 11.3 After `getSessionHistory` returns, log `context_history_loaded` with `{correlation_id, session_id, history_count}`.
- [x] 11.4 After publishing the context-assembled message to the agent stream, log `context_published` with `{correlation_id, session_id, target_agent, assembled_count}`.

## 12. Integration test
- [x] 12.1 Extend `tests/integration_test.go` (or add a new test file) with a scenario that runs two consecutive `/v1/responses` turns against an in-process stack and asserts both reach the broker. The test SHALL fail with the current code (turn 2 hangs) and pass after the fix.
- [x] 12.2 Add a test that simulates a dropped slot-assigned reply (e.g., by injecting a publish failure in a fake broker client) and asserts the slot is reclaimed within `provisionalTimeout` and that `FindUnassigned` sees the slot again.

## 13. Rollout and verification
- [x] 13.1 Update `docker-compose.yml` (or document in a README) the new broker env vars `PROVISIONAL_TIMEOUT_MS` and `SLOT_SNAPSHOT_INTERVAL_S` if exposed there. (docker-compose.yml and .env.example updated.)
- [x] 13.2 Document the "restart broker once after rollout to clear pre-existing leaks" step in the change's archive notes or the project README. (Captured in proposal.md / design.md migration plan.)
- [ ] 13.3 After deployment, verify the first `slot_table_snapshot` after startup shows 4 unassigned slots (no pinned slot 0). *(Runtime verification — run after deploy.)*
- [ ] 13.4 After deployment, run two `/v1/responses` turns in Open WebUI and `grep` a single correlation_id across all service logs to confirm end-to-end tracing. *(Runtime verification — run after deploy.)*
