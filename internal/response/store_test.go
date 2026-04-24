package response

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	return NewStore(rdb), mr
}

func TestNewResponseID(t *testing.T) {
	id := NewResponseID()
	if len(id) < 6 || id[:5] != "resp_" {
		t.Errorf("expected resp_ prefix, got %q", id)
	}

	id2 := NewResponseID()
	if id == id2 {
		t.Error("expected unique IDs")
	}
}

func TestNewSessionID(t *testing.T) {
	id := NewSessionID()
	if len(id) == 0 {
		t.Error("expected non-empty session ID")
	}
}

func TestSaveAndGet(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	resp := &Response{
		ID:        "resp_test1",
		Input:     []InputItem{{Type: "message", Role: "user", Content: "hello"}},
		Output:    []OutputItem{{Type: "message", Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "hi"}}}},
		SessionID: "sess-1",
		Model:     "test-model",
		CreatedAt: "2025-01-01T00:00:00Z",
		Status:    StatusCompleted,
	}

	if err := store.Save(ctx, resp); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.Get(ctx, "resp_test1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected response, got nil")
	}
	if got.ID != "resp_test1" {
		t.Errorf("expected id resp_test1, got %q", got.ID)
	}
	if got.SessionID != "sess-1" {
		t.Errorf("expected session sess-1, got %q", got.SessionID)
	}
	if got.Status != StatusCompleted {
		t.Errorf("expected completed, got %q", got.Status)
	}
	if len(got.Input) != 1 || len(got.Output) != 1 {
		t.Errorf("expected 1 input and 1 output, got %d/%d", len(got.Input), len(got.Output))
	}
}

func TestGet_NotFound(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	got, err := store.Get(ctx, "resp_nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Error("expected nil for missing response")
	}
}

func TestWalkChain(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	r1 := &Response{ID: "resp_1", Input: []InputItem{{Type: "message", Role: "user", Content: "first"}}, Output: []OutputItem{{Type: "message", Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "reply1"}}}}, SessionID: "sess-chain", Model: "test", CreatedAt: "2025-01-01T00:00:00Z", Status: StatusCompleted}
	r2 := &Response{ID: "resp_2", PreviousResponseID: "resp_1", Input: []InputItem{{Type: "message", Role: "user", Content: "second"}}, Output: []OutputItem{{Type: "message", Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "reply2"}}}}, SessionID: "sess-chain", Model: "test", CreatedAt: "2025-01-01T00:01:00Z", Status: StatusCompleted}
	r3 := &Response{ID: "resp_3", PreviousResponseID: "resp_2", Input: []InputItem{{Type: "message", Role: "user", Content: "third"}}, Output: []OutputItem{{Type: "message", Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "reply3"}}}}, SessionID: "sess-chain", Model: "test", CreatedAt: "2025-01-01T00:02:00Z", Status: StatusCompleted}

	for _, r := range []*Response{r1, r2, r3} {
		if err := store.Save(ctx, r); err != nil {
			t.Fatalf("save %s: %v", r.ID, err)
		}
	}

	chain, err := store.WalkChain(ctx, "resp_3")
	if err != nil {
		t.Fatalf("walk chain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("expected 3 responses in chain, got %d", len(chain))
	}
	if chain[0].ID != "resp_1" || chain[1].ID != "resp_2" || chain[2].ID != "resp_3" {
		t.Errorf("chain order wrong: %s, %s, %s", chain[0].ID, chain[1].ID, chain[2].ID)
	}
}

func TestWalkChain_BrokenChain(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	r := &Response{ID: "resp_broken", PreviousResponseID: "resp_missing", Input: []InputItem{{Type: "message", Role: "user", Content: "x"}}, Output: []OutputItem{}, SessionID: "s", Model: "test", CreatedAt: "2025-01-01T00:00:00Z", Status: StatusCompleted}
	if err := store.Save(ctx, r); err != nil {
		t.Fatal(err)
	}

	_, err := store.WalkChain(ctx, "resp_broken")
	if err == nil {
		t.Error("expected error for broken chain")
	}
}

