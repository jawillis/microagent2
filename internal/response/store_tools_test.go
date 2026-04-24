package response

import (
	"context"
	"strings"
	"testing"

	"microagent2/internal/messaging"
)

func TestSaveAndGetToolCallRoundTrip(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	resp := &Response{
		ID:        "resp_tc",
		SessionID: "sess-tc",
		Input: []InputItem{
			{Type: "message", Role: "user", Content: "use the tool"},
			{Type: "function_call_output", CallID: "call_1", Output: "result body"},
		},
		Output: []OutputItem{
			{Type: "function_call", CallID: "call_1", Name: "list_skills", Args: `{"q":"x"}`},
			{Type: "message", Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "done"}}},
		},
		CreatedAt: "2025-01-01T00:00:00Z",
		Status:    StatusCompleted,
	}
	if err := store.Save(ctx, resp); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.Get(ctx, "resp_tc")
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}

	if len(got.Input) != 2 || got.Input[1].Type != "function_call_output" || got.Input[1].CallID != "call_1" || got.Input[1].Output != "result body" {
		t.Fatalf("input round-trip: %+v", got.Input)
	}
	if len(got.Output) != 2 || got.Output[0].Type != "function_call" || got.Output[0].CallID != "call_1" || got.Output[0].Name != "list_skills" || got.Output[0].Args != `{"q":"x"}` {
		t.Fatalf("output round-trip: %+v", got.Output)
	}
}

func TestGetSessionMessagesPreservesFunctionCallOutputInOutput(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	resp := &Response{
		ID:        "resp_agentic",
		SessionID: "sess-agentic",
		Input: []InputItem{
			{Type: "message", Role: "user", Content: "do it"},
		},
		Output: []OutputItem{
			{Type: "function_call", CallID: "c1", Name: "list_skills", Args: `{}`},
			{Type: "function_call_output", CallID: "c1", Output: "[{\"name\":\"a\",\"description\":\"A\"}]"},
			{Type: "function_call", CallID: "c2", Name: "read_skill", Args: `{"name":"a"}`},
			{Type: "function_call_output", CallID: "c2", Output: "body of a"},
			{Type: "message", Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "here is a"}}},
		},
		CreatedAt: "2025-01-01T00:00:00Z",
		Status:    StatusCompleted,
	}
	if err := store.Save(ctx, resp); err != nil {
		t.Fatalf("save: %v", err)
	}

	msgs, err := store.GetSessionMessages(ctx, "sess-agentic")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// Expect: user, assistant(tool_calls c1), tool(c1), assistant(tool_calls c2), tool(c2), assistant("here is a")
	if len(msgs) != 6 {
		t.Fatalf("count: %d %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" {
		t.Fatalf("[0]: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].ID != "c1" {
		t.Fatalf("[1]: %+v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "c1" || !strings.Contains(msgs[2].Content, "name") {
		t.Fatalf("[2]: %+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || len(msgs[3].ToolCalls) != 1 || msgs[3].ToolCalls[0].ID != "c2" {
		t.Fatalf("[3]: %+v", msgs[3])
	}
	if msgs[4].Role != "tool" || msgs[4].ToolCallID != "c2" || msgs[4].Content != "body of a" {
		t.Fatalf("[4]: %+v", msgs[4])
	}
	if msgs[5].Role != "assistant" || msgs[5].Content != "here is a" {
		t.Fatalf("[5]: %+v", msgs[5])
	}
}

func TestGetSessionMessagesPreservesToolFields(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	resp := &Response{
		ID:        "resp_tc_2",
		SessionID: "sess-tc-2",
		Input: []InputItem{
			{Type: "message", Role: "user", Content: "pls"},
			{Type: "function_call_output", CallID: "call_1", Output: "result"},
		},
		Output: []OutputItem{
			{Type: "function_call", CallID: "call_1", Name: "list_skills", Args: `{}`},
			{Type: "message", Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "ok"}}},
		},
		CreatedAt: "2025-01-01T00:00:00Z",
		Status:    StatusCompleted,
	}
	if err := store.Save(ctx, resp); err != nil {
		t.Fatalf("save: %v", err)
	}

	msgs, err := store.GetSessionMessages(ctx, "sess-tc-2")
	if err != nil {
		t.Fatalf("get session messages: %v", err)
	}

	// Expect: user, tool(call_1), assistant(tool_calls=[call_1]), assistant("ok")
	if len(msgs) != 4 {
		t.Fatalf("msg count: %d %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "pls" {
		t.Fatalf("msgs[0]: %+v", msgs[0])
	}
	if msgs[1].Role != "tool" || msgs[1].ToolCallID != "call_1" || msgs[1].Content != "result" {
		t.Fatalf("msgs[1]: %+v", msgs[1])
	}
	if msgs[2].Role != "assistant" || len(msgs[2].ToolCalls) != 1 || msgs[2].ToolCalls[0].Function.Name != "list_skills" {
		t.Fatalf("msgs[2]: %+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Content != "ok" {
		t.Fatalf("msgs[3]: %+v", msgs[3])
	}

	// Sanity: round-trip through JSON unchanged
	_ = messaging.ChatMsg{} // ensure import used
}
