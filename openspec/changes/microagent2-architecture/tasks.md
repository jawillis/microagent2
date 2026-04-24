## 1. Project Scaffolding

- [x] 1.1 Create Docker Compose file with service definitions for gateway, context-manager, llm-broker, main-agent, retro-agent, and external service references (Valkey, llama-server, MuninnDB)
- [x] 1.2 Create shared library/package for Valkey message schemas, correlation ID generation, and base message types
- [x] 1.3 Create Dockerfile templates for each service with common base image and dependency installation
- [x] 1.4 Create Docker network configuration and environment variable templates (.env.example)

## 2. Inter-Service Messaging Foundation

- [x] 2.1 Implement Valkey stream client wrapper with XADD/XREADGROUP/XACK operations and consumer group management
- [x] 2.2 Implement Valkey pub/sub client wrapper for event broadcasting and subscription
- [x] 2.3 Define base message schema types (type, correlation_id, timestamp, source, payload) with validation
- [x] 2.4 Implement correlation ID generation (UUID) and propagation through message chains
- [x] 2.5 Implement reply stream pattern — request includes reply_stream, responder publishes response with matching correlation_id

## 3. Agent Registration Protocol

- [x] 3.1 Define registration message schema (agent_id, priority, preemptible, capabilities, trigger, heartbeat_interval_ms)
- [x] 3.2 Implement agent-side registration publisher — publish to `stream:registry:announce` on startup
- [x] 3.3 Implement agent-side heartbeat publisher on `channel:heartbeat:{agent_id}` at declared interval
- [x] 3.4 Implement graceful deregistration — publish deregister message and release slots before shutdown
- [x] 3.5 Implement broker-side registration consumer — read `stream:registry:announce`, build active agent registry
- [x] 3.6 Implement broker-side heartbeat monitor — subscribe to heartbeat channels, detect missed beats, mark agents dead

## 4. LLM Broker

- [x] 4.1 Implement slot assignment table (slot number → agent_id, priority, assignment timestamp, or unassigned)
- [x] 4.2 Implement slot 0 pinning for main agent (priority 0) — permanent reservation, never reassigned
- [x] 4.3 Implement shared pool slot assignment — assign unassigned slots to requesting agents by priority
- [x] 4.4 Implement priority-based preemption — when no slots available and requester outranks an occupant, signal preemption
- [x] 4.5 Implement preemption signaling via `channel:agent:{agent_id}:preempt` with timeout-based force-release fallback
- [x] 4.6 Implement slot release handling — mark slot unassigned, publish `slot_available` event, assign to queued requests
- [x] 4.7 Implement LLM request proxy — forward completion requests to llama-server with `id_slot` and `stream: true`, relay streaming response back via pub/sub
- [x] 4.8 Implement health-based slot recovery — force-release slots from agents marked dead by heartbeat monitor

## 5. Context Manager

- [x] 5.1 Implement session history store in Valkey — append user/assistant messages per session, retrieve full history
- [x] 5.2 Implement MuninnDB memory retrieval client — query by user message content, receive ranked memory list
- [x] 5.3 Implement parallel fetch — session history retrieval and MuninnDB memory recall run concurrently
- [x] 5.4 Implement prompt assembly — thin system prompt → injected memories/skills → conversation history → user message
- [x] 5.5 Implement memory injection formatting — insert retrieved memories between system prompt and conversation history
- [x] 5.6 Implement request/reply flow — consume from `stream:gateway:requests`, assemble context, publish to agent request stream
- [x] 5.7 Implement speculative memory pre-warming — extract topics from assistant response, pre-fetch and cache memories for next turn

## 6. Agent Runtime (Shared)

- [x] 6.1 Implement slot request/release lifecycle — publish slot request to broker, wait for assignment, release on completion or preemption
- [x] 6.2 Implement LLM streaming execution loop — send completion request via broker, process tokens as they arrive, maintain progress log
- [x] 6.3 Implement preemption handler — subscribe to `channel:agent:{agent_id}:preempt`, save progress log, cancel stream, release slot, acknowledge
- [x] 6.4 Implement resumption support — include partial output from progress log in new request context after preemption

## 7. Gateway Service

- [x] 7.1 Implement HTTP server with `/v1/chat/completions` endpoint accepting OpenAI-format requests
- [x] 7.2 Implement request validation — check required fields (messages array, model), return OpenAI-format error responses for invalid input
- [x] 7.3 Implement session ID derivation — generate new or derive stable session identifier from request context
- [x] 7.4 Implement inbound translation — extract messages, generate correlation_id, publish to `stream:gateway:requests`
- [x] 7.5 Implement non-streaming response — wait for complete response on reply stream, translate to OpenAI chat.completion format
- [x] 7.6 Implement streaming response — subscribe to `channel:tokens:{session_id}`, wrap tokens as SSE chat.completion.chunk events, send `[DONE]` sentinel

## 8. Main Agent

- [x] 8.1 Implement main agent container using shared agent runtime with priority 0 and preemptible=false
- [x] 8.2 Register with capabilities and trigger configuration (always-on, request-driven)
- [x] 8.3 Implement token forwarding — publish each received LLM token to `channel:tokens:{session_id}` for gateway streaming
- [x] 8.4 Implement request consumption — read from agent request stream, invoke runtime execution loop, publish complete response

## 9. Retrospective Agents

- [x] 9.1 Implement retro agent base — shared runtime with priority 1, preemptible=true, configurable trigger mechanism
- [x] 9.2 Implement inactivity trigger — subscribe to session events, activate when no messages for configured timeout_seconds
- [x] 9.3 Implement session-end trigger — subscribe to `channel:events`, activate on `session_ended` event
- [x] 9.4 Implement memory extraction agent — analyze conversation history, identify facts/preferences/context worth storing, write to MuninnDB
- [x] 9.5 Implement structured memory output — write memories with category, key terms for embedding similarity, and actionable directive
- [x] 9.6 Implement skill creation agent — detect reusable problem-solving patterns across conversation history, create skill entries in MuninnDB
- [x] 9.7 Implement duplicate skill prevention — check existing skills before creation, skip or update existing rather than duplicate
- [x] 9.8 Implement curation agent — review memory/skill library for duplicates, contradictions, and stale entries; merge, resolve, or prune
- [x] 9.9 Implement preemption progress checkpointing — save which conversation turns have been processed, resume from checkpoint when re-assigned

## 10. Integration Testing

- [x] 10.1 End-to-end test: client → gateway → context manager → main agent → broker → llama-server → streaming response back to client
- [x] 10.2 Test correlation ID propagation across all service boundaries in a full request lifecycle
- [x] 10.3 Test preemption flow: retro agent working → higher-priority request → preemption signal → slot release → main agent assigned
- [x] 10.4 Test agent registration, heartbeat, and dead-agent slot recovery
- [x] 10.5 Test memory injection: store memory in MuninnDB, send relevant query, verify memory appears in assembled prompt
- [x] 10.6 Test retro agent trigger mechanisms: inactivity timeout and session-end event
