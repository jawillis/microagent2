package messaging

import (
	"encoding/json"
	"testing"
)

func TestChatMsgPlainMarshalOmitsToolFields(t *testing.T) {
	m := ChatMsg{Role: "user", Content: "hi"}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	want := `{"role":"user","content":"hi"}`
	if got != want {
		t.Fatalf("regression: want %q got %q", want, got)
	}
}

func TestChatMsgToolResultShape(t *testing.T) {
	m := ChatMsg{Role: "tool", Content: "result", ToolCallID: "call_1"}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	want := `{"role":"tool","content":"result","tool_call_id":"call_1"}`
	if got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestChatMsgAssistantToolCallsShape(t *testing.T) {
	m := ChatMsg{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: ToolCallFunction{
				Name:      "list_skills",
				Arguments: `{}`,
			},
		}},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	want := `{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_skills","arguments":"{}"}}]}`
	if got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestLLMRequestPayloadOmitsEmptyToolFields(t *testing.T) {
	p := LLMRequestPayload{SlotID: 3, Messages: []ChatMsg{{Role: "user", Content: "hi"}}, Stream: true}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	want := `{"slot_id":3,"messages":[{"role":"user","content":"hi"}],"stream":true}`
	if got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestChatResponsePayloadOmitsEmptyToolResults(t *testing.T) {
	p := ChatResponsePayload{SessionID: "s", Content: "hi", Done: true}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	if got != `{"session_id":"s","content":"hi","done":true}` {
		t.Fatalf("got %q", got)
	}
}

func TestToolResultJSONKeys(t *testing.T) {
	data, err := json.Marshal(ToolResult{CallID: "c1", Output: "out"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	if got != `{"call_id":"c1","output":"out"}` {
		t.Fatalf("got %q", got)
	}
}

func TestToolResultPayloadRoundTrip(t *testing.T) {
	msg, err := NewMessage(TypeToolResult, "main-agent", ToolResultPayload{SessionID: "s", CallID: "c1", Output: "out"})
	if err != nil {
		t.Fatal(err)
	}
	var p ToolResultPayload
	if err := msg.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "s" || p.CallID != "c1" || p.Output != "out" {
		t.Fatalf("roundtrip: %+v", p)
	}
}

func TestSlotRequestPayloadOmitsEmptyClass(t *testing.T) {
	p := SlotRequestPayload{AgentID: "a", Priority: 1}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"agent_id":"a","priority":1}`
	if got := string(data); got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestSlotRequestPayloadEmitsClassWhenSet(t *testing.T) {
	p := SlotRequestPayload{AgentID: "proxy", Priority: 0, SlotClass: "hindsight"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"agent_id":"proxy","priority":0,"slot_class":"hindsight"}`
	if got := string(data); got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestSlotRequestPayloadRoundTripWithClass(t *testing.T) {
	p := SlotRequestPayload{AgentID: "proxy", Priority: 0, SlotClass: "hindsight"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back SlotRequestPayload
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back != p {
		t.Fatalf("roundtrip mismatch: got %+v want %+v", back, p)
	}
}

func TestLLMRequestPayloadEmitsClassWhenSet(t *testing.T) {
	p := LLMRequestPayload{SlotID: 4, SlotClass: "hindsight", Messages: []ChatMsg{{Role: "user", Content: "hi"}}, Stream: false}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"slot_id":4,"slot_class":"hindsight","messages":[{"role":"user","content":"hi"}],"stream":false}`
	if got := string(data); got != want {
		t.Fatalf("want %q got %q", want, got)
	}
}

func TestSlotAssignedAckRoundTrip(t *testing.T) {
	msg, err := NewMessage(TypeSlotAssignedAck, "main-agent", SlotAssignedAckPayload{
		AgentID: "main-agent",
		SlotID:  2,
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if msg.Type != TypeSlotAssignedAck {
		t.Fatalf("type: want %q got %q", TypeSlotAssignedAck, msg.Type)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var payload SlotAssignedAckPayload
	if err := decoded.DecodePayload(&payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.AgentID != "main-agent" || payload.SlotID != 2 {
		t.Fatalf("payload mismatch: %+v", payload)
	}
}
