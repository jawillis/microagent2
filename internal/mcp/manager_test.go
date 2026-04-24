package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"microagent2/internal/config"
	"microagent2/internal/tools"
)

// buildFakeServer writes a small Go MCP server program to a temp dir,
// compiles it, and returns the binary path. The program handles initialize,
// tools/list (returns one tool named per the SERVER_TOOL_NAME env var or
// defaulting to "echo"), and tools/call (returns {content: [{type:"text",
// text: <arg.msg>}], isError: false}).
func buildFakeServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	source := `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type msg struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id,omitempty\"`" + `
	Method  string          ` + "`json:\"method,omitempty\"`" + `
	Params  json.RawMessage ` + "`json:\"params,omitempty\"`" + `
	Result  json.RawMessage ` + "`json:\"result,omitempty\"`" + `
}

func send(id json.RawMessage, result any) {
	b, _ := json.Marshal(result)
	out := map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(b)}
	j, _ := json.Marshal(out)
	fmt.Println(string(j))
}

func main() {
	toolName := os.Getenv("SERVER_TOOL_NAME")
	if toolName == "" { toolName = "echo" }
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var m msg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil { continue }
		if len(m.ID) == 0 { continue }
		switch m.Method {
		case "initialize":
			send(m.ID, map[string]any{"protocolVersion":"2024-11-05","capabilities":map[string]any{},"serverInfo":map[string]any{"name":"fake"}})
		case "tools/list":
			send(m.ID, map[string]any{"tools":[]map[string]any{{"name":toolName,"description":"echoes","inputSchema":map[string]any{"type":"object"}}}})
		case "tools/call":
			send(m.ID, map[string]any{"content":[]map[string]any{{"type":"text","text":"pong"}},"isError":false})
		default:
			send(m.ID, map[string]any{})
		}
	}
}
`
	if err := os.WriteFile(src, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fake-mcp-server")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build fake server: %v", err)
	}
	return bin
}

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func TestManager_StartRegistersTools(t *testing.T) {
	bin := buildFakeServer(t)
	rdb := testRedis(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	mgr := NewManager(rdb, logger)
	reg := tools.NewRegistry(logger)

	cfgs := []config.MCPServerConfig{
		{Name: "alpha", Enabled: true, Command: bin, Env: map[string]string{"SERVER_TOOL_NAME": "alpha_tool"}},
		{Name: "beta", Enabled: true, Command: bin, Env: map[string]string{"SERVER_TOOL_NAME": "beta_tool"}},
		{Name: "disabled", Enabled: false, Command: bin},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr.Start(ctx, cfgs, reg)
	defer mgr.Close(context.Background())

	schemas := reg.Schemas()
	var names []string
	for _, s := range schemas {
		names = append(names, s.Function.Name)
	}
	wantAlpha := "mcp__alpha__alpha_tool"
	wantBeta := "mcp__beta__beta_tool"
	hasAlpha := false
	hasBeta := false
	for _, n := range names {
		if n == wantAlpha {
			hasAlpha = true
		}
		if n == wantBeta {
			hasBeta = true
		}
	}
	if !hasAlpha || !hasBeta {
		t.Fatalf("expected both tools registered, got %+v", names)
	}

	// Invoke one — should round-trip.
	res, err := reg.Invoke(ctx, wantAlpha, `{}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Output != "pong" {
		t.Fatalf("invoke output: %q", res.Output)
	}

	// Health snapshot.
	raw, err := rdb.Get(ctx, healthKey).Result()
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	var entries []HealthEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("health parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(entries), entries)
	}
	byName := map[string]HealthEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if !byName["alpha"].Connected || byName["alpha"].ToolCount != 1 {
		t.Fatalf("alpha health: %+v", byName["alpha"])
	}
	if byName["disabled"].Connected {
		t.Fatalf("disabled should not be connected: %+v", byName["disabled"])
	}
}

func TestManager_BrokenServerSkippedOthersStillRegister(t *testing.T) {
	bin := buildFakeServer(t)
	rdb := testRedis(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mgr := NewManager(rdb, logger)
	reg := tools.NewRegistry(logger)

	cfgs := []config.MCPServerConfig{
		{Name: "broken", Enabled: true, Command: "/nonexistent/binary/does/not/exist"},
		{Name: "working", Enabled: true, Command: bin, Env: map[string]string{"SERVER_TOOL_NAME": "ok_tool"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr.Start(ctx, cfgs, reg)
	defer mgr.Close(context.Background())

	found := false
	for _, s := range reg.Schemas() {
		if s.Function.Name == "mcp__working__ok_tool" {
			found = true
		}
	}
	if !found {
		t.Fatal("working tool should still be registered when another server breaks")
	}
}

func TestManager_NoServersBehavesCleanly(t *testing.T) {
	rdb := testRedis(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mgr := NewManager(rdb, logger)
	reg := tools.NewRegistry(logger)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mgr.Start(ctx, nil, reg)
	defer mgr.Close(context.Background())

	if len(reg.Schemas()) != 0 {
		t.Fatalf("expected no tools, got %d", len(reg.Schemas()))
	}
}
