package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"microagent2/internal/messaging"
)

type currentTimeTool struct {
	now func() time.Time // injectable for tests
}

// NewCurrentTime returns a built-in tool that reports the current UTC time.
func NewCurrentTime() Tool { return &currentTimeTool{now: time.Now} }

func (t *currentTimeTool) Name() string { return "current_time" }

func (t *currentTimeTool) Schema() messaging.ToolSchema {
	params := json.RawMessage(`{"type":"object","properties":{"format":{"type":"string","description":"Optional Go time layout (e.g. 2006-01-02); defaults to RFC3339."}}}`)
	return messaging.ToolSchema{
		Type: "function",
		Function: messaging.ToolFunction{
			Name:        "current_time",
			Description: "Return the current UTC time. Optional 'format' argument accepts a Go time layout string (e.g. '2006-01-02 15:04:05'); default is RFC3339.",
			Parameters:  params,
		},
	}
}

func (t *currentTimeTool) Invoke(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Format string `json:"format"`
	}
	// Empty args is common and valid; treat as {}.
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed != "" && trimmed != "{}" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return jsonError(fmt.Sprintf("invalid arguments: %s", err.Error())), nil
		}
	}
	layout := strings.TrimSpace(args.Format)
	if layout == "" {
		layout = time.RFC3339
	}
	return t.now().UTC().Format(layout), nil
}
