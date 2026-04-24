package context

import (
	"strings"
	"testing"

	"microagent2/internal/messaging"
)

const testSystemPrompt = "You are a terse assistant. Reply in one short sentence."

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAssemble_SystemPromptByteStable(t *testing.T) {
	a := NewAssembler(testSystemPrompt)
	user := messaging.ChatMsg{Role: "user", Content: "hi"}

	firstCall := a.Assemble([]Memory{{Content: "m1", Score: 0.9}}, nil, user)
	secondCall := a.Assemble([]Memory{{Content: "m2", Score: 0.5}, {Content: "m3", Score: 0.3}}, nil, user)
	thirdCall := a.Assemble(nil, nil, user)

	for i, got := range []string{firstCall[0].Content, secondCall[0].Content, thirdCall[0].Content} {
		if got != testSystemPrompt {
			t.Fatalf("call %d: system content = %q, want %q", i+1, got, testSystemPrompt)
		}
	}
}

func TestAssemble_MemoriesFoldedIntoUserTurn(t *testing.T) {
	a := NewAssembler(testSystemPrompt)
	memories := []Memory{
		{Content: "A", Score: 0.9},
		{Content: "B", Score: 0.7},
	}
	user := messaging.ChatMsg{Role: "user", Content: "original"}

	out := a.Assemble(memories, nil, user)

	last := out[len(out)-1]
	if last.Role != "user" {
		t.Fatalf("last role = %q, want user", last.Role)
	}
	want := "<context>\n- A\n- B\n</context>\n\noriginal"
	if last.Content != want {
		t.Fatalf("last content = %q, want %q", last.Content, want)
	}
}

func TestAssemble_MemoriesSortedByScoreDesc(t *testing.T) {
	a := NewAssembler(testSystemPrompt)
	memories := []Memory{
		{Content: "low", Score: 0.4},
		{Content: "high", Score: 0.9},
		{Content: "mid", Score: 0.7},
	}
	user := messaging.ChatMsg{Role: "user", Content: "q"}

	out := a.Assemble(memories, nil, user)

	want := "<context>\n- high\n- mid\n- low\n</context>\n\nq"
	if got := out[len(out)-1].Content; got != want {
		t.Fatalf("user content = %q, want %q", got, want)
	}
}

func TestAssemble_EmptyRecallPassesUserThrough(t *testing.T) {
	a := NewAssembler(testSystemPrompt)
	user := messaging.ChatMsg{Role: "user", Content: "hello"}

	for _, name := range []string{"nil", "empty"} {
		var memories []Memory
		if name == "empty" {
			memories = []Memory{}
		}
		out := a.Assemble(memories, nil, user)
		last := out[len(out)-1]
		if last.Content != "hello" {
			t.Fatalf("%s memories: content = %q, want %q", name, last.Content, "hello")
		}
		if strings.Contains(last.Content, "<context>") {
			t.Fatalf("%s memories: content unexpectedly contains <context>: %q", name, last.Content)
		}
	}
}

func TestAssemble_OnlyContentRendered(t *testing.T) {
	a := NewAssembler(testSystemPrompt)
	memories := []Memory{{
		ID:      "m1",
		Content: "the only thing that should appear",
		Score:   0.99,
		Tags:    []string{"SHOULD_NOT_APPEAR_TAG"},
	}}
	user := messaging.ChatMsg{Role: "user", Content: "q"}

	out := a.Assemble(memories, nil, user)

	want := "- the only thing that should appear\n"
	if !strings.Contains(out[len(out)-1].Content, want) {
		t.Fatalf("expected rendered line %q in %q", want, out[len(out)-1].Content)
	}

	for _, msg := range out {
		for _, forbidden := range []string{"SHOULD_NOT_APPEAR_TAG", "0.99"} {
			if strings.Contains(msg.Content, forbidden) {
				t.Fatalf("forbidden field %q leaked into assembled message: %q", forbidden, msg.Content)
			}
		}
	}
}

func TestAssemble_AtMostOneSystemMessage(t *testing.T) {
	a := NewAssembler(testSystemPrompt)
	history := []messaging.ChatMsg{
		{Role: "user", Content: "earlier user"},
		{Role: "assistant", Content: "earlier assistant"},
	}
	user := messaging.ChatMsg{Role: "user", Content: "now"}

	out := a.Assemble([]Memory{{Content: "m", Score: 0.5}}, history, user)

	systemCount := 0
	for i, msg := range out {
		if msg.Role == "system" {
			systemCount++
			if i != 0 {
				t.Fatalf("system message at index %d, want 0", i)
			}
		}
	}
	if systemCount != 1 {
		t.Fatalf("system message count = %d, want 1", systemCount)
	}
}

func TestAssemble_PreservesToolFieldsInHistory(t *testing.T) {
	a := NewAssembler(testSystemPrompt)
	history := []messaging.ChatMsg{
		{Role: "user", Content: "pls"},
		{Role: "assistant", ToolCalls: []messaging.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: messaging.ToolCallFunction{Name: "list_skills", Arguments: "{}"},
		}}},
		{Role: "tool", ToolCallID: "call_1", Content: "skill list output"},
		{Role: "assistant", Content: "here you go"},
	}
	user := messaging.ChatMsg{Role: "user", Content: "follow-up"}

	out := a.Assemble(nil, history, user)

	// system + history(4) + user = 6
	if len(out) != 6 {
		t.Fatalf("len: %d %+v", len(out), out)
	}
	if out[2].Role != "assistant" || len(out[2].ToolCalls) != 1 || out[2].ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant tool_calls lost: %+v", out[2])
	}
	if out[3].Role != "tool" || out[3].ToolCallID != "call_1" || out[3].Content != "skill list output" {
		t.Fatalf("tool result lost: %+v", out[3])
	}
	if out[5].Role != "user" || out[5].Content != "follow-up" {
		t.Fatalf("user turn wrong: %+v", out[5])
	}
	// System prompt invariant holds
	if out[0].Content != testSystemPrompt {
		t.Fatalf("system prompt drifted")
	}
}

func TestAssemble_DoesNotMutateInputs(t *testing.T) {
	a := NewAssembler(testSystemPrompt)
	memories := []Memory{
		{Content: "low", Score: 0.4},
		{Content: "high", Score: 0.9},
	}
	user := messaging.ChatMsg{Role: "user", Content: "original"}

	snapshotMem := make([]Memory, len(memories))
	copy(snapshotMem, memories)
	snapshotUser := user

	_ = a.Assemble(memories, nil, user)

	for i, m := range memories {
		s := snapshotMem[i]
		if m.ID != s.ID || m.Content != s.Content || m.Score != s.Score || !sameStrings(m.Tags, s.Tags) {
			t.Fatalf("memories mutated at index %d: before=%+v after=%+v", i, s, m)
		}
	}
	if user.Role != snapshotUser.Role || user.Content != snapshotUser.Content || user.ToolCallID != snapshotUser.ToolCallID || len(user.ToolCalls) != len(snapshotUser.ToolCalls) {
		t.Fatalf("userMessage mutated: before=%+v after=%+v", snapshotUser, user)
	}
}
