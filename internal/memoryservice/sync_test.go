package memoryservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"microagent2/internal/hindsight"
)

// bankRecorder captures GET / PATCH / POST against the fake Hindsight.
type bankRecorder struct {
	*httptest.Server
	bankExists     atomic.Bool
	getConfigCalls atomic.Int32
	patchCalls     atomic.Int32
	createBankHits atomic.Int32
	listDirHits    atomic.Int32
	createDirHits  atomic.Int32
	updateDirHits  atomic.Int32

	lastPatch    hindsight.BankConfigUpdate
	currentCfg   map[string]interface{}
	currentDirs  []hindsight.Directive
}

func newBankRecorder(t *testing.T) *bankRecorder {
	t.Helper()
	br := &bankRecorder{
		currentCfg:  map[string]interface{}{},
		currentDirs: []hindsight.Directive{},
	}
	br.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/banks"):
			banks := hindsight.BankListResponse{}
			if br.bankExists.Load() {
				banks.Banks = append(banks.Banks, hindsight.BankListItem{BankID: "microagent2"})
			}
			_ = json.NewEncoder(w).Encode(banks)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/banks/microagent2"):
			br.createBankHits.Add(1)
			br.bankExists.Store(true)
			_ = json.NewEncoder(w).Encode(hindsight.BankListItem{BankID: "microagent2"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/banks/microagent2/config"):
			br.getConfigCalls.Add(1)
			_ = json.NewEncoder(w).Encode(hindsight.BankConfigResponse{
				BankID:    "microagent2",
				Config:    br.currentCfg,
				Overrides: map[string]interface{}{},
			})
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/banks/microagent2/config"):
			br.patchCalls.Add(1)
			br.lastPatch = hindsight.BankConfigUpdate{} // reset before decode
			_ = json.NewDecoder(r.Body).Decode(&br.lastPatch)
			for k, v := range br.lastPatch.Updates {
				br.currentCfg[k] = v
			}
			_ = json.NewEncoder(w).Encode(hindsight.BankConfigResponse{
				BankID:    "microagent2",
				Config:    br.currentCfg,
				Overrides: br.lastPatch.Updates,
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/banks/microagent2/directives"):
			br.listDirHits.Add(1)
			_ = json.NewEncoder(w).Encode(hindsight.DirectiveListResponse{Items: br.currentDirs})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/banks/microagent2/directives"):
			br.createDirHits.Add(1)
			var req hindsight.CreateDirectiveRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			d := hindsight.Directive{
				ID:       fmt.Sprintf("d-%d", br.createDirHits.Load()),
				BankID:   "microagent2",
				Name:     req.Name,
				Content:  req.Content,
				Priority: req.Priority,
				IsActive: req.IsActive,
				Tags:     req.Tags,
			}
			br.currentDirs = append(br.currentDirs, d)
			_ = json.NewEncoder(w).Encode(d)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/directives/"):
			br.updateDirHits.Add(1)
			id := strings.TrimPrefix(r.URL.Path, "")
			segs := strings.Split(id, "/")
			dirID := segs[len(segs)-1]
			var req hindsight.UpdateDirectiveRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			for i := range br.currentDirs {
				if br.currentDirs[i].ID == dirID {
					if req.Content != nil {
						br.currentDirs[i].Content = *req.Content
					}
					if req.Priority != nil {
						br.currentDirs[i].Priority = *req.Priority
					}
					if req.IsActive != nil {
						br.currentDirs[i].IsActive = *req.IsActive
					}
					if req.Tags != nil {
						br.currentDirs[i].Tags = req.Tags
					}
					_ = json.NewEncoder(w).Encode(br.currentDirs[i])
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(br.Server.Close)
	_ = io.Discard
	return br
}

func makeSeed() *SeedConfig {
	active := true
	_ = active
	return &SeedConfig{
		Bank: BankSeed{
			BankID: "microagent2",
			Config: map[string]interface{}{
				"retain_mission":      "seed_retain",
				"observations_mission": "seed_obs",
				"enable_observations": true,
			},
		},
		Directives: []DirectiveSeed{
			{Name: "d1", Content: "c1", Priority: 90, Tags: []string{"preferences"}},
			{Name: "d2", Content: "c2", Priority: 80},
		},
	}
}

func TestSyncerCreatesBankWhenAbsent(t *testing.T) {
	br := newBankRecorder(t)
	hc := hindsight.New(br.URL, "")
	s := NewSyncer(hc, "microagent2", discardLogger())
	ctx := context.Background()

	if err := s.Apply(ctx, makeSeed()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if br.createBankHits.Load() != 1 {
		t.Fatalf("createBankHits = %d", br.createBankHits.Load())
	}
	if br.patchCalls.Load() != 1 {
		t.Fatalf("patchCalls = %d", br.patchCalls.Load())
	}
	if br.createDirHits.Load() != 2 {
		t.Fatalf("createDirHits = %d", br.createDirHits.Load())
	}
}

func TestSyncerIdempotentOnRepeatedApply(t *testing.T) {
	br := newBankRecorder(t)
	hc := hindsight.New(br.URL, "")
	s := NewSyncer(hc, "microagent2", discardLogger())
	ctx := context.Background()

	if err := s.Apply(ctx, makeSeed()); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Reset counters after first apply
	createBefore := br.createBankHits.Load()
	patchBefore := br.patchCalls.Load()
	createDirBefore := br.createDirHits.Load()

	if err := s.Apply(ctx, makeSeed()); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if br.createBankHits.Load() != createBefore {
		t.Fatal("createBank called again on idempotent apply")
	}
	if br.patchCalls.Load() != patchBefore {
		t.Fatal("patch called again with no changes")
	}
	if br.createDirHits.Load() != createDirBefore {
		t.Fatal("directive create called again on idempotent apply")
	}
}

func TestSyncerUpdatesDirectiveWhenContentChanged(t *testing.T) {
	br := newBankRecorder(t)
	hc := hindsight.New(br.URL, "")
	s := NewSyncer(hc, "microagent2", discardLogger())
	ctx := context.Background()

	_ = s.Apply(ctx, makeSeed())
	// Now the seed changes content of d1
	seed := makeSeed()
	seed.Directives[0].Content = "updated-c1"
	if err := s.Apply(ctx, seed); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if br.updateDirHits.Load() != 1 {
		t.Fatalf("updateDirHits = %d", br.updateDirHits.Load())
	}
	if br.currentDirs[0].Content != "updated-c1" {
		t.Fatalf("content: %q", br.currentDirs[0].Content)
	}
}

func TestSyncerOnlyPatchesChangedFields(t *testing.T) {
	br := newBankRecorder(t)
	hc := hindsight.New(br.URL, "")
	s := NewSyncer(hc, "microagent2", discardLogger())
	ctx := context.Background()

	_ = s.Apply(ctx, makeSeed())

	// Second apply with a single field changed.
	seed := makeSeed()
	seed.Bank.Config["retain_mission"] = "new_retain"
	if err := s.Apply(ctx, seed); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(br.lastPatch.Updates) != 1 {
		t.Fatalf("want 1 field patched; got %+v", br.lastPatch.Updates)
	}
	if br.lastPatch.Updates["retain_mission"] != "new_retain" {
		t.Fatalf("updates: %+v", br.lastPatch.Updates)
	}
}
