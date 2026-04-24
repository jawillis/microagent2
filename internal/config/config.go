package config

type ChatConfig struct {
	SystemPrompt    string `json:"system_prompt"`
	Model           string `json:"model"`
	RequestTimeoutS int    `json:"request_timeout_s"`
}

func DefaultChatConfig() ChatConfig {
	return ChatConfig{
		SystemPrompt:    "You are a helpful assistant.",
		Model:           "default",
		RequestTimeoutS: 120,
	}
}

type MemoryConfig struct {
	RecallLimit     int     `json:"recall_limit"`
	RecallThreshold float64 `json:"recall_threshold"`
	MaxHops         int     `json:"max_hops"`
	PrewarmLimit    int     `json:"prewarm_limit"`
	Vault           string  `json:"vault"`
	StoreConfidence float64 `json:"store_confidence"`
}

func DefaultMemoryConfig() MemoryConfig {
	return MemoryConfig{
		RecallLimit:     5,
		RecallThreshold: 0.5,
		MaxHops:         2,
		PrewarmLimit:    3,
		Vault:           "default",
		StoreConfidence: 0.9,
	}
}

type BrokerConfig struct {
	SlotCount        int `json:"slot_count"`
	PreemptTimeoutMS int `json:"preempt_timeout_ms"`
}

func DefaultBrokerConfig() BrokerConfig {
	return BrokerConfig{
		SlotCount:        4,
		PreemptTimeoutMS: 5000,
	}
}

type RetroConfig struct {
	InactivityTimeoutS  int      `json:"inactivity_timeout_s"`
	SkillDupThreshold   float64  `json:"skill_dup_threshold"`
	MinHistoryTurns     int      `json:"min_history_turns"`
	CurationCategories  []string `json:"curation_categories"`
	CurationRecallLimit int      `json:"curation_recall_limit"`
}

func DefaultRetroConfig() RetroConfig {
	return RetroConfig{
		InactivityTimeoutS:  300,
		SkillDupThreshold:   0.85,
		MinHistoryTurns:     4,
		CurationCategories:  []string{"preference", "fact", "context", "skill"},
		CurationRecallLimit: 15,
	}
}

type MCPServerConfig struct {
	Name    string            `json:"name"`
	Enabled bool              `json:"enabled"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}
