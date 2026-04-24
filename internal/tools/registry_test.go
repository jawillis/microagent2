package tools

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"microagent2/internal/messaging"
)

type fakeTool struct {
	name     string
	desc     string
	invoke   func(ctx context.Context, args string) (string, error)
	panicMsg string
}

func (f *fakeTool) Name() string { return f.name }
func (f *fakeTool) Schema() messaging.ToolSchema {
	return messaging.ToolSchema{Type: "function", Function: messaging.ToolFunction{Name: f.name, Description: f.desc}}
}
func (f *fakeTool) Invoke(ctx context.Context, args string) (string, error) {
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	if f.invoke != nil {
		return f.invoke(ctx, args)
	}
	return "ok", nil
}

func silent() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestRegistry_RejectsMCPPrefixOnRegister(t *testing.T) {
	r := NewRegistry(silent())
	err := r.Register(&fakeTool{name: "mcp__foo__bar"})
	if err == nil {
		t.Fatal("expected error on mcp__ prefix via Register")
	}
}

func TestRegistry_RegisterMCPRequiresPrefix(t *testing.T) {
	r := NewRegistry(silent())
	if err := r.RegisterMCP(&fakeTool{name: "plain"}); err == nil {
		t.Fatal("expected error on non-mcp__ name via RegisterMCP")
	}
	if err := r.RegisterMCP(&fakeTool{name: "mcp__foo__bar"}); err != nil {
		t.Fatalf("RegisterMCP with proper prefix: %v", err)
	}
}

func TestRegistry_DuplicateRegisterErrors(t *testing.T) {
	r := NewRegistry(silent())
	if err := r.Register(&fakeTool{name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&fakeTool{name: "a"}); err == nil {
		t.Fatal("expected duplicate to error")
	}
}

func TestRegistry_SchemasInsertionOrder(t *testing.T) {
	r := NewRegistry(silent())
	for _, n := range []string{"b", "a", "c"} {
		_ = r.Register(&fakeTool{name: n})
	}
	got := r.Schemas()
	for i, want := range []string{"b", "a", "c"} {
		if got[i].Function.Name != want {
			t.Fatalf("order[%d] = %q want %q", i, got[i].Function.Name, want)
		}
	}
}

func TestRegistry_UnknownTool(t *testing.T) {
	r := NewRegistry(silent())
	res, err := r.Invoke(context.Background(), "nope", "{}")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Output != `{"error":"unknown tool: nope"}` {
		t.Fatalf("output: %q", res.Output)
	}
	if res.Outcome != "error" {
		t.Fatalf("outcome: %q", res.Outcome)
	}
}

func TestRegistry_ToolError(t *testing.T) {
	r := NewRegistry(silent())
	_ = r.Register(&fakeTool{name: "x", invoke: func(context.Context, string) (string, error) { return "", errors.New("boom") }})
	res, err := r.Invoke(context.Background(), "x", "{}")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Output != `{"error":"boom"}` {
		t.Fatalf("output: %q", res.Output)
	}
	if res.Outcome != "error" {
		t.Fatalf("outcome: %q", res.Outcome)
	}
}

func TestRegistry_Panic(t *testing.T) {
	r := NewRegistry(silent())
	_ = r.Register(&fakeTool{name: "p", panicMsg: "oops"})
	res, err := r.Invoke(context.Background(), "p", "{}")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Output != `{"error":"tool panicked"}` {
		t.Fatalf("output: %q", res.Output)
	}
	if res.Outcome != "panic" {
		t.Fatalf("outcome: %q", res.Outcome)
	}
}

func TestRegistry_SuccessfulInvocation(t *testing.T) {
	r := NewRegistry(silent())
	_ = r.Register(&fakeTool{name: "ok", invoke: func(_ context.Context, args string) (string, error) {
		return "result:" + args, nil
	}})
	res, err := r.Invoke(context.Background(), "ok", `{"x":1}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Output != `result:{"x":1}` {
		t.Fatalf("output: %q", res.Output)
	}
	if res.Outcome != "ok" {
		t.Fatalf("outcome: %q", res.Outcome)
	}
}
