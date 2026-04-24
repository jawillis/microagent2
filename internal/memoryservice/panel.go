package memoryservice

import (
	"microagent2/internal/config"
	"microagent2/internal/dashboard"
)

// BuildPanelDescriptor returns the panel descriptor memory-service
// publishes at registration. It declares a small form for
// microagent2-specific memory configuration followed by an iframe that
// embeds Hindsight's Control Plane.
//
// cpURL is the Hindsight Control Plane URL reachable from the operator's
// browser (not the in-network URL — operator browser → host). Empty URL
// causes the iframe section to omit its URL, which the dashboard will
// surface as a broken iframe; the caller's responsibility to set
// MEMORY_SERVICE_CP_URL.
//
// bankID is surfaced in a readonly form field so operators can confirm
// which bank memory-service is talking to without leaving the dashboard.
func BuildPanelDescriptor(cpURL, bankID, statusURL string) *dashboard.PanelDescriptor {
	order := 200
	return &dashboard.PanelDescriptor{
		Version: dashboard.CurrentDescriptorVersion,
		Title:   "Memory",
		Order:   &order,
		Sections: []dashboard.Section{
			{
				Kind: dashboard.KindForm,
				Form: &dashboard.FormSection{
					Title:     "Memory Configuration",
					ConfigKey: "memory",
					Fields: map[string]dashboard.FieldSchema{
						"recall_limit": {
							Type:        dashboard.FieldInteger,
							Label:       "Recall Limit",
							Description: "Max memories returned per /recall call.",
							Min:         f64ptr(1),
							Default:     5,
						},
						"prewarm_limit": {
							Type:        dashboard.FieldInteger,
							Label:       "Prewarm Limit",
							Description: "Max memories fetched in the background after each turn to keep caches warm.",
							Min:         f64ptr(0),
							Default:     3,
						},
						"recall_default_types": {
							Type:        dashboard.FieldEnum,
							Label:       "Recall Default Types",
							Description: "Which memory layer /recall returns when the caller doesn't specify. Observation is the synthesis layer; world/experience are raw facts.",
							Values:      config.ValidRecallTypes(),
							Default:     config.DefaultRecallTypes,
						},
						"default_provenance": {
							Type:        dashboard.FieldEnum,
							Label:       "Default Provenance",
							Description: "Provenance applied to /retain calls that don't set metadata.provenance.",
							Values:      config.ValidProvenance(),
							Default:     config.DefaultProvenance,
						},
						"tag_taxonomy": {
							Type:        dashboard.FieldString,
							Label:       "Tag Taxonomy",
							Description: "Comma-separated list of well-known tags the extractor is encouraged to use. Informational for now.",
							Default:     config.DefaultTagTaxonomy,
						},
						"primary_user_id": {
							Type:        dashboard.FieldString,
							Label:       "Primary User ID",
							Description: "Fallback speaker for anonymous requests. Leave empty for multi-user deployments.",
						},
						"recall_default_speaker_scope": {
							Type:        dashboard.FieldEnum,
							Label:       "Recall Speaker Scope",
							Description: "How recall treats missing speaker_id filter. 'any' = no implicit scope, 'primary' = restrict to primary_user_id, 'explicit' = require caller to specify.",
							Values:      config.ValidRecallSpeakerScope(),
							Default:     config.DefaultRecallSpeakerScope,
						},
						"identity_name_denylist": {
							Type:        dashboard.FieldString,
							Label:       "Identity Name Denylist",
							Description: "Comma-separated names that trigger a startup warning if found in missions/directives. For catching hard-coded person references.",
						},
						"memory_bank_id": {
							Type:        dashboard.FieldString,
							Label:       "Memory Bank ID",
							Description: "The Hindsight bank this service is configured for. Set via MEMORY_BANK_ID env; restart to change.",
							Default:     bankID,
							Readonly:    true,
						},
					},
				},
			},
			{
				Kind: dashboard.KindStatus,
				Status: &dashboard.StatusSection{
					Title:  "Identity Stats",
					URL:    statusURL,
					Layout: dashboard.StatusKeyValue,
				},
			},
			{
				Kind: dashboard.KindIframe,
				Iframe: &dashboard.IframeSection{
					Title:  "Hindsight Control Plane",
					URL:    cpURL,
					Height: "800px",
				},
			},
		},
	}
}

func f64ptr(v float64) *float64 { return &v }
