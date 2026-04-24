package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

var mcpServerNameRegexp = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func ResolveChat(ctx context.Context, store *Store) ChatConfig {
	cfg := DefaultChatConfig()

	if v := os.Getenv("SYSTEM_PROMPT"); v != "" {
		cfg.SystemPrompt = v
	}
	if v := os.Getenv("MODEL"); v != "" {
		cfg.Model = v
	}
	if v, err := strconv.Atoi(os.Getenv("REQUEST_TIMEOUT_S")); err == nil {
		cfg.RequestTimeoutS = v
	}

	_ = store.Load(ctx, KeyChat, &cfg)
	return cfg
}

func ResolveMemory(ctx context.Context, store *Store) MemoryConfig {
	cfg := DefaultMemoryConfig()

	if v, err := strconv.Atoi(os.Getenv("RECALL_LIMIT")); err == nil {
		cfg.RecallLimit = v
	}
	if v, err := strconv.ParseFloat(os.Getenv("RECALL_THRESHOLD"), 64); err == nil {
		cfg.RecallThreshold = v
	}
	if v, err := strconv.Atoi(os.Getenv("MAX_HOPS")); err == nil {
		cfg.MaxHops = v
	}
	if v, err := strconv.Atoi(os.Getenv("PREWARM_LIMIT")); err == nil {
		cfg.PrewarmLimit = v
	}
	if v := os.Getenv("VAULT"); v != "" {
		cfg.Vault = v
	}
	if v, err := strconv.ParseFloat(os.Getenv("STORE_CONFIDENCE"), 64); err == nil {
		cfg.StoreConfidence = v
	}

	_ = store.Load(ctx, KeyMemory, &cfg)
	return cfg
}

func ResolveBroker(ctx context.Context, store *Store) BrokerConfig {
	cfg := DefaultBrokerConfig()

	if v, err := strconv.Atoi(os.Getenv("SLOT_COUNT")); err == nil {
		cfg.SlotCount = v
	}
	if v, err := strconv.Atoi(os.Getenv("PREEMPT_TIMEOUT_MS")); err == nil {
		cfg.PreemptTimeoutMS = v
	}

	_ = store.Load(ctx, KeyBroker, &cfg)
	return cfg
}

// ResolveMCPServers reads the stored MCP server list and returns valid
// entries. Invalid entries (missing required fields, bad names, duplicate
// names) are logged and skipped; the return is never nil (empty slice on
// missing/empty key or all-invalid entries).
func ResolveMCPServers(ctx context.Context, store *Store, logger *slog.Logger) []MCPServerConfig {
	raw, err := store.rdb.Get(ctx, KeyMCPServers).Result()
	if err == redis.Nil {
		return []MCPServerConfig{}
	}
	if err != nil {
		if logger != nil {
			logger.Warn("mcp_servers_config_read_failed", "error", err.Error())
		}
		return []MCPServerConfig{}
	}

	var entries []MCPServerConfig
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		if logger != nil {
			logger.Warn("mcp_servers_config_parse_failed", "error", err.Error())
		}
		return []MCPServerConfig{}
	}

	valid := make([]MCPServerConfig, 0, len(entries))
	seen := map[string]bool{}
	for i, e := range entries {
		if reason := validateMCPServerEntry(e, seen); reason != "" {
			if logger != nil {
				logger.Warn("mcp_server_config_invalid", "entry_index", i, "reason", reason)
			}
			continue
		}
		seen[e.Name] = true
		valid = append(valid, e)
	}
	return valid
}

// SaveMCPServers validates the full list and atomically replaces the stored
// value. Returns a typed error on validation failure.
func SaveMCPServers(ctx context.Context, store *Store, servers []MCPServerConfig) error {
	seen := map[string]bool{}
	for i, e := range servers {
		if reason := validateMCPServerEntry(e, seen); reason != "" {
			return fmt.Errorf("entry %d (%q): %s", i, e.Name, reason)
		}
		seen[e.Name] = true
	}
	data, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	return store.rdb.Set(ctx, KeyMCPServers, data, 0).Err()
}

func validateMCPServerEntry(e MCPServerConfig, seen map[string]bool) string {
	if strings.TrimSpace(e.Name) == "" {
		return "name is required"
	}
	if !mcpServerNameRegexp.MatchString(e.Name) {
		return "name must match [a-zA-Z0-9_-]+"
	}
	if seen[e.Name] {
		return "duplicate name"
	}
	if strings.TrimSpace(e.Command) == "" {
		return "command is required"
	}
	return ""
}

func ResolveRetro(ctx context.Context, store *Store) RetroConfig {
	cfg := DefaultRetroConfig()

	if v, err := strconv.Atoi(os.Getenv("INACTIVITY_TIMEOUT_S")); err == nil {
		cfg.InactivityTimeoutS = v
	}
	if v, err := strconv.ParseFloat(os.Getenv("SKILL_DUP_THRESHOLD"), 64); err == nil {
		cfg.SkillDupThreshold = v
	}
	if v, err := strconv.Atoi(os.Getenv("MIN_HISTORY_TURNS")); err == nil {
		cfg.MinHistoryTurns = v
	}
	if v := os.Getenv("CURATION_CATEGORIES"); v != "" {
		cfg.CurationCategories = strings.Split(v, ",")
	}

	_ = store.Load(ctx, KeyRetro, &cfg)
	return cfg
}
