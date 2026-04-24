## Context

microagent2 is a greenfield project building a self-improving LLM agent as a set of Docker-based microservices. The system uses an external llama-server instance (llama.cpp) for inference, MuninnDB for memory storage/recall, and Valkey for all inter-service communication. There is no existing codebase — all services are new.

Key constraints:
- llama-server runs externally (likely on the host for GPU access) and exposes a limited number of KV cache slots via `id_slot`
- MuninnDB is a pre-existing service with its own API — it is integrated, not built
- All inter-service communication flows through Valkey — services never call each other directly
- The system must be extensible: adding a new agent type means adding a Docker container, not modifying existing services

## Goals / Non-Goals

**Goals:**
- Sub-second TTFT for interactive requests through KV cache slot pinning and thin system prompts
- Extensible agent architecture where new agent containers self-register and participate without code changes to existing services
- Self-improvement loop: retrospective agents extract memories and skills from conversation history during idle time
- OpenAI-compatible API so microagent2 is a drop-in replacement for any tool expecting an OpenAI endpoint
- Clean service boundaries with well-defined Valkey message contracts

**Non-Goals:**
- Multi-user / multi-tenant support (single-user system for now)
- Fine-tuning or model training — improvements are through memory and skills, not weight updates
- Sandbox/experimental agent tier (architecturally planned for but not in v1)
- Tool-use / function-calling support (future capability)
- Web UI or admin dashboard

## Decisions

### 1. Valkey hybrid: Streams for request/reply, pub/sub for events

**Decision:** Use Valkey Streams (XADD/XREADGROUP) for durable request/reply workflows and pub/sub for ephemeral signals (token streaming, preemption, system events).

**Alternatives considered:**
- Pure pub/sub: No durability, messages lost if consumer is down. Request/reply requires awkward reply channels.
- Pure streams: Overhead for ephemeral data like token-by-token streaming. Consumer groups add complexity where broadcast semantics are simpler.
- gRPC between services: Tight coupling, service discovery complexity, harder to add new services without code changes.

**Rationale:** Streams give durability and consumer groups for the request pipeline (no lost messages). Pub/sub is natural for broadcast events. Both live in Valkey — one dependency.

### 2. LLM broker as a dedicated service

**Decision:** A dedicated LLM broker service sits between agents and llama-server. Agents never call llama-server directly.

**Alternatives considered:**
- Direct access with distributed locking: Agents coordinate via Valkey locks. Harder to implement preemption correctly, race conditions around slot assignment.
- Sidecar proxy: Each agent container gets a proxy. Duplicates slot management logic.

**Rationale:** Centralized slot arbitration is simpler and safer. The broker is thin (slot table, priority queue, health checks) — not a monolith. It's the only component that knows about llama-server's slot topology. Agents just say "give me a slot" and "I'm done."

### 3. Agent self-registration via Valkey

**Decision:** Agent containers register on startup by publishing to `stream:registry:announce` with their metadata (priority, triggers, capabilities, preemptibility). The broker and other services discover agents from this stream.

**Alternatives considered:**
- Static configuration (docker-compose labels or config file): Requires restart or config reload to add agents.
- Service discovery tool (Consul, etcd): Additional infrastructure dependency for a relatively simple need.

**Rationale:** Valkey is already the communication backbone. Registration is a stream message — durable, ordered, and queryable. New agents start, announce, and participate. Heartbeats on dedicated pub/sub channels handle health monitoring.

### 4. Slot pinning for the main agent, shared pool for background agents

**Decision:** Slot 0 is permanently pinned to the main (interactive) agent. Remaining slots are a shared pool for background agents (retro jobs, future sandbox). Background agents are preemptible.

**Alternatives considered:**
- All slots pooled: Risk of cache eviction on the main agent's slot, destroying the TTFT benefit.
- Dedicated slot per agent type: Wastes KV cache memory when background agents are idle (which is most of the time).

