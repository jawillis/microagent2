package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"microagent2/internal/agent"
	"microagent2/internal/messaging"
	"microagent2/internal/sessionskill"
	"microagent2/internal/skills"
	"microagent2/internal/tools"
)

// baseToolNames collects all registered tool names — the "base toolset" as
// main.go computes it after MCP start. Shared by multiple gating tests.
func baseToolNames(r *tools.Registry) []string {
	out := make([]string, 0)
	for _, m := range r.Manifest() {
		out = append(out, m.Name)
	}
	return out
}

// writeSkillWithAllowed creates a skill whose SKILL.md declares allowed-tools.
func writeSkillWithAllowed(t *testing.T, root, name string, allowed []string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: d\n"
	if len(allowed) > 0 {
		body += "allowed-tools:\n"
		for _, a := range allowed {
			body += "  - " + a + "\n"
		}
	}
	body += "---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stubBroker runs a tiny broker stub against miniredis that executes a
// scripted sequence of iteration responses. Each scriptStep describes what
// the LLM "returns" for the Nth iteration: zero or more tool_calls, then
// either final text (loop exits) or bare Done (another iteration follows).
type scriptStep struct {
	toolCalls []messaging.ToolCall
	finalText string // if non-empty, loop stops after this iteration
}

func runStubBroker(ctx context.Context, t *testing.T, client *messaging.Client, steps []scriptStep) <-chan struct{} {
	t.Helper()
	brokerGroup := "cg:broker-stub"
	_ = client.EnsureGroup(ctx, "stream:broker:slot-requests", brokerGroup)
	_ = client.EnsureGroup(ctx, "stream:broker:llm-requests", brokerGroup)

	done := make(chan struct{})
	go func() {
		defer close(done)

		go func() {
			for {
				msgs, ids, err := client.ReadGroup(ctx, "stream:broker:slot-requests", brokerGroup, "slots", 1, 200*time.Millisecond)
				if err != nil || len(msgs) == 0 {
					if ctx.Err() != nil {
						return
					}
					continue
				}
				m := msgs[0]
				_ = client.Ack(ctx, "stream:broker:slot-requests", brokerGroup, ids[0])
				if m.Type == messaging.TypeSlotRequest && m.ReplyStream != "" {
					reply, _ := messaging.NewReply(m, messaging.TypeSlotAssigned, "broker-stub", messaging.SlotAssignedPayload{SlotID: 0})
					_, _ = client.Publish(ctx, m.ReplyStream, reply)
				}
			}
		}()

		stepIdx := 0
		for {
			msgs, ids, err := client.ReadGroup(ctx, "stream:broker:llm-requests", brokerGroup, "llm", 1, 200*time.Millisecond)
			if err != nil || len(msgs) == 0 {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			m := msgs[0]
			_ = client.Ack(ctx, "stream:broker:llm-requests", brokerGroup, ids[0])
			if m.Type != messaging.TypeChatRequest {
				continue
			}
			if stepIdx >= len(steps) {
				return
			}
			step := steps[stepIdx]
			stepIdx++

			for _, tc := range step.toolCalls {
				tcReply, _ := messaging.NewReply(m, messaging.TypeToolCall, "broker-stub", messaging.ToolCallPayload{Call: tc})
				_, _ = client.Publish(ctx, m.ReplyStream, tcReply)
			}
			if step.finalText != "" {
				tm, _ := messaging.NewReply(m, messaging.TypeToken, "broker-stub", messaging.TokenPayload{Token: step.finalText})
				_, _ = client.Publish(ctx, m.ReplyStream, tm)
			}
			doneM, _ := messaging.NewReply(m, messaging.TypeToken, "broker-stub", messaging.TokenPayload{Done: true})
			_, _ = client.Publish(ctx, m.ReplyStream, doneM)
			if step.finalText != "" {
				return
			}
		}
	}()
	return done
}

// buildDeps wires a requestDeps with the supplied registry/store/rdb.
func buildDeps(client *messaging.Client, reg *tools.Registry, store *skills.Store, logger *slog.Logger) *requestDeps {
	return &requestDeps{
		client:    client,
		runtime:   agent.NewRuntime(client, "main-agent", 0, false, logger),
		registry:  reg,
		store:     store,
		rdb:       client.Redis(),
		baseTools: baseToolNames(reg),
		maxIter:   5,
		logger:    logger,
	}
}

// registerBuiltinsPlusCustom registers the four built-ins and optional extra
// tools, returning the registry.
func registerBuiltinsPlusCustom(t *testing.T, store *skills.Store, logger *slog.Logger, extras ...tools.Tool) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry(logger)
	for _, tool := range []tools.Tool{
		tools.NewListSkills(store),
		tools.NewReadSkill(store),
		tools.NewReadSkillFile(store),
		tools.NewCurrentTime(),
	} {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name(), err)
		}
	}
	for _, e := range extras {
		if err := reg.Register(e); err != nil {
			t.Fatalf("register extra %s: %v", e.Name(), err)
		}
	}
	return reg
}

