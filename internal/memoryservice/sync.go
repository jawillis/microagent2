package memoryservice

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	"microagent2/internal/hindsight"
)

// Syncer applies a SeedConfig to Hindsight idempotently: creates the bank if
// absent, PATCHes config fields that differ, and reconciles directives by
// name (create if missing, update if content/priority/tags differ). YAML is
// authoritative — live-edited fields in Hindsight UI get overwritten.
type Syncer struct {
	hc     *hindsight.Client
	bankID string
	logger *slog.Logger
}

// NewSyncer builds a Syncer.
func NewSyncer(hc *hindsight.Client, bankID string, logger *slog.Logger) *Syncer {
	return &Syncer{hc: hc, bankID: bankID, logger: logger}
}

// Apply performs a full sync pass. It is safe to call repeatedly.
func (s *Syncer) Apply(ctx context.Context, seed *SeedConfig) error {
	if seed.Bank.BankID != "" && seed.Bank.BankID != s.bankID {
		return fmt.Errorf("memoryservice: bank.yaml bank_id=%q disagrees with MEMORY_BANK_ID=%q", seed.Bank.BankID, s.bankID)
	}

	if err := s.ensureBank(ctx, seed); err != nil {
		return fmt.Errorf("memoryservice: ensure bank: %w", err)
	}
	if err := s.syncConfig(ctx, seed); err != nil {
		return fmt.Errorf("memoryservice: sync config: %w", err)
	}
	if err := s.syncDirectives(ctx, seed); err != nil {
		return fmt.Errorf("memoryservice: sync directives: %w", err)
	}
	return nil
}

func (s *Syncer) ensureBank(ctx context.Context, seed *SeedConfig) error {
	// Hindsight's /banks/{id} endpoint does not support GET; use the list
	// endpoint and filter. Bank counts are small enough that this is fine.
	list, err := s.hc.ListBanks(ctx)
	if err != nil {
		return err
	}
	for _, b := range list.Banks {
		if b.BankID == s.bankID {
			return nil
		}
	}
	req := hindsight.CreateBankRequest{BankID: s.bankID, Name: seed.Bank.Name}
	if _, err := s.hc.CreateBank(ctx, req); err != nil {
		return err
	}
	s.logger.Info("memory_bank_created", "bank_id", s.bankID)
	return nil
}

func (s *Syncer) syncConfig(ctx context.Context, seed *SeedConfig) error {
	if len(seed.Bank.Config) == 0 {
		return nil
	}
	current, err := s.hc.GetBankConfig(ctx, s.bankID)
	if err != nil {
		return err
	}
	updates := map[string]interface{}{}
	for k, want := range seed.Bank.Config {
		got, ok := current.Config[k]
		if !ok || !reflect.DeepEqual(got, want) {
			updates[k] = want
		}
	}
	if len(updates) == 0 {
		s.logger.Info("memory_bank_config_in_sync", "bank_id", s.bankID)
		return nil
	}
	if _, err := s.hc.PatchBankConfig(ctx, s.bankID, hindsight.BankConfigUpdate{Updates: updates}); err != nil {
		return err
	}
	keys := make([]string, 0, len(updates))
	for k := range updates {
		keys = append(keys, k)
	}
	s.logger.Info("memory_bank_config_patched", "bank_id", s.bankID, "fields", keys)
	return nil
}

func (s *Syncer) syncDirectives(ctx context.Context, seed *SeedConfig) error {
	if len(seed.Directives) == 0 {
		return nil
	}
	existing, err := s.hc.ListDirectives(ctx, s.bankID)
	if err != nil {
		return err
	}
	byName := map[string]hindsight.Directive{}
	for _, d := range existing.Items {
		byName[d.Name] = d
	}

	for _, want := range seed.Directives {
		active := true
		if want.IsActive != nil {
			active = *want.IsActive
		}
		cur, ok := byName[want.Name]
		if !ok {
			if _, err := s.hc.CreateDirective(ctx, s.bankID, hindsight.CreateDirectiveRequest{
				Name:     want.Name,
				Content:  want.Content,
				Priority: want.Priority,
				IsActive: active,
				Tags:     want.Tags,
			}); err != nil {
				return fmt.Errorf("create directive %s: %w", want.Name, err)
			}
			s.logger.Info("memory_directive_created", "bank_id", s.bankID, "name", want.Name, "priority", want.Priority)
			continue
		}
		if cur.Content == want.Content && cur.Priority == want.Priority && cur.IsActive == active && tagsEqual(cur.Tags, want.Tags) {
			continue
		}
		update := hindsight.UpdateDirectiveRequest{
			Content:  &want.Content,
			Priority: &want.Priority,
			IsActive: &active,
			Tags:     want.Tags,
		}
		if _, err := s.hc.UpdateDirective(ctx, s.bankID, cur.ID, update); err != nil {
			return fmt.Errorf("update directive %s: %w", want.Name, err)
		}
		s.logger.Info("memory_directive_updated", "bank_id", s.bankID, "name", want.Name, "id", cur.ID)
	}
	return nil
}

// CheckDenylist scans all mission and directive text for entries in
// denylist (comma-separated names). Logs WARN per hit but does not
// block sync. Returns the count of hits.
func CheckDenylist(seed *SeedConfig, denylist string, logger *slog.Logger) int {
	if denylist == "" || seed == nil {
		return 0
	}
	names := splitDenylist(denylist)
	if len(names) == 0 {
		return 0
	}
	hits := 0
	for field, text := range seed.Missions {
		lower := strings.ToLower(text)
		for _, name := range names {
			if strings.Contains(lower, strings.ToLower(name)) {
				logger.Warn("identity_hardcoded_name_detected",
					"source", "mission",
					"field", field,
					"name", name,
				)
				hits++
			}
		}
	}
	for _, d := range seed.Directives {
		lower := strings.ToLower(d.Content)
		for _, name := range names {
			if strings.Contains(lower, strings.ToLower(name)) {
				logger.Warn("identity_hardcoded_name_detected",
					"source", "directive",
					"name_field", d.Name,
					"name", name,
				)
				hits++
			}
		}
	}
	return hits
}

func splitDenylist(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		t := strings.TrimSpace(part)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func tagsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, t := range a {
		set[t] = true
	}
	for _, t := range b {
		if !set[t] {
			return false
		}
	}
	return true
}
