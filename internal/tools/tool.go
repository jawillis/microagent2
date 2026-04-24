package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"microagent2/internal/messaging"
)

const mcpPrefix = "mcp__"

type Tool interface {
	Name() string
	Schema() messaging.ToolSchema
	Invoke(ctx context.Context, argsJSON string) (string, error)
}

type ManifestEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type InvocationResult struct {
	Output     string
	Outcome    string // "ok" | "error" | "panic"
	ElapsedMS  int64
	ResultSize int
}

type Registry struct {
	tools  map[string]Tool
	order  []string
	logger *slog.Logger
}

func NewRegistry(logger *slog.Logger) *Registry {
	return &Registry{tools: map[string]Tool{}, logger: logger}
}

// Register adds a built-in tool. Names starting with "mcp__" are reserved for
// MCP-sourced tools; attempting to register a built-in under that namespace
// returns an error. Use RegisterMCP for MCP-sourced tools.
func (r *Registry) Register(t Tool) error {
	name := t.Name()
	if strings.HasPrefix(name, mcpPrefix) {
		return fmt.Errorf("tool name %q uses reserved mcp__ prefix; use RegisterMCP", name)
	}
	return r.register(t)
}

// RegisterMCP adds an MCP-sourced tool. The tool's Name must start with
// "mcp__<server>__". Collisions and malformed names are still rejected.
func (r *Registry) RegisterMCP(t Tool) error {
	name := t.Name()
	if !strings.HasPrefix(name, mcpPrefix) {
		return fmt.Errorf("MCP tool name %q must start with mcp__", name)
	}
	return r.register(t)
}

func (r *Registry) register(t Tool) error {
	name := t.Name()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool already registered: %s", name)
	}
	r.tools[name] = t
	r.order = append(r.order, name)
	return nil
}

// Schemas returns every registered tool's schema in registration order.
// Callers that want to filter by active skill should prefer SchemasFor.
func (r *Registry) Schemas() []messaging.ToolSchema {
	out := make([]messaging.ToolSchema, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name].Schema())
	}
	return out
}

// SchemasFor returns schemas for the union of `base` and `allowed` — the
// set of tool names visible under the current active skill. Names not
// registered are silently ignored; the returned slice preserves registration
// order (not argument order). Deduplication happens via set membership.
func (r *Registry) SchemasFor(base []string, allowed []string) []messaging.ToolSchema {
	visible := make(map[string]struct{}, len(base)+len(allowed))
	for _, n := range base {
		visible[n] = struct{}{}
	}
	for _, n := range allowed {
		visible[n] = struct{}{}
	}
	out := make([]messaging.ToolSchema, 0, len(visible))
	for _, name := range r.order {
		if _, ok := visible[name]; !ok {
			continue
		}
		out = append(out, r.tools[name].Schema())
	}
	return out
}

// Has reports whether a tool with the given name is registered. Callers
// use this to pre-validate a visible set before calling Invoke.
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

func (r *Registry) Manifest() []ManifestEntry {
	out := make([]ManifestEntry, 0, len(r.order))
	for _, name := range r.order {
		s := r.tools[name].Schema()
		out = append(out, ManifestEntry{Name: s.Function.Name, Description: s.Function.Description})
	}
	return out
}

// Invoke resolves name to a registered tool and runs it. Panics are recovered.
// Errors (including unknown-tool and panic) surface as JSON-encoded error
// strings in the returned output, with a nil error — callers feed the result
// straight to the model as a tool_result.
func (r *Registry) Invoke(ctx context.Context, name, argsJSON string) (InvocationResult, error) {
	start := time.Now()
	t, ok := r.tools[name]
	if !ok {
		return InvocationResult{Output: jsonError(fmt.Sprintf("unknown tool: %s", name)), Outcome: "error", ElapsedMS: time.Since(start).Milliseconds()}, nil
	}

	var out string
	var invokeErr error
	outcome := "ok"
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				if r.logger != nil {
					r.logger.Error("tool_panic", "tool_name", name, "panic", fmt.Sprint(rec))
				}
				out = jsonError("tool panicked")
				outcome = "panic"
				invokeErr = nil
			}
		}()
		out, invokeErr = t.Invoke(ctx, argsJSON)
	}()
	if invokeErr != nil {
		out = jsonError(invokeErr.Error())
		outcome = "error"
	}
	return InvocationResult{Output: out, Outcome: outcome, ElapsedMS: time.Since(start).Milliseconds(), ResultSize: len(out)}, nil
}

func jsonError(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}
