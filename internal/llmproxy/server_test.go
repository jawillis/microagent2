package llmproxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"microagent2/internal/messaging"
)

func TestDecodeToolChoiceString(t *testing.T) {
	if got := decodeToolChoice(json.RawMessage(`"auto"`)); got != "auto" {
		t.Fatalf("want auto, got %q", got)
	}
	if got := decodeToolChoice(json.RawMessage(`"none"`)); got != "none" {
		t.Fatalf("want none, got %q", got)
	}
}

func TestDecodeToolChoiceObjectPassthrough(t *testing.T) {
	raw := json.RawMessage(`{"type":"function","function":{"name":"search"}}`)
	got := decodeToolChoice(raw)
	if got != string(raw) {
		t.Fatalf("want object passthrough, got %q", got)
	}
}

func TestDecodeToolChoiceEmpty(t *testing.T) {
	if got := decodeToolChoice(nil); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
	if got := decodeToolChoice(json.RawMessage(``)); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestFinishReason(t *testing.T) {
	if finishReason(nil) != "stop" {
		t.Fatalf("empty tools should stop")
	}
	if finishReason([]messaging.ToolCall{{ID: "1"}}) != "tool_calls" {
		t.Fatalf("with tools should be tool_calls")
	}
}

func TestWriteJSONError(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSONError(w, http.StatusServiceUnavailable, "slot_unavailable", "timeout")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d", w.Code)
	}
	var body errorBody
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "slot_unavailable" || body.Error.Message != "timeout" {
		t.Fatalf("body mismatch: %+v", body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestHealthHandler(t *testing.T) {
	// Server with no messaging client — health should still respond.
	s := New(nil, Config{Identity: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestChatCompletionsRejectsInvalidJSON(t *testing.T) {
	s := New(nil, Config{Identity: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d (want 400)", rec.Code)
	}
	var body errorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "invalid_request" {
		t.Fatalf("code mismatch: %+v", body)
	}
}
