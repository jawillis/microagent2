package broker

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"microagent2/internal/messaging"
)

func newTestBroker() *Broker {
	return &Broker{logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}
}

func collectStream(t *testing.T, body io.Reader) ([]string, []messaging.ToolCall, error) {
	t.Helper()
	b := newTestBroker()
	tokenCh := make(chan string, 64)
	toolCallCh := make(chan messaging.ToolCall, 8)
	errCh := make(chan error, 1)

	done := make(chan struct{})
	go func() {
		b.readSSEStream(body, tokenCh, toolCallCh, errCh)
		close(tokenCh)
		close(toolCallCh)
		close(errCh)
		close(done)
	}()

	var tokens []string
	var calls []messaging.ToolCall
	for t := range tokenCh {
		tokens = append(tokens, t)
	}
	for c := range toolCallCh {
		calls = append(calls, c)
	}
	var streamErr error
	for e := range errCh {
		streamErr = e
	}
	<-done
	return tokens, calls, streamErr
}

func sseChunk(delta map[string]any) string {
	body, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": delta}},
	})
	return "data: " + string(body) + "\n"
}

func TestSSEStreamPureTextRegressionFree(t *testing.T) {
	body := strings.Join([]string{
		sseChunk(map[string]any{"content": "hello"}),
		sseChunk(map[string]any{"content": " world"}),
		"data: [DONE]\n",
	}, "")
	tokens, calls, err := collectStream(t, strings.NewReader(body))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected zero tool calls, got %d", len(calls))
	}
	if strings.Join(tokens, "") != "hello world" {
		t.Fatalf("tokens: %q", tokens)
	}
}

func TestSSEStreamSingleToolCallAcrossThreeChunks(t *testing.T) {
	body := strings.Join([]string{
		sseChunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": "list_skills"}}}}),
		sseChunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": "{\"q\":"}}}}),
		sseChunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": "\"a\"}"}}}}),
		"data: [DONE]\n",
	}, "")
	tokens, calls, err := collectStream(t, strings.NewReader(body))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected no tokens, got %v", tokens)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	c := calls[0]
	if c.ID != "call_1" || c.Type != "function" || c.Function.Name != "list_skills" || c.Function.Arguments != `{"q":"a"}` {
		t.Fatalf("unexpected call: %+v", c)
	}
}

func TestSSEStreamInterleavedToolCallsTwoIndices(t *testing.T) {
	body := strings.Join([]string{
		sseChunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c0", "function": map[string]any{"name": "a"}}}}),
		sseChunk(map[string]any{"tool_calls": []any{map[string]any{"index": 1, "id": "c1", "function": map[string]any{"name": "b"}}}}),
		sseChunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": "{\"x\":1}"}}}}),
		sseChunk(map[string]any{"tool_calls": []any{map[string]any{"index": 1, "function": map[string]any{"arguments": "{\"y\":2}"}}}}),
		"data: [DONE]\n",
	}, "")
	_, calls, err := collectStream(t, strings.NewReader(body))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}
	if calls[0].Function.Name != "a" || calls[0].Function.Arguments != `{"x":1}` {
		t.Fatalf("call[0]: %+v", calls[0])
	}
	if calls[1].Function.Name != "b" || calls[1].Function.Arguments != `{"y":2}` {
		t.Fatalf("call[1]: %+v", calls[1])
	}
}

func TestSSEStreamLegacyFunctionCallIgnored(t *testing.T) {
	body := strings.Join([]string{
		sseChunk(map[string]any{"function_call": map[string]any{"name": "old", "arguments": "{}"}}),
		sseChunk(map[string]any{"content": "hi"}),
		"data: [DONE]\n",
	}, "")
	tokens, calls, err := collectStream(t, strings.NewReader(body))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected 0 tool calls, got %d", len(calls))
	}
	if strings.Join(tokens, "") != "hi" {
		t.Fatalf("tokens: %v", tokens)
	}
}

func TestChatCompletionRequestOmitsEmptyTools(t *testing.T) {
	data, err := json.Marshal(chatCompletionRequest{
		Model:    "default",
		Messages: []messaging.ChatMsg{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "tools") || strings.Contains(got, "tool_choice") {
		t.Fatalf("regression: body contains tool keys: %s", got)
	}
}

func TestChatCompletionRequestIncludesToolsWhenProvided(t *testing.T) {
	tools := []messaging.ToolSchema{{
		Type: "function",
		Function: messaging.ToolFunction{
			Name:        "list_skills",
			Description: "list available skills",
		},
	}}
	data, err := json.Marshal(chatCompletionRequest{
		Model:      "default",
		Messages:   []messaging.ChatMsg{{Role: "user", Content: "hi"}},
		Stream:     true,
		Tools:      tools,
		ToolChoice: "auto",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"tools":[{"type":"function","function":{"name":"list_skills","description":"list available skills"}}]`) {
		t.Fatalf("tools not serialized as expected: %s", got)
	}
	if !strings.Contains(got, `"tool_choice":"auto"`) {
		t.Fatalf("tool_choice missing: %s", got)
	}
}

// Keep vet happy — bytes.Buffer used elsewhere; this silences unused-import
// warnings in test-only builds.
var _ = bytes.NewBuffer
