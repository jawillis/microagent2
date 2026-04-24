package config

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func testStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return NewStore(rdb), mr
}

func TestResolveChat_Defaults(t *testing.T) {
	store, _ := testStore(t)
	cfg := ResolveChat(context.Background(), store)

	if cfg.Model != "default" {
		t.Errorf("Model = %q, want %q", cfg.Model, "default")
	}
	if cfg.RequestTimeoutS != 120 {
		t.Errorf("RequestTimeoutS = %d, want 120", cfg.RequestTimeoutS)
	}
	if cfg.SystemPrompt != "You are a helpful assistant." {
		t.Errorf("SystemPrompt = %q, want default", cfg.SystemPrompt)
	}
}

func TestResolveChat_EnvOverridesDefault(t *testing.T) {
	store, _ := testStore(t)
	t.Setenv("MODEL", "llama3")
	t.Setenv("REQUEST_TIMEOUT_S", "60")

	cfg := ResolveChat(context.Background(), store)

	if cfg.Model != "llama3" {
		t.Errorf("Model = %q, want %q", cfg.Model, "llama3")
	}
	if cfg.RequestTimeoutS != 60 {
		t.Errorf("RequestTimeoutS = %d, want 60", cfg.RequestTimeoutS)
	}
}

func TestResolveChat_ValkeyOverridesEnv(t *testing.T) {
	store, mr := testStore(t)
	t.Setenv("MODEL", "llama3")

	data, _ := json.Marshal(ChatConfig{Model: "qwen2", RequestTimeoutS: 30, SystemPrompt: "Custom prompt"})
	mr.Set(KeyChat, string(data))

	cfg := ResolveChat(context.Background(), store)

	if cfg.Model != "qwen2" {
		t.Errorf("Model = %q, want %q", cfg.Model, "qwen2")
	}
	if cfg.RequestTimeoutS != 30 {
		t.Errorf("RequestTimeoutS = %d, want 30", cfg.RequestTimeoutS)
	}
	if cfg.SystemPrompt != "Custom prompt" {
		t.Errorf("SystemPrompt = %q, want %q", cfg.SystemPrompt, "Custom prompt")
	}
}

func TestResolveMemory_Defaults(t *testing.T) {
	store, _ := testStore(t)
	cfg := ResolveMemory(context.Background(), store)

	if cfg.RecallLimit != 5 {
		t.Errorf("RecallLimit = %d, want 5", cfg.RecallLimit)
	}
	if cfg.PrewarmLimit != 3 {
		t.Errorf("PrewarmLimit = %d, want 3", cfg.PrewarmLimit)
	}
	if cfg.RecallDefaultTypes != DefaultRecallTypes {
		t.Errorf("RecallDefaultTypes = %q, want %q", cfg.RecallDefaultTypes, DefaultRecallTypes)
	}
	if cfg.DefaultProvenance != DefaultProvenance {
		t.Errorf("DefaultProvenance = %q, want %q", cfg.DefaultProvenance, DefaultProvenance)
	}
	if cfg.TagTaxonomy != DefaultTagTaxonomy {
		t.Errorf("TagTaxonomy = %q, want %q", cfg.TagTaxonomy, DefaultTagTaxonomy)
	}
}

func TestResolveMemory_ValkeyOverrides(t *testing.T) {
	store, mr := testStore(t)
	data, _ := json.Marshal(map[string]any{
		"recall_limit":         10,
		"recall_default_types": "all",
		"default_provenance":   "implicit",
	})
	mr.Set(KeyMemory, string(data))

	cfg := ResolveMemory(context.Background(), store)

	if cfg.RecallLimit != 10 {
		t.Errorf("RecallLimit = %d, want 10", cfg.RecallLimit)
	}
	if cfg.RecallDefaultTypes != "all" {
		t.Errorf("RecallDefaultTypes = %q, want all", cfg.RecallDefaultTypes)
	}
	if cfg.DefaultProvenance != "implicit" {
		t.Errorf("DefaultProvenance = %q, want implicit", cfg.DefaultProvenance)
	}
	if cfg.PrewarmLimit != 3 {
		t.Errorf("PrewarmLimit = %d, want 3 (default)", cfg.PrewarmLimit)
	}
}

func TestResolveMemory_IgnoresDeprecatedKeys(t *testing.T) {
	store, mr := testStore(t)
	// Old keys present in Valkey; Resolve should silently tolerate them.
	data, _ := json.Marshal(map[string]any{
		"recall_limit":     7,
		"vault":            "legacy",
		"max_hops":         99,
		"store_confidence": 0.1,
		"recall_threshold": 0.99,
	})
	mr.Set(KeyMemory, string(data))

	cfg := ResolveMemory(context.Background(), store)
	if cfg.RecallLimit != 7 {
		t.Errorf("RecallLimit = %d, want 7", cfg.RecallLimit)
	}
	// Deprecated fields still populate the struct (for tolerance) but
	// no code path should consume them. We only verify the non-deprecated
	// defaults remain correct despite the legacy input.
	if cfg.RecallDefaultTypes != DefaultRecallTypes {
		t.Errorf("new default dropped because of legacy payload")
	}
}

