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
	RecallLimit        int    `json:"recall_limit"`
	PrewarmLimit       int    `json:"prewarm_limit"`
	RecallDefaultTypes string `json:"recall_default_types"`
	DefaultProvenance  string `json:"default_provenance"`
	TagTaxonomy        string `json:"tag_taxonomy"`

	// Deprecated: no longer used after add-memory-panel-contribution.
	// Hindsight does not expose a caller-controlled recall threshold or
	// graph-traversal depth, and "vault" and "store_confidence" are
	// muninndb-era concepts that have no Hindsight equivalent. These
	// fields are retained to tolerate old Valkey values silently during
	// the migration window; reads ignore them.
	RecallThreshold float64 `json:"recall_threshold,omitempty"`
	MaxHops         int     `json:"max_hops,omitempty"`
	Vault           string  `json:"vault,omitempty"`
	StoreConfidence float64 `json:"store_confidence,omitempty"`
}

// DefaultRecallTypes is the default `recall_default_types` value.
const DefaultRecallTypes = "observation"

// DefaultProvenance is the default `default_provenance` value.
const DefaultProvenance = "explicit"

// DefaultTagTaxonomy is the default `tag_taxonomy` comma-separated list.
const DefaultTagTaxonomy = "identity,preferences,technical,home,ephemera"

func DefaultMemoryConfig() MemoryConfig {
	return MemoryConfig{
		RecallLimit:        5,
		PrewarmLimit:       3,
		RecallDefaultTypes: DefaultRecallTypes,
		DefaultProvenance:  DefaultProvenance,
		TagTaxonomy:        DefaultTagTaxonomy,
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
