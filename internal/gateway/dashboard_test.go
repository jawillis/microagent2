package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"microagent2/internal/dashboard"
	"microagent2/internal/registry"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAggregatePanels_BuiltinsOnly(t *testing.T) {
	reg := registry.NewRegistry()
	entries := aggregatePanels(reg)
	// expect 3 built-ins in explicit order: chat (10), sessions (80), system (90)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	wantTitles := []string{"Chat", "Sessions", "System"}
	for i, want := range wantTitles {
		if entries[i].Descriptor.Title != want {
			t.Errorf("entry[%d] title = %q; want %q", i, entries[i].Descriptor.Title, want)
		}
	}
}

func TestAggregatePanels_ServiceContributedSortsAfterBuiltins(t *testing.T) {
	reg := registry.NewRegistry()
	order := 200
	reg.Register(&registry.AgentInfo{
		AgentID: "memory-service",
		DashboardPanel: &dashboard.PanelDescriptor{
			Version: 1, Title: "Memory", Order: &order,
			Sections: []dashboard.Section{{
				Kind: dashboard.KindIframe,
				Iframe: &dashboard.IframeSection{Title: "CP", URL: "http://x"},
			}},
		},
	})
	entries := aggregatePanels(reg)
	if len(entries) != 4 {
		t.Fatalf("got %d, want 4", len(entries))
	}
	// Memory (order 200) must sort AFTER Chat(10)/Sessions(80)/System(90)
	if entries[3].Descriptor.Title != "Memory" {
		t.Fatalf("last entry title = %q; want Memory", entries[3].Descriptor.Title)
	}
}

func TestAggregatePanels_ServiceWithLowOrderClamped(t *testing.T) {
	reg := registry.NewRegistry()
	// Service tries to claim order 5 (inside reserved range)
	order := 5
	reg.Register(&registry.AgentInfo{
		AgentID: "sneaky-service",
		DashboardPanel: &dashboard.PanelDescriptor{
			Version: 1, Title: "Sneaky", Order: &order,
			Sections: []dashboard.Section{{
				Kind: dashboard.KindStatus,
				Status: &dashboard.StatusSection{Title: "S", URL: "/x", Layout: dashboard.StatusKeyValue},
			}},
		},
	})
	entries := aggregatePanels(reg)
	var sneakyOrder int
	for _, e := range entries {
		if e.Descriptor.Title == "Sneaky" {
			sneakyOrder = e.Order
		}
	}
	if sneakyOrder < dashboard.ReservedOrderCeiling {
		t.Fatalf("service order not clamped: %d (ceiling %d)", sneakyOrder, dashboard.ReservedOrderCeiling)
	}
}

func TestAggregatePanels_OmitsServicesWithoutDescriptor(t *testing.T) {
	reg := registry.NewRegistry()
	reg.Register(&registry.AgentInfo{AgentID: "no-panel-service"}) // nil descriptor
	entries := aggregatePanels(reg)
	for _, e := range entries {
		if e.ServiceID == "no-panel-service" {
			t.Fatal("service without descriptor should not appear in aggregation")
		}
	}
}

func TestHandleListDashboardPanels_ReturnsBuiltins(t *testing.T) {
	s := &Server{logger: silentLogger(), registry: registry.NewRegistry()}
	req := httptest.NewRequest("GET", "/v1/dashboard/panels", nil)
	w := httptest.NewRecorder()
	s.handleListDashboardPanels(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	var body struct {
		Panels []struct {
			ServiceID  string                    `json:"service_id"`
			Order      int                       `json:"order"`
			Descriptor dashboard.PanelDescriptor `json:"descriptor"`
		} `json:"panels"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Panels) != 3 {
		t.Fatalf("got %d panels, want 3", len(body.Panels))
	}
	for _, p := range body.Panels {
		if p.ServiceID != "gateway" {
			t.Errorf("built-in service_id = %q; want gateway", p.ServiceID)
		}
	}
}
