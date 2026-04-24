package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"microagent2/internal/messaging"
	"microagent2/internal/response"
)

// TestCreateResponse_NonStreamingHidesServerTraceFromClient verifies the key
// boundary: when the agent's reply carries tool_calls and tool_results
// (server-side agentic loop), the client response body contains ONLY the
// final assistant text message — function_call/function_call_output items
// are stripped. The full trace is still persisted for the dashboard.
func TestCreateResponse_NonStreamingHidesServerTraceFromClient(t *testing.T) {
	srv, mr := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	group := fmt.Sprintf("cg:fake-agent-trace-%d", time.Now().UnixNano())
	_ = srv.client.EnsureGroup(ctx, messaging.StreamGatewayRequests, group)

	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			msgs, ids, err := srv.client.ReadGroup(ctx, messaging.StreamGatewayRequests, group, "fake", 1, 200*time.Millisecond)
			if err != nil || len(msgs) == 0 {
				continue
			}
			reqMsg := msgs[0]
			_ = srv.client.Ack(ctx, messaging.StreamGatewayRequests, group, ids[0])
			reply, _ := messaging.NewReply(reqMsg, messaging.TypeChatResponse, "main-agent", messaging.ChatResponsePayload{
				Content: "here you go",
				ToolCalls: []messaging.ToolCall{
					{ID: "c1", Type: "function", Function: messaging.ToolCallFunction{Name: "list_skills", Arguments: `{}`}},
					{ID: "c2", Type: "function", Function: messaging.ToolCallFunction{Name: "read_skill", Arguments: `{"name":"a"}`}},
				},
				ToolResults: []messaging.ToolResult{
					{CallID: "c1", Output: `[{"name":"a","description":"A"}]`},
					{CallID: "c2", Output: "body of a"},
				},
				Done: true,
			})
			_, _ = srv.client.Publish(ctx, reqMsg.ReplyStream, reply)
			return
		}
	}()

	body := `{"input":"go","model":"test","stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	<-done

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}

	// Client-facing body: only the final message item.
	var resp struct {
		ID     string                `json:"id"`
		Output []response.OutputItem `json:"output"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("client body should contain exactly 1 item (message), got %d: %+v", len(resp.Output), resp.Output)
	}
	if resp.Output[0].Type != "message" || resp.Output[0].Role != "assistant" {
		t.Fatalf("client body item: %+v", resp.Output[0])
	}
	for _, it := range resp.Output {
		if it.Type == "function_call" || it.Type == "function_call_output" {
			t.Fatalf("client body leaked internal tool item: %+v", it)
		}
	}

	// Storage: full trace persisted.
	stored, err := srv.responses.Get(ctx, resp.ID)
	if err != nil || stored == nil {
		t.Fatalf("stored response missing: err=%v", err)
	}
	// Expected in storage: fc(c1), fco(c1), fc(c2), fco(c2), message
	if len(stored.Output) != 5 {
		t.Fatalf("storage len: %d %+v", len(stored.Output), stored.Output)
	}
	want := []struct{ Type, CallID string }{
		{"function_call", "c1"},
		{"function_call_output", "c1"},
		{"function_call", "c2"},
		{"function_call_output", "c2"},
		{"message", ""},
	}
	for i, w := range want {
		got := stored.Output[i]
		if got.Type != w.Type || got.CallID != w.CallID {
			t.Fatalf("storage[%d]: type=%q call_id=%q want type=%q call_id=%q", i, got.Type, got.CallID, w.Type, w.CallID)
		}
	}
	_ = mr
}

// TestCreateResponse_NonStreamingWithToolCalls verifies that a /v1/responses
// request whose agent reply carries tool_calls produces a client body that
// contains only the assistant text (server-side tool trace stripped), while
// the stored response retains the full trace for the dashboard.
func TestCreateResponse_NonStreamingWithToolCalls(t *testing.T) {
	srv, _ := newTestServer(t)

	// Fake the agent: consume the gateway's request and publish a reply with tool_calls.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	group := "cg:fake-agent"
	_ = srv.client.EnsureGroup(ctx, messaging.StreamGatewayRequests, group)

	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			msgs, ids, err := srv.client.ReadGroup(ctx, messaging.StreamGatewayRequests, group, "fake", 1, 200*time.Millisecond)
			if err != nil || len(msgs) == 0 {
				continue
			}
			reqMsg := msgs[0]
			_ = srv.client.Ack(ctx, messaging.StreamGatewayRequests, group, ids[0])

			reply, err := messaging.NewReply(reqMsg, messaging.TypeChatResponse, "main-agent", messaging.ChatResponsePayload{
				Content: "here you go",
				ToolCalls: []messaging.ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: messaging.ToolCallFunction{
						Name:      "list_skills",
						Arguments: `{"q":"x"}`,
					},
				}},
				Done: true,
			})
			if err != nil {
				return
			}
			_, _ = srv.client.Publish(ctx, reqMsg.ReplyStream, reply)
			return
		}
	}()

	body := `{"input":"go","model":"test","stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("fake agent did not run")
	}

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ID     string                `json:"id"`
		Output []response.OutputItem `json:"output"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Client body: message only, no tool trace.
	for _, it := range resp.Output {
		if it.Type == "function_call" || it.Type == "function_call_output" {
			t.Fatalf("client body leaked tool item: %+v", it)
		}
	}
	if len(resp.Output) != 1 || resp.Output[0].Type != "message" {
		t.Fatalf("client body: %+v", resp.Output)
	}

	// Storage: function_call present for audit/dashboard.
	stored, err := srv.responses.Get(context.Background(), resp.ID)
	if err != nil || stored == nil {
		t.Fatalf("stored: err=%v", err)
	}
	var seenStoredToolCall bool
	for _, it := range stored.Output {
		if it.Type == "function_call" && it.CallID == "call_1" {
			seenStoredToolCall = true
		}
	}
	if !seenStoredToolCall {
		t.Fatalf("stored response missing function_call: %+v", stored.Output)
	}
}

// TestCreateResponse_NonStreamingPureText verifies the pre-change shape is
// byte-compatible: a turn with no tool_calls produces output items that
// contain no function_call entries.
func TestCreateResponse_NonStreamingPureText(t *testing.T) {
	srv, _ := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	group := fmt.Sprintf("cg:fake-agent-%d", time.Now().UnixNano())
	_ = srv.client.EnsureGroup(ctx, messaging.StreamGatewayRequests, group)

	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			msgs, ids, err := srv.client.ReadGroup(ctx, messaging.StreamGatewayRequests, group, "fake", 1, 200*time.Millisecond)
			if err != nil || len(msgs) == 0 {
				continue
			}
			reqMsg := msgs[0]
			_ = srv.client.Ack(ctx, messaging.StreamGatewayRequests, group, ids[0])
			reply, _ := messaging.NewReply(reqMsg, messaging.TypeChatResponse, "main-agent", messaging.ChatResponsePayload{
				Content: "hello",
				Done:    true,
			})
			_, _ = srv.client.Publish(ctx, reqMsg.ReplyStream, reply)
			return
		}
	}()

	body := `{"input":"go","model":"test","stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	<-done

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Output []response.OutputItem `json:"output"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, out := range resp.Output {
		if out.Type == "function_call" {
			t.Fatalf("regression: pure-text turn emitted function_call output: %+v", out)
		}
	}
}
