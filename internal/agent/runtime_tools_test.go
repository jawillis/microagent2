package agent

import (
	"context"
	"log/slog"
	"io"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"microagent2/internal/messaging"
)

// helper: drive Execute by publishing canned reply messages on the reply stream
// our brand-new Execute subscribes to. We bypass broker/slot machinery by
// pre-seeding the runtime's slotID.
func setupRuntime(t *testing.T) (*Runtime, *messaging.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := messaging.NewClient(mr.Addr())
	t.Cleanup(func() { client.Close() })
	rt := NewRuntime(client, "test-agent", 0, false, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	rt.slotID = 0 // bypass slot machinery
	return rt, client, mr
}

func TestExecuteReturnsToolCallsWithoutAddingToText(t *testing.T) {
	rt, client, _ := setupRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Arrange: subscribe to the llm-requests stream so we can discover the
	// reply stream the runtime chose, then publish canned replies there.
	group := "cg:broker-stub"
	_ = client.EnsureGroup(ctx, "stream:broker:llm-requests", group)

	done := make(chan struct{})
	var onTokenCalls []string
	var onToolCallCalls []messaging.ToolCall

	var result string
	var calls []messaging.ToolCall
	var execErr error

	go func() {
		defer close(done)
		result, calls, execErr = rt.Execute(ctx,
			[]messaging.ChatMsg{{Role: "user", Content: "hi"}},
			nil,
			func(tok string) { onTokenCalls = append(onTokenCalls, tok) },
			func(c messaging.ToolCall) { onToolCallCalls = append(onToolCallCalls, c) },
		)
	}()

	// Wait for the runtime to publish its request so we can learn its reply stream.
	var reqMsg *messaging.Message
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msgs, ids, err := client.ReadGroup(ctx, "stream:broker:llm-requests", group, "stub", 1, 200*time.Millisecond)
		if err == nil && len(msgs) > 0 {
			reqMsg = msgs[0]
			_ = client.Ack(ctx, "stream:broker:llm-requests", group, ids[0])
			break
		}
	}
	if reqMsg == nil {
		t.Fatal("runtime did not publish llm request")
	}

	reply := reqMsg.ReplyStream
	if reply == "" {
		t.Fatal("reply stream empty")
	}

	// Emit a TypeToolCall, then a Done TypeToken.
	tcMsg, err := messaging.NewReply(reqMsg, messaging.TypeToolCall, "llm-broker", messaging.ToolCallPayload{
		Call: messaging.ToolCall{
			ID:   "call_1",
			Type: "function",
			Function: messaging.ToolCallFunction{
				Name:      "list_skills",
				Arguments: `{"q":"x"}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("build tc reply: %v", err)
	}
	if _, err := client.Publish(ctx, reply, tcMsg); err != nil {
		t.Fatalf("publish tc: %v", err)
	}

	doneMsg, err := messaging.NewReply(reqMsg, messaging.TypeToken, "llm-broker", messaging.TokenPayload{Done: true})
	if err != nil {
		t.Fatalf("build done: %v", err)
	}
	if _, err := client.Publish(ctx, reply, doneMsg); err != nil {
		t.Fatalf("publish done: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return")
	}

	if execErr != nil {
		t.Fatalf("execErr: %v", execErr)
	}
	if result != "" {
		t.Fatalf("expected empty text, got %q", result)
	}
	if len(calls) != 1 || calls[0].Function.Name != "list_skills" {
		t.Fatalf("calls: %+v", calls)
	}
	if len(onTokenCalls) != 0 {
		t.Fatalf("onToken should not be invoked for tool calls, got %v", onTokenCalls)
	}
	if len(onToolCallCalls) != 1 {
		t.Fatalf("onToolCall should be invoked exactly once, got %d", len(onToolCallCalls))
	}
}

func TestExecuteTextOnlyReturnsEmptyToolCalls(t *testing.T) {
	rt, client, _ := setupRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	group := "cg:broker-stub"
	_ = client.EnsureGroup(ctx, "stream:broker:llm-requests", group)

	done := make(chan struct{})
	var result string
	var calls []messaging.ToolCall
	var execErr error
	go func() {
		defer close(done)
		result, calls, execErr = rt.Execute(ctx,
			[]messaging.ChatMsg{{Role: "user", Content: "hi"}},
			nil,
			nil, nil,
		)
	}()

	var reqMsg *messaging.Message
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msgs, ids, err := client.ReadGroup(ctx, "stream:broker:llm-requests", group, "stub", 1, 200*time.Millisecond)
		if err == nil && len(msgs) > 0 {
			reqMsg = msgs[0]
			_ = client.Ack(ctx, "stream:broker:llm-requests", group, ids[0])
			break
		}
	}
	if reqMsg == nil {
		t.Fatal("runtime did not publish llm request")
	}

	for _, tok := range []string{"hello", " ", "world"} {
		m, _ := messaging.NewReply(reqMsg, messaging.TypeToken, "llm-broker", messaging.TokenPayload{Token: tok})
		_, _ = client.Publish(ctx, reqMsg.ReplyStream, m)
	}
	doneM, _ := messaging.NewReply(reqMsg, messaging.TypeToken, "llm-broker", messaging.TokenPayload{Done: true})
	_, _ = client.Publish(ctx, reqMsg.ReplyStream, doneM)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return")
	}

	if execErr != nil {
		t.Fatalf("execErr: %v", execErr)
	}
	if result != "hello world" {
		t.Fatalf("text: %q", result)
	}
	if len(calls) != 0 {
		t.Fatalf("expected empty calls, got %+v", calls)
	}
}
