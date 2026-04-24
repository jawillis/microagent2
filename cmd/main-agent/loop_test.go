package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"microagent2/internal/agent"
	"microagent2/internal/messaging"
	"microagent2/internal/skills"
	"microagent2/internal/tools"
)

// TestHandleRequest_ToolLoopRunsTwoIterations exercises the main-agent tool
// loop end-to-end against an in-memory broker stub: iteration 1 returns a
// tool_call for list_skills, iteration 2 returns final text. The test asserts
// the registry was invoked and the final ChatResponsePayload on the reply
// stream contains both tool_calls and tool_results.
func TestHandleRequest_ToolLoopRunsTwoIterations(t *testing.T) {
	mr := miniredis.RunT(t)
	client := messaging.NewClient(mr.Addr())
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	// Skills store with one skill so list_skills returns non-empty.
	skillsRoot := t.TempDir()
	dir := filepath.Join(skillsRoot, "demo")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: demo\ndescription: a demo skill\n---\nbody\n"), 0o644)
	store := skills.NewStore(skillsRoot, logger)

	reg := tools.NewRegistry(logger)
	if err := reg.Register(tools.NewListSkills(store)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tools.NewReadSkill(store)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tools.NewReadSkillFile(store)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tools.NewCurrentTime()); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tools.NewRunSkillScript(nil, logger)); err != nil {
		t.Fatal(err)
	}

	// Schema order: list_skills, read_skill, read_skill_file, current_time, run_skill_script.
	schemas := reg.Schemas()
	wantOrder := []string{"list_skills", "read_skill", "read_skill_file", "current_time", "run_skill_script"}
	if len(schemas) != len(wantOrder) {
		t.Fatalf("schemas len = %d, want %d", len(schemas), len(wantOrder))
	}
	for i, want := range wantOrder {
		if schemas[i].Function.Name != want {
			t.Fatalf("schemas[%d] = %q, want %q", i, schemas[i].Function.Name, want)
		}
	}

	rt := agent.NewRuntime(client, "main-agent", 0, false, logger)

	// Stub broker: consume llm-requests, reply via the request's reply stream
	// and also simulate slot assignment via broker slot-request stream.
	brokerGroup := "cg:broker-stub"
	_ = client.EnsureGroup(ctx, "stream:broker:slot-requests", brokerGroup)
	_ = client.EnsureGroup(ctx, "stream:broker:llm-requests", brokerGroup)

	iter := 0
	brokerDone := make(chan struct{})
	go func() {
		defer close(brokerDone)
		// Handle slot requests by sending a slot_assigned reply
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

		// Handle LLM requests — iteration 1 returns tool_call; iteration 2 final text.
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
			iter++
			if iter == 1 {
				tcReply, _ := messaging.NewReply(m, messaging.TypeToolCall, "broker-stub", messaging.ToolCallPayload{
					Call: messaging.ToolCall{
						ID:       "call_xyz",
						Type:     "function",
						Function: messaging.ToolCallFunction{Name: "list_skills", Arguments: "{}"},
					},
				})
				_, _ = client.Publish(ctx, m.ReplyStream, tcReply)
				doneM, _ := messaging.NewReply(m, messaging.TypeToken, "broker-stub", messaging.TokenPayload{Done: true})
				_, _ = client.Publish(ctx, m.ReplyStream, doneM)
			} else {
				// Iteration 2: final text, no tool_calls
				for _, tok := range []string{"skills ", "are ", "loaded"} {
					tm, _ := messaging.NewReply(m, messaging.TypeToken, "broker-stub", messaging.TokenPayload{Token: tok})
					_, _ = client.Publish(ctx, m.ReplyStream, tm)
				}
				doneM, _ := messaging.NewReply(m, messaging.TypeToken, "broker-stub", messaging.TokenPayload{Done: true})
				_, _ = client.Publish(ctx, m.ReplyStream, doneM)
				return
			}
		}
	}()

	// Build an incoming ContextAssembledPayload message.
	replyStream := "stream:test:reply:" + messaging.NewCorrelationID()
	req := &messaging.Message{
		Type:          messaging.TypeContextAssembled,
		CorrelationID: messaging.NewCorrelationID(),
		Source:        "test",
	}
	payload := messaging.ContextAssembledPayload{
		SessionID:   "sess-loop",
		Messages:    []messaging.ChatMsg{{Role: "system", Content: "sys"}, {Role: "user", Content: "list the skills"}},
		TargetAgent: "main-agent",
		ReplyStream: replyStream,
	}
	pbytes, _ := json.Marshal(payload)
	req.Payload = pbytes

	// Run the handler.
	deps := &requestDeps{
		client:    client,
		runtime:   rt,
		registry:  reg,
		store:     store,
		rdb:       client.Redis(),
		baseTools: baseToolNames(reg),
		maxIter:   5,
		logger:    logger,
	}
	handleRequest(ctx, deps, req)

	select {
	case <-brokerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("broker stub did not finish")
	}

	if iter != 2 {
		t.Fatalf("expected 2 iterations, got %d", iter)
	}

	// Read the reply from the reply stream and assert ToolCalls/ToolResults.
	_ = client.EnsureGroup(ctx, replyStream, "cg:reader")
	msgs, _, err := client.ReadGroup(ctx, replyStream, "cg:reader", "r", 1, 1*time.Second)
	if err != nil || len(msgs) == 0 {
		t.Fatalf("no reply: err=%v", err)
	}
	var resp messaging.ChatResponsePayload
	if err := msgs[0].DecodePayload(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Content != "skills are loaded" {
		t.Fatalf("content: %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_xyz" || resp.ToolCalls[0].Function.Name != "list_skills" {
		t.Fatalf("tool_calls: %+v", resp.ToolCalls)
	}
	if len(resp.ToolResults) != 1 || resp.ToolResults[0].CallID != "call_xyz" {
		t.Fatalf("tool_results: %+v", resp.ToolResults)
	}
	// The invoked list_skills should have returned [{"name":"demo","description":"a demo skill"}]
	if resp.ToolResults[0].Output != `[{"name":"demo","description":"a demo skill"}]` {
		t.Fatalf("result output: %q", resp.ToolResults[0].Output)
	}
}
