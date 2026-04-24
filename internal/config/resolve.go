package config

import (
	"context"
	"os"
	"strconv"
	"strings"
)

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
