## ADDED Requirements

### Requirement: Conversation analysis for memory extraction
The retrospective memory extraction agent SHALL analyze completed or idle conversation sessions to identify facts, preferences, and contextual information worth storing as long-term memories in MuninnDB.

#### Scenario: Memory extraction on session inactivity
- **WHEN** a conversation session has been inactive for the configured timeout period
- **THEN** the retro agent retrieves the recent conversation history, requests an LLM slot from the broker, analyzes the conversation for memorable information, and writes extracted memories to MuninnDB

#### Scenario: No memorable content
- **WHEN** the retro agent analyzes a conversation and finds no information worth storing
- **THEN** it completes without writing any memories and releases its slot

#### Scenario: Preemption during analysis
- **WHEN** the retro agent is preempted while analyzing a conversation
- **THEN** it saves its progress (which turns have been processed) and resumes from that point when re-assigned a slot

### Requirement: Memory phrasing optimization
The retro agent SHALL phrase extracted memories in a structured format designed to maximize retrieval relevance when queried by the context manager.

#### Scenario: Structured memory output
- **WHEN** the retro agent extracts a memory from conversation
- **THEN** it writes the memory with an explicit category, key terms surfaced for embedding similarity, and an actionable directive describing how the agent should use this information

### Requirement: Skill creation from conversation patterns
The retrospective skill creation agent SHALL identify reusable patterns across conversation history and create skill entries that can be injected into future prompts to avoid reinventing solutions.

#### Scenario: Pattern identification
- **WHEN** the skill creation agent analyzes conversation history and detects a problem-solving pattern that was applied successfully
- **THEN** it creates a skill entry containing the problem class, the approach taken, and the outcome, stored in MuninnDB with appropriate metadata for retrieval

#### Scenario: Duplicate skill prevention
- **WHEN** the skill creation agent identifies a pattern that closely matches an existing skill
- **THEN** it either skips creation or proposes an update to the existing skill rather than creating a duplicate

### Requirement: Skill and memory curation
A retrospective curation agent SHALL periodically review the memory and skill library to prune low-quality entries, merge duplicates, and resolve contradictions.

#### Scenario: Duplicate memory detection
- **WHEN** the curation agent reviews the memory store and finds two or more memories conveying substantially the same information
- **THEN** it merges them into a single memory retaining the most complete and well-phrased version, and removes the redundant entries

#### Scenario: Contradictory memory resolution
- **WHEN** the curation agent finds two memories that contradict each other
- **THEN** it evaluates recency and source context, retains the more recent or better-supported memory, removes or annotates the contradicted one, and logs the resolution

#### Scenario: Stale skill pruning
- **WHEN** the curation agent identifies a skill that has not been retrieved or used within a configurable time window
- **THEN** it marks the skill for review and MAY remove it if no supporting evidence is found in recent conversations

### Requirement: Configurable trigger mechanism
Retrospective agents SHALL support configurable trigger mechanisms, with at minimum inactivity-based and session-end-based triggers.

#### Scenario: Inactivity trigger
- **WHEN** a retro agent is configured with `trigger: { type: "inactivity", timeout_seconds: N }`
- **THEN** it activates when no new messages have been added to a session for N seconds

#### Scenario: Session-end trigger
- **WHEN** a retro agent is configured with `trigger: { type: "event", event: "session_ended" }`
- **THEN** it activates when a `session_ended` event is published to the event channel
