package memoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetain(t *testing.T) {
	var got RetainRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/retain" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"success":true,"bank_id":"microagent2","items_count":1,"async":false}`)
	}))
	defer srv.Close()
	c := New(srv.URL)
	_, err := c.Retain(context.Background(), RetainRequest{
		Content: "hello",
		Tags:    []string{"preferences"},
		Metadata: map[string]string{"provenance": "explicit", "confidence": "0.9"},
	})
	if err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if got.Content != "hello" || got.Metadata["confidence"] != "0.9" {
		t.Fatalf("payload: %+v", got)
	}
}

func TestRecall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"memories":[{"id":"m1","content":"hi","score":0.8,"tags":["preferences"]}]}`)
	}))
	defer srv.Close()
	resp, err := New(srv.URL).Recall(context.Background(), RecallRequest{Query: "hello", Limit: 3})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(resp.Memories) != 1 || resp.Memories[0].Content != "hi" {
		t.Fatalf("memories: %+v", resp.Memories)
	}
	if resp.Memories[0].Score != 0.8 {
		t.Fatalf("score: %v", resp.Memories[0].Score)
	}
}

func TestReflect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"text":"result"}`)
	}))
	defer srv.Close()
	resp, err := New(srv.URL).Reflect(context.Background(), ReflectRequest{Query: "x"})
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if resp.Text != "result" {
		t.Fatalf("text: %q", resp.Text)
	}
}

func TestForget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ForgetRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.MemoryID != "m42" {
			t.Errorf("memory_id: %q", req.MemoryID)
		}
		_, _ = io.WriteString(w, `{"deleted_id":"m42"}`)
	}))
	defer srv.Close()
	resp, err := New(srv.URL).Forget(context.Background(), ForgetRequest{MemoryID: "m42"})
	if err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if resp.DeletedID != "m42" {
		t.Fatalf("deleted_id: %q", resp.DeletedID)
	}
}

func TestCorrelationIDHeader(t *testing.T) {
	got := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Correlation-ID")
		_, _ = io.WriteString(w, `{"success":true,"bank_id":"b","items_count":0,"async":false}`)
	}))
	defer srv.Close()

	ctx := WithCorrelationID(context.Background(), "corr-abc")
	_, err := New(srv.URL).Retain(ctx, RetainRequest{Content: "x"})
	if err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if got != "corr-abc" {
		t.Fatalf("X-Correlation-ID = %q", got)
	}
}

func TestNon2xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv.Close()
	_, err := New(srv.URL).Recall(context.Background(), RecallRequest{Query: "x"})
	if err == nil {
		t.Fatal("want error")
	}
	var he *Error
	if !errors.As(err, &he) {
		t.Fatalf("not *Error: %v", err)
	}
	if he.StatusCode != 500 || !strings.Contains(he.Body, "boom") {
		t.Fatalf("err: %+v", he)
	}
}
