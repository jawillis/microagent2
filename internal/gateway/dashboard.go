package gateway

import (
	"encoding/json"
	"net/http"
	"sort"

	"microagent2/internal/dashboard"
	"microagent2/internal/registry"
)

// builtinPanel is a descriptor synthesized by the gateway for its own
// built-in panels (Chat, Sessions, System). It carries an order value
// in the reserved range [0, 100).
type builtinPanel struct {
	ServiceID  string
	Descriptor *dashboard.PanelDescriptor
}

// buildBuiltinPanels returns the gateway's synthesized panel descriptors.
// These fill the reserved order range and are rendered uniformly with
// service-contributed panels.
//
// The Memory and Agents panels are deliberately absent — they are
// contributed by memory-service and llm-broker / llm-proxy / retro-agent
// / main-agent in follow-up changes.
func buildBuiltinPanels() []builtinPanel {
	return []builtinPanel{
		{
			ServiceID:  "gateway",
			Descriptor: chatPanelDescriptor(),
		},
		{
			ServiceID:  "gateway",
			Descriptor: sessionsPanelDescriptor(),
		},
		{
			ServiceID:  "gateway",
			Descriptor: systemPanelDescriptor(),
		},
		{
			ServiceID:  "gateway",
			Descriptor: logsPanelDescriptor(),
		},
	}
}

func logsPanelDescriptor() *dashboard.PanelDescriptor {
	order := 85
	return &dashboard.PanelDescriptor{
		Version: 1,
		Title:   "Logs",
		Order:   &order,
		Sections: []dashboard.Section{
			{
				Kind: dashboard.KindLogs,
				Logs: &dashboard.LogsSection{
					Title:        "Live Logs",
					TailURL:      "/v1/logs/tail",
					HistoryURL:   "/v1/logs/stream",
					ServicesURL:  "/v1/logs/services",
					DefaultLevel: "info",
				},
			},
		},
	}
}

func chatPanelDescriptor() *dashboard.PanelDescriptor {
	order := 10
	return &dashboard.PanelDescriptor{
		Version: 1,
		Title:   "Chat",
		Order:   &order,
		Sections: []dashboard.Section{
			{
				Kind: dashboard.KindForm,
				Form: &dashboard.FormSection{
					Title:     "Chat Configuration",
					ConfigKey: "chat",
					Fields: map[string]dashboard.FieldSchema{
						"system_prompt": {
							Type:  dashboard.FieldTextarea,
							Label: "System Prompt",
						},
						"model": {
							Type:  dashboard.FieldString,
							Label: "Model",
						},
						"request_timeout_s": {
							Type:  dashboard.FieldInteger,
							Label: "Request Timeout (seconds)",
							Min:   f64ptr(1),
						},
					},
				},
			},
		},
	}
}

func sessionsPanelDescriptor() *dashboard.PanelDescriptor {
	order := 80
	return &dashboard.PanelDescriptor{
		Version: 1,
		Title:   "Sessions",
		Order:   &order,
		Sections: []dashboard.Section{
			{
				Kind: dashboard.KindStatus,
				Status: &dashboard.StatusSection{
					Title:  "Sessions",
					URL:    "/v1/sessions",
					Layout: dashboard.StatusTable,
				},
			},
		},
	}
}

func systemPanelDescriptor() *dashboard.PanelDescriptor {
	order := 90
	return &dashboard.PanelDescriptor{
		Version: 1,
		Title:   "System",
		Order:   &order,
		Sections: []dashboard.Section{
			{
				Kind: dashboard.KindStatus,
				Status: &dashboard.StatusSection{
					Title:  "System Status",
					URL:    "/v1/status",
					Layout: dashboard.StatusKeyValue,
				},
			},
		},
	}
}

func f64ptr(v float64) *float64 { return &v }

// panelEntry is one row in the aggregated dashboard response.
type panelEntry struct {
	ServiceID        string                     `json:"service_id"`
	Order            int                        `json:"order"`
	Descriptor       *dashboard.PanelDescriptor `json:"descriptor"`
	explicitOrder    bool                       // internal — sort tie-break
}

// aggregatePanels returns the dashboard panel list ordered by explicit
// order (ascending), then by service ID alphabetically for descriptors
// without an explicit order. Built-in panels and service-contributed
// panels are merged into a single list.
//
// The gateway built-ins are always included; service-contributed
// descriptors are included only when the service is alive in the
// registry. Invalid descriptors never reach the registry (they are
// rejected by the consumer), so no additional validation is needed here.
func aggregatePanels(reg *registry.Registry) []panelEntry {
	entries := make([]panelEntry, 0, 8)

	// Gateway built-ins.
	for _, bi := range buildBuiltinPanels() {
		ord, explicit := dashboard.ClampOrder(bi.Descriptor, false /* not service-contributed */)
		entries = append(entries, panelEntry{
			ServiceID:     bi.ServiceID,
			Order:         ord,
			Descriptor:    bi.Descriptor,
			explicitOrder: explicit,
		})
	}

	// Service-contributed panels (alive only).
	for _, agent := range reg.ListAlive() {
		if agent.DashboardPanel == nil {
			continue
		}
		ord, explicit := dashboard.ClampOrder(agent.DashboardPanel, true /* service-contributed, clamps to >=100 */)
		entries = append(entries, panelEntry{
			ServiceID:     agent.AgentID,
			Order:         ord,
			Descriptor:    agent.DashboardPanel,
			explicitOrder: explicit,
		})
	}

	// Stable sort: explicit-ordered first (by order), then non-ordered (by service ID).
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.explicitOrder != b.explicitOrder {
			// Explicit-ordered entries sort before non-explicit ones.
			return a.explicitOrder
		}
		if a.explicitOrder {
			if a.Order != b.Order {
				return a.Order < b.Order
			}
			return a.ServiceID < b.ServiceID
		}
		return a.ServiceID < b.ServiceID
	})
	return entries
}

// handleListDashboardPanels implements GET /v1/dashboard/panels.
func (s *Server) handleListDashboardPanels(w http.ResponseWriter, r *http.Request) {
	entries := aggregatePanels(s.registry)
	// Shape the response: caller sees panel descriptors with the service_id
	// and resolved order exposed for diagnostics / UI debug.
	type outEntry struct {
		ServiceID  string                     `json:"service_id"`
		Order      int                        `json:"order"`
		Descriptor *dashboard.PanelDescriptor `json:"descriptor"`
	}
	out := make([]outEntry, len(entries))
	for i, e := range entries {
		out[i] = outEntry{ServiceID: e.ServiceID, Order: e.Order, Descriptor: e.Descriptor}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"panels": out})
	s.logger.Debug("dashboard_panels_aggregated", "count", len(out))
}