func TestInheritSessionID(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	r := &Response{ID: "resp_inherit", SessionID: "inherited-sess", Input: []InputItem{}, Output: []OutputItem{}, Model: "test", CreatedAt: "2025-01-01T00:00:00Z", Status: StatusCompleted}
	if err := store.Save(ctx, r); err != nil {
		t.Fatal(err)
	}

	sid, err := store.InheritSessionID(ctx, "resp_inherit")
	if err != nil {
		t.Fatalf("inherit: %v", err)
	}
	if sid != "inherited-sess" {
		t.Errorf("expected inherited-sess, got %q", sid)
	}
}

func TestInheritSessionID_NotFound(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	_, err := store.InheritSessionID(ctx, "resp_nope")
	if err == nil {
		t.Error("expected error for missing response")
	}
}

func TestSessionHistory(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	r1 := &Response{ID: "resp_h1", SessionID: "sess-hist", Input: []InputItem{{Type: "message", Role: "user", Content: "msg1"}}, Output: []OutputItem{{Type: "message", Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "r1"}}}}, Model: "test", CreatedAt: "2025-01-01T00:00:00Z", Status: StatusCompleted}
	r2 := &Response{ID: "resp_h2", PreviousResponseID: "resp_h1", SessionID: "sess-hist", Input: []InputItem{{Type: "message", Role: "user", Content: "msg2"}}, Output: []OutputItem{{Type: "message", Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "r2"}}}}, Model: "test", CreatedAt: "2025-01-01T00:01:00Z", Status: StatusCompleted}

	for _, r := range []*Response{r1, r2} {
		if err := store.Save(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	history, err := store.GetSessionHistory(ctx, "sess-hist")
	if err != nil {
		t.Fatalf("get session history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(history))
	}
	if history[0].ID != "resp_h1" || history[1].ID != "resp_h2" {
		t.Error("history order wrong")
	}
}

func TestDeleteSession(t *testing.T) {
	store, mr := newTestStore(t)
	ctx := context.Background()

	r := &Response{ID: "resp_del", SessionID: "sess-del", Input: []InputItem{}, Output: []OutputItem{}, Model: "test", CreatedAt: "2025-01-01T00:00:00Z", Status: StatusCompleted}
	if err := store.Save(ctx, r); err != nil {
		t.Fatal(err)
	}

	exists, _ := store.SessionExists(ctx, "sess-del")
	if !exists {
		t.Fatal("session should exist before delete")
	}

	if err := store.DeleteSession(ctx, "sess-del"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	exists, _ = store.SessionExists(ctx, "sess-del")
	if exists {
		t.Error("session should not exist after delete")
	}

	if mr.Exists("response:resp_del") {
		t.Error("response hash should be deleted")
	}
}

func TestListSessions(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	r1 := &Response{ID: "resp_ls1", SessionID: "sess-ls-a", Input: []InputItem{}, Output: []OutputItem{}, Model: "test", CreatedAt: "2025-01-01T00:00:00Z", Status: StatusCompleted}
	r2 := &Response{ID: "resp_ls2", SessionID: "sess-ls-b", Input: []InputItem{}, Output: []OutputItem{}, Model: "test", CreatedAt: "2025-01-02T00:00:00Z", Status: StatusCompleted}

	for _, r := range []*Response{r1, r2} {
		if err := store.Save(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	sessions, err := store.ListSessions(ctx)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	found := map[string]bool{}
	for _, s := range sessions {
		found[s.SessionID] = true
		if s.TurnCount != 1 {
			t.Errorf("expected turn count 1 for %s, got %d", s.SessionID, s.TurnCount)
		}
	}
	if !found["sess-ls-a"] || !found["sess-ls-b"] {
		t.Error("missing expected session IDs")
	}
}

func TestSessionExists(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	exists, _ := store.SessionExists(ctx, "nonexistent")
	if exists {
		t.Error("expected false for nonexistent session")
	}

	r := &Response{ID: "resp_ex", SessionID: "sess-ex", Input: []InputItem{}, Output: []OutputItem{}, Model: "test", CreatedAt: "2025-01-01T00:00:00Z", Status: StatusCompleted}
	if err := store.Save(ctx, r); err != nil {
		t.Fatal(err)
	}

	exists, _ = store.SessionExists(ctx, "sess-ex")
	if !exists {
		t.Error("expected true for existing session")
	}
}
