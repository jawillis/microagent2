package memoryservice

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SeedConfig is the in-memory representation of deploy/memory/*.yaml.
type SeedConfig struct {
	Bank       BankSeed
	Missions   map[string]string // field name → text (e.g. "retain_mission")
	Directives []DirectiveSeed
}

// BankSeed is the bank.yaml shape.
type BankSeed struct {
	BankID      string `yaml:"bank_id"`
	Name        string `yaml:"name,omitempty"`
	Disposition struct {
		Skepticism int `yaml:"skepticism"`
		Literalism int `yaml:"literalism"`
		Empathy    int `yaml:"empathy"`
	} `yaml:"disposition,omitempty"`
	// Config is a dict of bank-config overrides applied verbatim via PATCH
	// /banks/{id}/config. Any field accepted by BankConfigUpdate.updates goes
	// here. Missions loaded from deploy/memory/missions/*.yaml are merged in
	// automatically by LoadSeedConfig.
	Config map[string]interface{} `yaml:"config,omitempty"`
}

// DirectiveSeed is one YAML-declared directive.
type DirectiveSeed struct {
	Name     string   `yaml:"name"`
	Content  string   `yaml:"content"`
	Priority int      `yaml:"priority,omitempty"`
	IsActive *bool    `yaml:"is_active,omitempty"`
	Tags     []string `yaml:"tags,omitempty"`
}

type missionFile struct {
	Field string `yaml:"field"`
	Text  string `yaml:"text"`
}

// LoadSeedConfig reads bank.yaml + missions/*.yaml + directives/*.yaml from
// `dir` and returns the merged SeedConfig. Missions are merged into
// SeedConfig.Bank.Config so a single idempotent PATCH can carry the whole
// bank shape to Hindsight.
func LoadSeedConfig(dir string) (*SeedConfig, error) {
	bankPath := filepath.Join(dir, "bank.yaml")
	bank := BankSeed{}
	if data, err := os.ReadFile(bankPath); err == nil {
		if err := yaml.Unmarshal(data, &bank); err != nil {
			return nil, fmt.Errorf("memoryservice: parse %s: %w", bankPath, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("memoryservice: read %s: %w", bankPath, err)
	}
	if bank.Config == nil {
		bank.Config = map[string]interface{}{}
	}

	missions := map[string]string{}
	missionsDir := filepath.Join(dir, "missions")
	missionEntries, _ := os.ReadDir(missionsDir)
	for _, e := range missionEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(missionsDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("memoryservice: read %s: %w", e.Name(), err)
		}
		var m missionFile
		if err := yaml.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("memoryservice: parse %s: %w", e.Name(), err)
		}
		if m.Field == "" {
			return nil, fmt.Errorf("memoryservice: mission %s missing 'field'", e.Name())
		}
		missions[m.Field] = m.Text
		bank.Config[m.Field] = m.Text
	}

	var directives []DirectiveSeed
	dirDir := filepath.Join(dir, "directives")
	dirEntries, _ := os.ReadDir(dirDir)
	// Sort for stable ordering, which gives reproducible create order.
	names := make([]string, 0, len(dirEntries))
	for _, e := range dirEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dirDir, n))
		if err != nil {
			return nil, fmt.Errorf("memoryservice: read %s: %w", n, err)
		}
		var d DirectiveSeed
		if err := yaml.Unmarshal(data, &d); err != nil {
			return nil, fmt.Errorf("memoryservice: parse %s: %w", n, err)
		}
		if d.Name == "" || d.Content == "" {
			return nil, fmt.Errorf("memoryservice: directive %s missing name/content", n)
		}
		directives = append(directives, d)
	}

	return &SeedConfig{
		Bank:       bank,
		Missions:   missions,
		Directives: directives,
	}, nil
}
