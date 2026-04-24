package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"microagent2/internal/execclient"
	"microagent2/internal/messaging"
)

type bashTool struct {
	client *execclient.Client
	logger *slog.Logger
}

// NewBash constructs the built-in tool that forwards arbitrary shell
// commands to the exec service's /v1/bash endpoint. session_id is expected
// to be pre-injected into argsJSON by main-agent before Invoke is called
// (see injectSessionID in cmd/main-agent/main.go).
func NewBash(client *execclient.Client, logger *slog.Logger) Tool {
	return &bashTool{client: client, logger: logger}
}

func (t *bashTool) Name() string { return "bash" }

func (t *bashTool) Schema() messaging.ToolSchema {
	params := json.RawMessage(`{
		"type":"object",
		"properties":{
			"command":{"type":"string","description":"Shell command to run via sh -c"},
			"timeout_s":{"type":"integer","description":"Per-command deadline in seconds; capped server-side"}
		},
		"required":["command"]
	}`)
	return messaging.ToolSchema{
		Type: "function",
		Function: messaging.ToolFunction{
			Name:        "bash",
			Description: "Run a shell command in a per-session sandbox directory. Files persist across calls within the same session until 60 minutes of inactivity, then are reclaimed. Commands run under 'sh -c' (not bash). The /skills directory is NOT accessible from bash; use read_skill_file for that. Network access follows operator policy. Use this to draft and iterate on new skills; finished artifacts live in the sandbox until an operator copies them to /skills.",
			Parameters:  params,
		},
	}
}

func (t *bashTool) Invoke(ctx context.Context, argsJSON string) (string, error) {
	var args execclient.BashRequest
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError(fmt.Sprintf("invalid arguments: %s", err.Error())), nil
	}
	args.Command = strings.TrimSpace(args.Command)
	if args.Command == "" {
		return jsonError("command argument is required"), nil
	}

	resp, err := t.client.Bash(ctx, &args)
	if err != nil {
		return jsonError(classifyClientError(err)), nil
	}

	b, mErr := json.Marshal(resp)
	if mErr != nil {
		return jsonError(fmt.Sprintf("envelope marshal: %s", mErr.Error())), nil
	}
	return string(b), nil
}
