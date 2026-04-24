package memoryservice

import (
	"path/filepath"
	"testing"
)

func TestLoadSeedConfigFromRepoYAML(t *testing.T) {
	// Absolute path to the in-repo deploy/memory dir.
	dir, err := filepath.Abs("../../deploy/memory")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	seed, err := LoadSeedConfig(dir)
	if err != nil {
		t.Fatalf("LoadSeedConfig: %v", err)
	}
	if seed.Bank.BankID != "microagent2" {
		t.Fatalf("bank_id = %q", seed.Bank.BankID)
	}
	// Missions should have merged into Bank.Config.
	if _, ok := seed.Bank.Config["retain_mission"]; !ok {
		t.Fatal("retain_mission not merged into config")
	}
	if _, ok := seed.Bank.Config["observations_mission"]; !ok {
		t.Fatal("observations_mission not merged")
	}
	if _, ok := seed.Bank.Config["reflect_mission"]; !ok {
		t.Fatal("reflect_mission not merged")
	}
	if _, ok := seed.Bank.Config["enable_observations"]; !ok {
		t.Fatal("enable_observations not preserved")
	}
	if len(seed.Directives) < 4 {
		t.Fatalf("directives: got %d, want >= 4", len(seed.Directives))
	}
	// Directive sort should be deterministic (filename order).
	names := make([]string, 0, len(seed.Directives))
	for _, d := range seed.Directives {
		names = append(names, d.Name)
	}
	want := []string{"user_subjective_authority", "external_facts_require_sources", "researched_claims_cite_sources", "inferred_memories_require_ratification"}
	for i, w := range want {
		if i >= len(names) || names[i] != w {
			t.Fatalf("directive order: got %v, want prefix %v", names, want)
		}
	}
}
