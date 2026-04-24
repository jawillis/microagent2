## Why

Current LLM agent architectures suffer from bloated system prompts that slow time-to-first-token (TTFT), monolithic designs that resist extension, and no mechanism for self-improvement. microagent2 addresses these by building a microservice-based agent that replaces prompt bloat with dynamic memory injection, uses llama.cpp's KV cache slot pinning for fast TTFT, and employs background retrospective agents that continuously improve response quality through memory curation and automatic skill creation — all while exposing a standard OpenAI-compatible API for easy integration.

## What Changes

- Introduce a Docker-based microservice architecture using Valkey (pub/sub + streams) for inter-service communication
- Implement a gateway service exposing an OpenAI-compatible API endpoint (chat completions with streaming SSE)
- Implement a context/session manager responsible for session history, memory retrieval, and prompt assembly with a thin system prompt
- Implement an LLM broker service that arbitrates access to llama-server's KV cache slots across agents with different priorities
- Integrate MuninnDB as the memory microservice for vector-based memory recall and storage
- Integrate llama-server (llama.cpp) as the external LLM, using `id_slot` for KV cache slot pinning
- Implement a main agent container (priority 0, non-preemptible) handling interactive user requests with streaming
- Implement a retrospective agent container (priority 1, preemptible) that analyzes completed conversations to extract memories, create skills, and curate the memory/skill library
- Establish an agent registration protocol where new agent containers self-register via Valkey with their priority, trigger conditions, and capabilities — enabling extensibility without modifying existing services
- Implement a priority-based preemption mechanism where higher-priority agents can reclaim LLM slots from lower-priority agents mid-stream

## Capabilities

### New Capabilities
- `gateway-api`: OpenAI-compatible chat completions endpoint with streaming SSE, request translation between OpenAI format and internal message protocol
- `context-management`: Session history tracking, memory retrieval and injection, prompt assembly with thin system prompt, speculative memory pre-warming
- `llm-broker`: KV cache slot management, priority-based slot arbitration, preemption signaling, agent health monitoring via heartbeats
- `agent-runtime`: Core agent execution loop — LLM streaming, progress tracking for resumption after preemption, slot request/release lifecycle
- `agent-registration`: Self-registration protocol for agent containers, priority and capability declaration, trigger configuration, heartbeat contract
- `inter-service-messaging`: Valkey hybrid communication layer — streams for durable request/reply, pub/sub for ephemeral events and token streaming, correlation ID tracking
- `retrospection`: Background analysis of conversation history triggered by inactivity/session-end, memory extraction, skill creation, skill pruning, memory deduplication

### Modified Capabilities

(none — greenfield project)

## Impact

- **Infrastructure**: Requires Docker, Valkey, llama-server with GPU access, MuninnDB instance
- **External dependencies**: llama.cpp (id_slot API), MuninnDB (memory storage/recall API), Valkey 7+ (streams + pub/sub)
- **Network**: All services communicate over Docker network; llama-server may run on host for GPU access
- **API surface**: Single external endpoint (OpenAI-compatible); all other communication is internal via Valkey