**Rationale:** The main agent's TTFT depends on KV cache hits from a stable prompt prefix. Pinning guarantees this. Background agents have looser latency requirements and can tolerate cache misses after preemption. Shared pool maximizes utilization for background work.

### 5. Context manager handles memory injection, not agents

**Decision:** The context/session manager is responsible for querying MuninnDB, selecting relevant memories, and assembling the final prompt. Agents receive a pre-assembled context — they don't do their own memory retrieval.

**Alternatives considered:**
- Agent-side retrieval: Each agent queries MuninnDB directly. Duplicates retrieval logic across agent types.
- Memory service as middleware: A separate service that intercepts requests and enriches them. Adds latency and another hop.

**Rationale:** Centralizing prompt assembly in the context manager ensures consistent memory injection strategy, enables parallel fetch (session history + memory recall simultaneously), and keeps agents focused on LLM interaction. It also means memory retrieval strategy can evolve without changing any agent code.

### 6. Retrospective agents are autonomous containers triggered by events

**Decision:** Each retro job type (memory extraction, skill creation, skill pruning, etc.) is an independent container that subscribes to system events and self-triggers. They are not orchestrated by a central scheduler.

**Alternatives considered:**
- Centralized scheduler service: Owns all background job logic. Adding a new job type requires modifying the scheduler — violates extensibility goal.
- Cron-based triggering: External cron triggers jobs. Simple but doesn't respond to events (session end, inactivity).

**Rationale:** Autonomous agents align with the extensibility goal. Each container encapsulates its own trigger logic (inactivity timer, session-end event, periodic sweep) and its own LLM prompt engineering. The system event bus (pub/sub) provides the signals. Adding a new retro job type is just a new container.

### 7. Preemption via pub/sub signaling with cooperative shutdown

**Decision:** When the broker needs to reclaim a slot, it publishes a preempt signal on the agent's dedicated channel. The agent is responsible for saving progress, canceling its LLM stream, and releasing the slot. The broker waits for acknowledgment with a timeout — if the agent doesn't respond, the broker force-releases the slot and marks the agent unhealthy.

**Alternatives considered:**
- Kill the container: Destructive, loses all in-flight state, slow to restart.
- Pause the container (docker pause): Freezes the process but doesn't release the LLM slot — llama-server still holds the KV cache.

**Rationale:** Cooperative preemption lets agents checkpoint their progress for potential resumption. The timeout-based fallback handles hung agents. Streaming from the LLM means the agent has an up-to-date progress log — it knows exactly where it was interrupted.

## Risks / Trade-offs

**Memory retrieval latency on critical path** → Accept the latency cost. Mitigate through parallel fetch (history + memory simultaneously), KV cache gains on subsequent tokens, and potential speculative pre-warming of likely-relevant memories after each turn.

**KV cache loss on preemption** → Background agents lose their cached prefix when preempted. Acceptable because background work has loose latency requirements. If a retro job is expensive to restart, it can checkpoint its partial output and resume from the last completed subtask rather than replaying the full LLM context.

**Valkey as single point of failure** → All communication flows through Valkey. Mitigate with Valkey persistence (AOF) and health monitoring. For v1, single-instance Valkey is acceptable given the single-user constraint.

**Skill/memory quality degradation over time** → Unsupervised memory extraction and skill creation may produce low-quality or contradictory entries. Mitigate with dedicated pruning/curation retro jobs and (future) sandbox evaluation. Monitor memory store growth and retrieval quality metrics.

**llama-server slot exhaustion** → If more agents register than slots exist, low-priority work stalls indefinitely. Mitigate with configurable slot count awareness in the broker, queue depth alerts, and priority-based admission control.

**Agent misbehavior** → A buggy agent container could fail to release slots, spam the event bus, or write bad memories. Mitigate with heartbeat-based health checks, broker-side force-release timeouts, and rate limiting on memory writes.
