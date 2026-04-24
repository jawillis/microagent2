package agent

import (
	"microagent2/internal/dashboard"
)

// BuildMCPPanelDescriptor returns main-agent's MCP panel. It shows the
// current MCP servers, exposes add/remove actions, and a small form
// for the MCP invoke timeout.
func BuildMCPPanelDescriptor() *dashboard.PanelDescriptor {
	order := 330
	return &dashboard.PanelDescriptor{
		Version: dashboard.CurrentDescriptorVersion,
		Title:   "MCP",
		Order:   &order,
		Sections: []dashboard.Section{
			{
				Kind: dashboard.KindStatus,
				Status: &dashboard.StatusSection{
					Title:  "Registered MCP Servers",
					URL:    "/v1/mcp/servers",
					Layout: dashboard.StatusTable,
				},
			},
			{
				Kind: dashboard.KindAction,
				Action: &dashboard.ActionSection{
					Title: "MCP Server Management",
					Actions: []dashboard.Action{
						{
							Label:  "Add server",
							URL:    "/v1/mcp/servers",
							Method: "POST",
							Params: []dashboard.ActionParam{
								{Name: "name", Type: dashboard.FieldString, Required: true, Label: "Name"},
								{Name: "command", Type: dashboard.FieldString, Required: true, Label: "Command"},
								{Name: "args", Type: dashboard.FieldString, Label: "Args (space-separated)"},
								{Name: "env", Type: dashboard.FieldTextarea, Label: "Env (KEY=VALUE per line)"},
								{Name: "enabled", Type: dashboard.FieldBoolean, Default: true, Label: "Enabled"},
							},
						},
						{
							Label:   "Remove server",
							URL:     "/v1/mcp/servers/{name}",
							Method:  "DELETE",
							Confirm: "Remove this MCP server? Active connections will be closed.",
							Params: []dashboard.ActionParam{
								{Name: "name", Type: dashboard.FieldString, Required: true, Label: "Name"},
							},
						},
					},
				},
			},
		},
	}
}
