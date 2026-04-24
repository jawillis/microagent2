package messaging

type ChatRequestPayload struct {
	SessionID string    `json:"session_id"`
	Messages  []ChatMsg `json:"messages"`
	Model     string    `json:"model,omitempty"`
	Stream    bool      `json:"stream"`
}

type ChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatResponsePayload struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Done      bool   `json:"done"`
}

type TokenPayload struct {
	SessionID string `json:"session_id"`
	Token     string `json:"token"`
	Done      bool   `json:"done"`
}

type SlotRequestPayload struct {
	AgentID  string `json:"agent_id"`
	Priority int    `json:"priority"`
}

type SlotAssignedPayload struct {
	SlotID int `json:"slot_id"`
}

type SlotAssignedAckPayload struct {
	AgentID string `json:"agent_id"`
	SlotID  int    `json:"slot_id"`
}

type SlotReleasePayload struct {
	AgentID string `json:"agent_id"`
	SlotID  int    `json:"slot_id"`
}

type PreemptPayload struct {
	Reason string `json:"reason"`
}

type RegisterPayload struct {
	AgentID            string   `json:"agent_id"`
	Priority           int      `json:"priority"`
	Preemptible        bool     `json:"preemptible"`
	Capabilities       []string `json:"capabilities"`
	Trigger            string   `json:"trigger"`
	HeartbeatIntervalMS int     `json:"heartbeat_interval_ms"`
}

type DeregisterPayload struct {
	AgentID string `json:"agent_id"`
}

type SessionEventPayload struct {
	SessionID string `json:"session_id"`
	Event     string `json:"event"`
}

type ContextAssembledPayload struct {
	SessionID    string    `json:"session_id"`
	Messages     []ChatMsg `json:"messages"`
	TargetAgent  string    `json:"target_agent"`
	ReplyStream  string    `json:"reply_stream"`
}

type LLMRequestPayload struct {
	SlotID   int       `json:"slot_id"`
	Messages []ChatMsg `json:"messages"`
	Stream   bool      `json:"stream"`
}

type RetroTriggerPayload struct {
	SessionID string `json:"session_id"`
	JobType   string `json:"job_type"`
}
