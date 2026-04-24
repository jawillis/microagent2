package context

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*MuninnClient, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	addr := strings.TrimPrefix(srv.URL, "http://")
	return NewMuninnClient(addr, "test-key", "test-vault", 0.5, 2, 0.9), srv.Close
}

func TestStore_OmitsEmptyOptionalFields(t *testing.T) {
	var got map[string]any
	client, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"new-id"}`))
	})
	defer closeFn()

	err := client.Store(context.Background(), StoredMemory{
		Concept: "c",
		Content: "x",
		Tags:    []string{"t"},
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	if _, present := got["summary"]; present {
		t.Errorf("summary unexpectedly present in payload: %v", got)
	}
	if _, present := got["memory_type"]; present {
		t.Errorf("memory_type unexpectedly present in payload: %v", got)
	}
	if _, present := got["type_label"]; present {
		t.Errorf("type_label unexpectedly present in payload: %v", got)
	}
	if _, present := got["confidence"]; present {
		t.Errorf("confidence unexpectedly present in payload: %v", got)
	}
	if got["concept"] != "c" || got["content"] != "x" {
		t.Errorf("required fields wrong: %v", got)
	}
}

func TestStore_IncludesAllFieldsWhenSet(t *testing.T) {
	var got map[string]any
	client, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"new-id"}`))
	})
	defer closeFn()

	err := client.Store(context.Background(), StoredMemory{
		Concept:    "headline",
		Content:    "full statement",
		Summary:    "brief",
		Tags:       []string{"a", "b"},
		MemoryType: "fact",
		Confidence: 0.75,
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	for _, key := range []string{"vault", "concept", "content", "summary", "tags", "memory_type", "type_label", "confidence"} {
		if _, ok := got[key]; !ok {
			t.Errorf("expected key %q in payload; got %v", key, got)
		}
	}
	if got["memory_type"].(float64) != 0 {
		t.Errorf("memory_type = %v, want 0 (fact)", got["memory_type"])
	}
	if got["type_label"] != "fact" {
		t.Errorf("type_label = %v, want fact", got["type_label"])
	}
	if got["confidence"].(float64) != 0.75 {
		t.Errorf("confidence = %v, want 0.75", got["confidence"])
	}
}

func TestConsolidate_RequestShape(t *testing.T) {
	var gotURL string
	var got map[string]any
	client, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"merged-id"}`))
	})
	defer closeFn()

	id, err := client.Consolidate(context.Background(), []string{"a", "b"}, "merged")
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if id != "merged-id" {
		t.Errorf("id = %q, want merged-id", id)
	}
	if gotURL != "/api/consolidate" {
		t.Errorf("URL = %q, want /api/consolidate", gotURL)
	}
	if got["vault"] != "test-vault" {
		t.Errorf("vault = %v, want test-vault", got["vault"])
	}
	if got["merged_content"] != "merged" {
		t.Errorf("merged_content = %v, want merged", got["merged_content"])
	}
	ids, _ := got["ids"].([]any)
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("ids = %v, want [a b]", ids)
	}
}

func TestEvolve_RequestShape(t *testing.T) {
	var gotURL string
	var got map[string]any
	client, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"new-id"}`))
	})
	defer closeFn()

	id, err := client.Evolve(context.Background(), "old-id", "refined", "summary")
	if err != nil {
		t.Fatalf("Evolve: %v", err)
	}
	if id != "new-id" {
		t.Errorf("id = %q, want new-id", id)
	}
	if gotURL != "/api/engrams/old-id/evolve" {
		t.Errorf("URL = %q, want /api/engrams/old-id/evolve", gotURL)
	}
	if got["content"] != "refined" {
		t.Errorf("content = %v, want refined", got["content"])
	}
	if got["summary"] != "summary" {
		t.Errorf("summary = %v, want summary", got["summary"])
	}
	if got["vault"] != "test-vault" {
		t.Errorf("vault = %v, want test-vault", got["vault"])
	}
}

func TestDelete_RequestShape(t *testing.T) {
	var gotMethod, gotURL string
	client, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURL = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	defer closeFn()

	if err := client.Delete(context.Background(), "some-id"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotURL != "/api/engrams/some-id" {
		t.Errorf("URL = %q, want /api/engrams/some-id", gotURL)
	}
}

func TestLink_RequestShape(t *testing.T) {
	var gotURL string
	var got map[string]any
	client, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	})
	defer closeFn()

	if err := client.Link(context.Background(), "src", "tgt", 4, 0.5); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if gotURL != "/api/link" {
		t.Errorf("URL = %q, want /api/link", gotURL)
	}
	if got["source_id"] != "src" || got["target_id"] != "tgt" {
		t.Errorf("source/target wrong: %v", got)
	}
	if got["rel_type"].(float64) != 4 {
		t.Errorf("rel_type = %v, want 4", got["rel_type"])
	}
	if got["weight"].(float64) != 0.5 {
		t.Errorf("weight = %v, want 0.5", got["weight"])
	}
	if got["vault"] != "test-vault" {
		t.Errorf("vault = %v, want test-vault", got["vault"])
	}
}

func TestRecall_PopulatesID(t *testing.T) {
	client, closeFn := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"activations":[{"id":"engram-1","score":0.9,"content":"c","concept":"x","summary":"s"}]}`))
	})
	defer closeFn()

	mems, err := client.Recall(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("len(mems) = %d, want 1", len(mems))
	}
	if mems[0].ID != "engram-1" {
		t.Errorf("ID = %q, want engram-1", mems[0].ID)
	}
}