// fakeInvokeTool records every invocation and returns a configurable result.
type fakeInvokeTool struct {
	name        string
	calls       int
	lastArgs    string
	schemaJSON  json.RawMessage
	description string
	invokeFn    func(ctx context.Context, args string) (string, error)
}

func (f *fakeInvokeTool) Name() string { return f.name }
func (f *fakeInvokeTool) Schema() messaging.ToolSchema {
	return messaging.ToolSchema{
		Type: "function",
		Function: messaging.ToolFunction{
			Name:        f.name,
			Description: f.description,
			Parameters:  f.schemaJSON,
		},
	}
}
func (f *fakeInvokeTool) Invoke(ctx context.Context, args string) (string, error) {
	f.calls++
	f.lastArgs = args
	if f.invokeFn != nil {
		return f.invokeFn(ctx, args)
	}
	return "fake-ok", nil
}

func buildPayload(sessionID string) *messaging.Message {
	replyStream := "stream:test:reply:" + messaging.NewCorrelationID()
	payload := messaging.ContextAssembledPayload{
		SessionID:   sessionID,
		Messages:    []messaging.ChatMsg{{Role: "system", Content: "sys"}, {Role: "user", Content: "do the thing"}},
		TargetAgent: "main-agent",
		ReplyStream: replyStream,
	}
	pbytes, _ := json.Marshal(payload)
	return &messaging.Message{
		Type:          messaging.TypeContextAssembled,
		CorrelationID: messaging.NewCorrelationID(),
		Source:        "test",
		Payload:       pbytes,
	}
}

func newTestHarness(t *testing.T) (*messaging.Client, *slog.Logger, *bytes.Buffer, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := messaging.NewClient(mr.Addr())
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(&buf, io.Discard), nil))
	return client, logger, &buf, func() { client.Close() }
}

// --- Tests ---

func TestGating_NoActiveSkillGatesUnknownTool(t *testing.T) {
	client, logger, buf, cleanup := newTestHarness(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	skillsRoot := t.TempDir()
	store := skills.NewStore(skillsRoot, logger)

	hidden := &fakeInvokeTool{name: "hidden_tool", description: "not in base"}
	reg := registerBuiltinsPlusCustom(t, store, logger, hidden)
	// Deliberately craft baseTools to EXCLUDE hidden_tool (base = first four
	// built-ins). This simulates hidden_tool being an MCP tool that wasn't
	// configured, while the model "knows" about it somehow.
	deps := &requestDeps{
		client:    client,
		runtime:   agent.NewRuntime(client, "main-agent", 0, false, logger),
		registry:  reg,
		store:     store,
		rdb:       client.Redis(),
		baseTools: []string{"list_skills", "read_skill", "read_skill_file", "current_time"},
		maxIter:   5,
		logger:    logger,
	}

	steps := []scriptStep{
		{toolCalls: []messaging.ToolCall{{
			ID:       "c1",
			Type:     "function",
			Function: messaging.ToolCallFunction{Name: "hidden_tool", Arguments: "{}"},
		}}},
		{finalText: "done"},
	}
	brokerDone := runStubBroker(ctx, t, client, steps)

	req := buildPayload("sess-gate")
	var payload messaging.ContextAssembledPayload
	_ = json.Unmarshal(req.Payload, &payload)
	handleRequest(ctx, deps, req)

	select {
	case <-brokerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("broker stub timeout")
	}

	if hidden.calls != 0 {
		t.Fatalf("hidden tool invoked %d times; gate failed", hidden.calls)
	}
	if !strings.Contains(buf.String(), `"outcome":"gated"`) {
		t.Fatalf("expected gated outcome in log; got:\n%s", buf.String())
	}

	// Gate envelope must have made it into the tool-result that was sent
	// back on the reply stream.
	_ = client.EnsureGroup(ctx, payload.ReplyStream, "cg:reader-gate")
	msgs, _, err := client.ReadGroup(ctx, payload.ReplyStream, "cg:reader-gate", "r", 1, 1*time.Second)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("no reply: err=%v", err)
	}
	var resp messaging.ChatResponsePayload
	if err := msgs[0].DecodePayload(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolResults) != 1 {
		t.Fatalf("tool_results = %d, want 1", len(resp.ToolResults))
	}
	if !strings.Contains(resp.ToolResults[0].Output, "tool not available under active skill") {
		t.Fatalf("gate envelope missing from tool-result: %q", resp.ToolResults[0].Output)
	}
}

