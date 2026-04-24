package retro

import (
	"microagent2/internal/dashboard"
)

// BuildPanelDescriptor returns retro-agent's dashboard panel. Two
// sections: form for retro policy (config_key "retro"), action for
// manually triggering memory_extraction / skill_creation / curation
// jobs against a session ID.
func BuildPanelDescriptor() *dashboard.PanelDescriptor {
	order := 320
	sessionParam := []dashboard.ActionParam{
		{
			Name:     "session_id",
			Type:     dashboard.FieldString,
			Required: true,
			Label:    "Session ID",
		},
	}
	return &dashboard.PanelDescriptor{
		Version: dashboard.CurrentDescriptorVersion,
		Title:   "Retro",
		Order:   &order,
		Sections: []dashboard.Section{
			{
				Kind: dashboard.KindForm,
				Form: &dashboard.FormSection{
					Title:     "Retro Policy",
					ConfigKey: "retro",
					Fields: map[string]dashboard.FieldSchema{
						"inactivity_timeout_s": {
							Type:        dashboard.FieldInteger,
							Label:       "Inactivity Timeout (s)",
							Description: "How long a session is silent before retro fires on it.",
							Min:         f64ptr(10),
							Default:     300,
						},
						"skill_dup_threshold": {
							Type:        dashboard.FieldNumber,
							Label:       "Skill Duplicate Threshold",
							Description: "Jaccard similarity above which a newly extracted skill is treated as a duplicate.",
							Min:         f64ptr(0),
							Max:         f64ptr(1),
							Step:        f64ptr(0.01),
							Default:     0.85,
						},
						"min_history_turns": {
							Type:        dashboard.FieldInteger,
							Label:       "Min History Turns",
							Description: "Minimum turn count before skill-creation runs on a session.",
							Min:         f64ptr(1),
							Default:     4,
						},
						"curation_recall_limit": {
							Type:        dashboard.FieldInteger,
							Label:       "Curation Recall Limit",
							Description: "Per-category recall limit during curation pass.",
							Min:         f64ptr(1),
							Default:     15,
						},
					},
				},
			},
			{
				Kind: dashboard.KindAction,
				Action: &dashboard.ActionSection{
					Title: "Manual Triggers",
					Actions: []dashboard.Action{
						{
							Label:  "Run Memory Extraction",
							URL:    "/v1/retro/{session_id}/trigger",
							Method: "POST",
							Body:   map[string]any{"job_type": "memory_extraction"},
							Params: sessionParam,
						},
						{
							Label:  "Run Skill Creation",
							URL:    "/v1/retro/{session_id}/trigger",
							Method: "POST",
							Body:   map[string]any{"job_type": "skill_creation"},
							Params: sessionParam,
						},
						{
							Label:  "Run Curation",
							URL:    "/v1/retro/{session_id}/trigger",
							Method: "POST",
							Body:   map[string]any{"job_type": "curation"},
							Params: sessionParam,
						},
					},
				},
			},
		},
	}
}

func f64ptr(v float64) *float64 { return &v }
