package llmproxy

import (
	"microagent2/internal/dashboard"
)

// BuildPanelDescriptor returns llm-proxy's dashboard panel: one form
// section for its two timeout knobs plus a readonly identity field.
func BuildPanelDescriptor(identity string) *dashboard.PanelDescriptor {
	order := 310
	return &dashboard.PanelDescriptor{
		Version: dashboard.CurrentDescriptorVersion,
		Title:   "LLM Proxy",
		Order:   &order,
		Sections: []dashboard.Section{
			{
				Kind: dashboard.KindForm,
				Form: &dashboard.FormSection{
					Title:     "LLM Proxy Configuration",
					ConfigKey: "llm_proxy",
					Fields: map[string]dashboard.FieldSchema{
						"slot_timeout_ms": {
							Type:        dashboard.FieldInteger,
							Label:       "Slot Acquire Timeout (ms)",
							Description: "How long to wait for a hindsight-class slot before returning 503.",
							Min:         f64ptr(100),
							Default:     10000,
						},
						"request_timeout_ms": {
							Type:        dashboard.FieldInteger,
							Label:       "Upstream Request Timeout (ms)",
							Description: "Total deadline for the upstream chat completion.",
							Min:         f64ptr(1000),
							Default:     300000,
						},
						"identity": {
							Type:        dashboard.FieldString,
							Label:       "Proxy Identity",
							Description: "The agent_id used for broker ownership validation. Env-only.",
							Default:     identity,
							Readonly:    true,
						},
					},
				},
			},
		},
	}
}

func f64ptr(v float64) *float64 { return &v }