func TestGating_ActiveSkillAllowsNewTool(t *testing.T) {
	client, logger, _, cleanup := newTestHarness(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	skillsRoot := t.TempDir()
	writeSkillWithAllowed(t, skillsRoot, "expands", []string{"fancy_tool"})
	store := skills.NewStore(skillsRoot, logger)

	fancy := &fakeInvokeTool{name: "fancy_tool", description: "unlocked by expands"}
	reg := registerBuiltinsPlusCustom(t, store, logger, fancy)
	deps := &requestDeps{
		client:    client,
		runtime:   agent.NewRuntime(client, "main-agent", 0, false, logger),
		registry:  reg,
		store:     store,
		rdb:       client.Redis(),
		baseTools: []string{"list_skills", "read_skill", "read_skill_file", "current_time"},
		maxIter:   5,
		logger:    logger,
	}

	// Pre-activate the skill in Valkey so iteration 1 sees it.
	_ = sessionskill.Set(ctx, client.Redis(), "sess-allow", "expands", time.Hour)

	steps := []scriptStep{
		{toolCalls: []messaging.ToolCall{{
			ID:       "c1",
			Type:     "function",
			Function: messaging.ToolCallFunction{Name: "fancy_tool", Arguments: `{"hi":true}`},
		}}},
		{finalText: "fancy done"},
	}
	brokerDone := runStubBroker(ctx, t, client, steps)

	handleRequest(ctx, deps, buildPayload("sess-allow"))

	select {
	case <-brokerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("broker stub timeout")
	}

	if fancy.calls != 1 {
		t.Fatalf("fancy_tool calls = %d, want 1", fancy.calls)
	}
}

func TestGating_ReadSkillActivatesForNextIteration(t *testing.T) {
	client, logger, buf, cleanup := newTestHarness(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	skillsRoot := t.TempDir()
	writeSkillWithAllowed(t, skillsRoot, "activates", []string{"bonus_tool"})
	store := skills.NewStore(skillsRoot, logger)

	bonus := &fakeInvokeTool{name: "bonus_tool"}
	reg := registerBuiltinsPlusCustom(t, store, logger, bonus)
	deps := &requestDeps{
		client:    client,
		runtime:   agent.NewRuntime(client, "main-agent", 0, false, logger),
		registry:  reg,
		store:     store,
		rdb:       client.Redis(),
		baseTools: []string{"list_skills", "read_skill", "read_skill_file", "current_time"},
		maxIter:   5,
		logger:    logger,
	}

	// Iteration 1: model calls read_skill("activates") — base tool, succeeds.
	// Iteration 2: model calls bonus_tool — only visible after activation.
	steps := []scriptStep{
		{toolCalls: []messaging.ToolCall{{
			ID:       "c1",
			Type:     "function",
			Function: messaging.ToolCallFunction{Name: "read_skill", Arguments: `{"name":"activates"}`},
		}}},
		{toolCalls: []messaging.ToolCall{{
			ID:       "c2",
			Type:     "function",
			Function: messaging.ToolCallFunction{Name: "bonus_tool", Arguments: "{}"},
		}}},
		{finalText: "done"},
	}
	brokerDone := runStubBroker(ctx, t, client, steps)

	handleRequest(ctx, deps, buildPayload("sess-activate"))

	select {
	case <-brokerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("broker stub timeout")
	}

	if bonus.calls != 1 {
		t.Fatalf("bonus_tool calls = %d, want 1", bonus.calls)
	}

	// Valkey key should be set to "activates".
	got, err := sessionskill.Get(context.Background(), client.Redis(), "sess-activate")
	if err != nil || got != "activates" {
		t.Fatalf("valkey active = (%q, %v)", got, err)
	}

	if !strings.Contains(buf.String(), `"active_skill_changed"`) {
		t.Fatal("expected active_skill_changed log")
	}
}

func TestGating_FailedReadSkillDoesNotActivate(t *testing.T) {
	client, logger, buf, cleanup := newTestHarness(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	skillsRoot := t.TempDir()
	store := skills.NewStore(skillsRoot, logger)

	reg := registerBuiltinsPlusCustom(t, store, logger)
	deps := buildDeps(client, reg, store, logger)

	steps := []scriptStep{
		{toolCalls: []messaging.ToolCall{{
			ID:       "c1",
			Type:     "function",
			Function: messaging.ToolCallFunction{Name: "read_skill", Arguments: `{"name":"nonexistent"}`},
		}}},
		{finalText: "nope"},
	}
	brokerDone := runStubBroker(ctx, t, client, steps)

	handleRequest(ctx, deps, buildPayload("sess-nope"))
	<-brokerDone

	got, _ := sessionskill.Get(context.Background(), client.Redis(), "sess-nope")
	if got != "" {
		t.Fatalf("active skill should remain empty; got %q", got)
	}
	if strings.Contains(buf.String(), `"active_skill_changed"`) {
		t.Fatalf("should not have logged activation; buf:\n%s", buf.String())
	}
}

func TestGating_UnknownAllowedToolLogsWarn(t *testing.T) {
	client, logger, buf, cleanup := newTestHarness(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	skillsRoot := t.TempDir()
	writeSkillWithAllowed(t, skillsRoot, "bogus", []string{"does_not_exist"})
	store := skills.NewStore(skillsRoot, logger)

	reg := registerBuiltinsPlusCustom(t, store, logger)
	deps := buildDeps(client, reg, store, logger)

	_ = sessionskill.Set(ctx, client.Redis(), "sess-warn", "bogus", time.Hour)

	steps := []scriptStep{{finalText: "done"}}
	brokerDone := runStubBroker(ctx, t, client, steps)

	handleRequest(ctx, deps, buildPayload("sess-warn"))
	<-brokerDone

	if !strings.Contains(buf.String(), "skill_allowed_tool_unknown") {
		t.Fatalf("expected WARN log for unknown allowed-tool; got:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `"unknown_tool":"does_not_exist"`) {
		t.Fatalf("WARN did not name unknown tool; got:\n%s", buf.String())
	}
}

func TestGating_StaleActiveSkillCleared(t *testing.T) {
	client, logger, buf, cleanup := newTestHarness(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Skill `vanished` is NOT present on disk, but we seed Valkey as if it
	// had been loaded earlier. Store has no other skills.
	skillsRoot := t.TempDir()
	store := skills.NewStore(skillsRoot, logger)

	reg := registerBuiltinsPlusCustom(t, store, logger)
	deps := buildDeps(client, reg, store, logger)

	_ = sessionskill.Set(ctx, client.Redis(), "sess-stale", "vanished", time.Hour)

	steps := []scriptStep{{finalText: "done"}}
	brokerDone := runStubBroker(ctx, t, client, steps)

	handleRequest(ctx, deps, buildPayload("sess-stale"))
	<-brokerDone

	got, _ := sessionskill.Get(context.Background(), client.Redis(), "sess-stale")
	if got != "" {
		t.Fatalf("stale active skill should be cleared; got %q", got)
	}
	if !strings.Contains(buf.String(), "skill_missing_from_store") {
		t.Fatalf("expected transition log; got:\n%s", buf.String())
	}
}
