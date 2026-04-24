package broker

import (
	"microagent2/internal/dashboard"
)

// BuildPanelDescriptor returns the llm-broker dashboard panel. Two
// sections: a form for slot budget + timeouts (config_key "broker"),
// and a status table showing the live slot snapshot fetched from the
// gateway's /v1/broker/slots endpoint.
func BuildPanelDescriptor() *dashboard.PanelDescriptor {
	order := 300
	return &dashboard.PanelDescriptor{
		Version: dashboard.CurrentDescriptorVersion,
		Title:   "Broker",
		Order:   &order,
		Sections: []dashboard.Section{
			{
				Kind: dashboard.KindForm,
				Form: &dashboard.FormSection{
					Title:     "Broker Configuration",
					ConfigKey: "broker",
					Fields: map[string]dashboard.FieldSchema{
						"slot_count": {
							Type:        dashboard.FieldInteger,
							Label:       "Total Slot Count",
							Description: "Must equal llama-server's configured slot count. Requires broker restart to apply.",
							Min:         f64ptr(1),
						},
						"preempt_timeout_ms": {
							Type:        dashboard.FieldInteger,
							Label:       "Preempt Timeout (ms)",
							Description: "How long the broker waits for a preempted agent to release its slot before force-releasing. Hot-reloads.",
							Min:         f64ptr(0),
						},
					},
				},
			},
			{
				Kind: dashboard.KindStatus,
				Status: &dashboard.StatusSection{
					Title:  "Slot Table",
					URL:    "/v1/broker/slots",
					Layout: dashboard.StatusTable,
				},
			},
		},
	}
}

func f64ptr(v float64) *float64 { return &v }
