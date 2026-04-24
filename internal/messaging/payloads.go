package messaging

import "encoding/json"

type ChatRequestPayload struct {
	SessionID string    `json:"session_id"`
	Messages  []ChatMsg `json:"messages"`
	Model     string    `json:"model,omitempty"`
	Stream    bool      `json:"stream"`
}

type ChatMsg struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolSchema struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCallPayload struct {
	SessionID string   `json:"session_id"`
	Call      ToolCall `json:"call"`
	Done      bool     `json:"done"`
}

type ChatResponsePayload struct {
	SessionID   string       `json:"session_id"`
	Content     string       `json:"content"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
	Done        bool         `json:"done"`
}

type ToolResult struct {
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type ToolResultPayload struct {
	SessionID string `json:"session_id"`
	CallID    string `json:"call_id"`
	Output    string `json:"output"`
}

type TokenPayload struct {
	SessionID string `json:"session_id"`
	Token     string `json:"token"`
	Done      bool   `json:"done"`
}

type SlotRequestPayload struct {
	AgentID   string `json:"agent_id"`
	Priority  int    `json:"priority"`
	SlotClass string `json:"slot_class,omitempty"`
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
	SlotID     int          `json:"slot_id"`
	SlotClass  string       `json:"slot_class,omitempty"`
	Messages   []ChatMsg    `json:"messages"`
	Stream     bool         `json:"stream"`
	Tools      []ToolSchema `json:"tools,omitempty"`
	ToolChoice string       `json:"tool_choice,omitempty"`
}

type RetroTriggerPayload struct {
	SessionID string `json:"session_id"`
	JobType   string `json:"job_type"`
}
