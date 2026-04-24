## ADDED Requirements

### Requirement: Slot assignment table
The LLM broker SHALL maintain a mapping of llama-server KV cache slots to their current assignment status (agent ID, priority, or unassigned).

#### Scenario: Slot state tracking
- **WHEN** an agent is assigned a slot
- **THEN** the broker records the slot number, assigned agent ID, agent priority, and assignment timestamp

#### Scenario: Slot release
- **WHEN** an agent releases a slot (explicitly or via timeout)
- **THEN** the broker marks the slot as unassigned and publishes a `slot_available` event

### Requirement: Slot pinning for main agent
The LLM broker SHALL permanently pin one slot (slot 0) to the main interactive agent to preserve KV cache continuity for fast TTFT.

#### Scenario: Main agent slot reservation
- **WHEN** the main agent registers with priority 0
- **THEN** the broker assigns slot 0 to the main agent and SHALL NOT reassign it to any other agent

#### Scenario: Pinned slot idle
- **WHEN** slot 0 is assigned to the main agent but no active request is in progress
- **THEN** the broker SHALL NOT reclaim slot 0 for background agents — it remains pinned

### Requirement: Priority-based slot arbitration
The LLM broker SHALL assign available slots to requesting agents based on priority (lower number = higher priority). When no slots are available, the broker SHALL preempt the lowest-priority agent occupying a non-pinned slot.

#### Scenario: Slot available for request
- **WHEN** an agent requests a slot and an unassigned slot exists in the shared pool
- **THEN** the broker assigns the slot to the agent and responds with the slot number

#### Scenario: No slot available, preemption required
- **WHEN** an agent with priority P requests a slot and all shared slots are occupied by agents with priority > P
- **THEN** the broker selects the lowest-priority agent in the shared pool, sends a preemption signal, and queues the requesting agent for assignment upon slot release

#### Scenario: No slot available, no preemption possible
- **WHEN** an agent requests a slot and all occupied agents have equal or higher priority
- **THEN** the broker queues the request and responds when a slot becomes available

### Requirement: Preemption signaling
The LLM broker SHALL send preemption signals to agents via their dedicated pub/sub channel and wait for acknowledgment within a configurable timeout.

#### Scenario: Cooperative preemption
- **WHEN** the broker sends a preempt signal to an agent
- **THEN** the agent acknowledges within the timeout, releases the slot, and the broker reassigns it

#### Scenario: Preemption timeout
- **WHEN** an agent fails to acknowledge a preempt signal within the configured timeout
- **THEN** the broker force-releases the slot, marks the agent as unhealthy, and publishes a health warning event

### Requirement: Agent health monitoring
The LLM broker SHALL monitor registered agents via heartbeat signals and release slots held by unresponsive agents.

#### Scenario: Heartbeat received
- **WHEN** an agent publishes a heartbeat within the expected interval
- **THEN** the broker considers the agent healthy

#### Scenario: Missed heartbeats
- **WHEN** an agent misses heartbeats for a configurable number of consecutive intervals
- **THEN** the broker marks the agent as dead, force-releases any slot it holds, and publishes a health event

### Requirement: LLM request proxying
The LLM broker SHALL forward LLM completion requests to llama-server with the assigned `id_slot` parameter and stream responses back to the requesting agent.

#### Scenario: Proxied streaming request
- **WHEN** an agent sends a completion request through the broker with an assigned slot
- **THEN** the broker forwards the request to llama-server with `id_slot` set to the assigned slot number, `stream: true`, and relays the streaming response back to the agent via pub/sub