func TestResolveBroker_EnvThenValkey(t *testing.T) {
	store, mr := testStore(t)
	t.Setenv("SLOT_COUNT", "8")

	cfg := ResolveBroker(context.Background(), store)
	if cfg.SlotCount != 8 {
		t.Errorf("SlotCount = %d, want 8 (from env)", cfg.SlotCount)
	}

	data, _ := json.Marshal(map[string]any{"slot_count": 16})
	mr.Set(KeyBroker, string(data))

	cfg = ResolveBroker(context.Background(), store)
	if cfg.SlotCount != 16 {
		t.Errorf("SlotCount = %d, want 16 (from valkey)", cfg.SlotCount)
	}
}

func TestResolveRetro_Defaults(t *testing.T) {
	store, _ := testStore(t)
	cfg := ResolveRetro(context.Background(), store)

	if cfg.InactivityTimeoutS != 300 {
		t.Errorf("InactivityTimeoutS = %d, want 300", cfg.InactivityTimeoutS)
	}
	if cfg.SkillDupThreshold != 0.85 {
		t.Errorf("SkillDupThreshold = %f, want 0.85", cfg.SkillDupThreshold)
	}
	if cfg.MinHistoryTurns != 4 {
		t.Errorf("MinHistoryTurns = %d, want 4", cfg.MinHistoryTurns)
	}
	if len(cfg.CurationCategories) != 4 {
		t.Errorf("CurationCategories len = %d, want 4", len(cfg.CurationCategories))
	}
	if cfg.CurationRecallLimit != 15 {
		t.Errorf("CurationRecallLimit = %d, want 15", cfg.CurationRecallLimit)
	}
}

func TestResolveRetro_ValkeyOverrides(t *testing.T) {
	store, mr := testStore(t)
	data, _ := json.Marshal(map[string]any{
		"inactivity_timeout_s":  600,
		"curation_categories":   []string{"preference", "fact"},
		"curation_recall_limit": 25,
	})
	mr.Set(KeyRetro, string(data))

	cfg := ResolveRetro(context.Background(), store)

	if cfg.InactivityTimeoutS != 600 {
		t.Errorf("InactivityTimeoutS = %d, want 600", cfg.InactivityTimeoutS)
	}
	if len(cfg.CurationCategories) != 2 {
		t.Errorf("CurationCategories len = %d, want 2", len(cfg.CurationCategories))
	}
	if cfg.CurationRecallLimit != 25 {
		t.Errorf("CurationRecallLimit = %d, want 25", cfg.CurationRecallLimit)
	}
}

func TestResolveRetro_EnvOverridesRecallLimit(t *testing.T) {
	t.Setenv("RETRO_CURATION_RECALL_LIMIT", "8")
	store, _ := testStore(t)
	cfg := ResolveRetro(context.Background(), store)

	if cfg.CurationRecallLimit != 8 {
		t.Errorf("CurationRecallLimit = %d, want 8 (from env)", cfg.CurationRecallLimit)
	}
}

func TestStore_SaveAndLoad(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()

	values := json.RawMessage(`{"model":"test-model","request_timeout_s":30}`)
	if err := store.Save(ctx, "chat", values); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var cfg ChatConfig
	if err := store.Load(ctx, KeyChat, &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "test-model" {
		t.Errorf("Model = %q, want %q", cfg.Model, "test-model")
	}
}

func TestStore_SaveMerges(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()

	store.Save(ctx, "chat", json.RawMessage(`{"model":"m1"}`))
	store.Save(ctx, "chat", json.RawMessage(`{"request_timeout_s":30}`))

	var cfg ChatConfig
	store.Load(ctx, KeyChat, &cfg)

	if cfg.Model != "m1" {
		t.Errorf("Model = %q, want %q (preserved from first save)", cfg.Model, "m1")
	}
	if cfg.RequestTimeoutS != 30 {
		t.Errorf("RequestTimeoutS = %d, want 30 (from second save)", cfg.RequestTimeoutS)
	}
}

func TestStore_InvalidSection(t *testing.T) {
	store, _ := testStore(t)
	err := store.Save(context.Background(), "invalid", json.RawMessage(`{}`))
	if err != ErrInvalidSection {
		t.Errorf("err = %v, want ErrInvalidSection", err)
	}
}

func TestStore_ReadAll(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()

	store.Save(ctx, "chat", json.RawMessage(`{"model":"test"}`))
	store.Save(ctx, "broker", json.RawMessage(`{"slot_count":2}`))

	all, err := store.ReadAll(ctx)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if _, ok := all["chat"]; !ok {
		t.Error("ReadAll missing chat section")
	}
	if _, ok := all["broker"]; !ok {
		t.Error("ReadAll missing broker section")
	}
}

func TestValidSection(t *testing.T) {
	for _, s := range []string{"chat", "memory", "broker", "retro"} {
		if !ValidSection(s) {
			t.Errorf("ValidSection(%q) = false, want true", s)
		}
	}
	if ValidSection("invalid") {
		t.Error("ValidSection(\"invalid\") = true, want false")
	}
}
