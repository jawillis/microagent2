package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"microagent2/internal/execclient"
	"microagent2/internal/messaging"
)

type runSkillScriptTool struct {
	client *execclient.Client
	logger *slog.Logger
}

// NewRunSkillScript constructs the built-in tool that forwards to the exec
// service's /v1/run endpoint. session_id is expected to be pre-injected into
// argsJSON by main-agent before Invoke is called (see cmd/main-agent/gating.go).
func NewRunSkillScript(client *execclient.Client, logger *slog.Logger) Tool {
	return &runSkillScriptTool{client: client, logger: logger}
}

func (t *runSkillScriptTool) Name() string { return "run_skill_script" }

func (t *runSkillScriptTool) Schema() messaging.ToolSchema {
	params := json.RawMessage(`{
		"type":"object",
		"properties":{
			"skill":{"type":"string","description":"Exact skill name as returned by list_skills"},
			"script":{"type":"string","description":"Relative path within the skill directory (e.g. scripts/hello.py)"},
			"args":{"type":"array","items":{"type":"string"},"description":"Optional argv for the script"},
			"stdin":{"type":"string","description":"Optional string piped to the script's stdin"},
			"timeout_s":{"type":"integer","description":"Optional per-invocation deadline in seconds; capped server-side"}
		},
		"required":["skill","script"]
	}`)
	return messaging.ToolSchema{
		Type: "function",
		Function: messaging.ToolFunction{
			Name:        "run_skill_script",
			Description: "Execute a bundled script inside a skill's scripts/ directory via the sandboxed exec service. Returns a JSON envelope with exit_code, stdout, stderr, workspace_dir, outputs, duration_ms, timed_out. First invocation for a skill may include dependency install latency; subsequent calls reuse the cached venv.",
			Parameters:  params,
		},
	}
}

func (t *runSkillScriptTool) Invoke(ctx context.Context, argsJSON string) (string, error) {
	var args execclient.RunRequest
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError(fmt.Sprintf("invalid arguments: %s", err.Error())), nil
	}
	args.Skill = strings.TrimSpace(args.Skill)
	args.Script = strings.TrimSpace(args.Script)
	if args.Skill == "" || args.Script == "" {
		return jsonError("skill and script arguments are required"), nil
	}

	resp, err := t.client.Run(ctx, &args)
	if err != nil {
		return jsonError(classifyClientError(err)), nil
	}

	// Return the envelope verbatim as a JSON string.
	b, mErr := json.Marshal(resp)
	if mErr != nil {
		// Should never happen with a well-formed RunResponse.
		return jsonError(fmt.Sprintf("envelope marshal: %s", mErr.Error())), nil
	}
	return string(b), nil
}

// classifyClientError turns a client error into a stable human-friendly
// message for the tool envelope. Preserves detail while normalizing the
// prefix so the model can reason about failure modes consistently.
func classifyClientError(err error) string {
	if err == nil {
		return ""
	}
	// Non-2xx responses surface status + body via execclient.Error.
	var ee *execclient.Error
	if errors.As(err, &ee) {
		return fmt.Sprintf("exec returned %d: %s", ee.StatusCode, ee.Body)
	}
	// Context deadline or cancellation.
	if errors.Is(err, context.DeadlineExceeded) {
		return "exec request failed: deadline exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "exec request failed: canceled"
	}
	// Network-level failures (connection refused, DNS, TLS, etc.).
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "exec unavailable: " + err.Error()
	}
	// Fallback: unclassified errors still surface as exec-unavailable so
	// the model has a consistent "service issue" signal.
	return "exec unavailable: " + err.Error()
}
