package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"microagent2/internal/messaging"
)

type mcpTool struct {
	serverName   string
	toolName     string
	description  string
	inputSchema  json.RawMessage
	client       *Client
}

// NewTool wraps a discovered MCP tool so it implements tools.Tool.
func NewTool(serverName string, tool MCPTool, client *Client) *mcpTool {
	return &mcpTool{
		serverName:  serverName,
		toolName:    tool.Name,
		description: tool.Description,
		inputSchema: tool.InputSchema,
		client:      client,
	}
}

func (t *mcpTool) Name() string { return "mcp__" + t.serverName + "__" + t.toolName }

func (t *mcpTool) Schema() messaging.ToolSchema {
	params := t.inputSchema
	if len(params) == 0 {
		params = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return messaging.ToolSchema{
		Type: "function",
		Function: messaging.ToolFunction{
			Name:        t.Name(),
			Description: t.description,
			Parameters:  params,
		},
	}
}

func (t *mcpTool) Invoke(ctx context.Context, argsJSON string) (string, error) {
	if t.client.Disconnected() {
		b, _ := json.Marshal(map[string]string{"error": fmt.Sprintf("mcp server %s disconnected", t.serverName)})
		return string(b), nil
	}
	out, err := t.client.CallTool(ctx, t.toolName, argsJSON)
	if err != nil {
		return "", err
	}
	return out, nil
}
