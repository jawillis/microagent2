package gateway

import (
	"testing"

	"microagent2/internal/response"
)

func TestChainToMessages_PreservesToolCalls(t *testing.T) {
	chain := []*response.Response{
		{
			Input: []response.InputItem{
				{Type: "message", Role: "user", Content: "pls"},
				{Type: "function_call_output", CallID: "call_1", Output: "result body"},
			},
			Output: []response.OutputItem{
				{Type: "function_call", CallID: "call_1", Name: "list_skills", Args: `{}`},
				{Type: "message", Role: "assistant", Content: []response.ContentPart{{Type: "output_text", Text: "ok"}}},
			},
		},
	}

	msgs := chainToMessages(chain)

	if len(msgs) != 4 {
		t.Fatalf("len: %d %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "pls" {
		t.Fatalf("msgs[0]: %+v", msgs[0])
	}
	if msgs[1].Role != "tool" || msgs[1].ToolCallID != "call_1" || msgs[1].Content != "result body" {
		t.Fatalf("msgs[1]: %+v", msgs[1])
	}
	if msgs[2].Role != "assistant" || len(msgs[2].ToolCalls) != 1 || msgs[2].ToolCalls[0].ID != "call_1" || msgs[2].ToolCalls[0].Function.Name != "list_skills" {
		t.Fatalf("msgs[2]: %+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Content != "ok" {
		t.Fatalf("msgs[3]: %+v", msgs[3])
	}
}

func TestChainToMessages_PreservesFunctionCallOutputInOutput(t *testing.T) {
	chain := []*response.Response{
		{
			Input: []response.InputItem{
				{Type: "message", Role: "user", Content: "use tool"},
			},
			Output: []response.OutputItem{
				{Type: "function_call", CallID: "c1", Name: "list_skills", Args: `{}`},
				{Type: "function_call_output", CallID: "c1", Output: "result body"},
				{Type: "message", Role: "assistant", Content: []response.ContentPart{{Type: "output_text", Text: "done"}}},
			},
		},
	}

	msgs := chainToMessages(chain)
	if len(msgs) != 4 {
		t.Fatalf("len: %d %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" {
		t.Fatalf("[0]: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("[1]: %+v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "c1" || msgs[2].Content != "result body" {
		t.Fatalf("[2]: %+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Content != "done" {
		t.Fatalf("[3]: %+v", msgs[3])
	}
}

func TestInputItemsToMessages_PreservesFunctionCallOutput(t *testing.T) {
	items := []response.InputItem{
		{Type: "message", Role: "user", Content: "ping"},
		{Type: "function_call_output", CallID: "c1", Output: "pong"},
	}
	msgs := inputItemsToMessages(items)
	if len(msgs) != 2 {
		t.Fatalf("len: %d", len(msgs))
	}
	if msgs[1].Role != "tool" || msgs[1].ToolCallID != "c1" || msgs[1].Content != "pong" {
		t.Fatalf("tool msg: %+v", msgs[1])
	}
}
