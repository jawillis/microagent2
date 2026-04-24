package context

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"microagent2/internal/config"
	"microagent2/internal/memoryclient"
	"microagent2/internal/messaging"
	"microagent2/internal/response"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type recallCapture struct {
	mu       sync.Mutex
	requests []memoryclient.RecallRequest
	skipped  bool
}

func (rc *recallCapture) last() (memoryclient.RecallRequest, bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.requests) == 0 {
		return memoryclient.RecallRequest{}, false
	}
	return rc.requests[len(rc.requests)-1], true
}

func (rc *recallCapture) count() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.requests)
}

func newTestManager(t *testing.T, memSrv *httptest.Server, scope, primaryUserID string) (*Manager, *miniredis.Miniredis) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	if scope != "" || primaryUserID != "" {
		memCfg := config.MemoryConfig{
			RecallDefaultSpeakerScope: scope,
			PrimaryUserID:             primaryUserID,
		}
		data, _ := json.Marshal(memCfg)
		rdb.Set(context.Background(), config.KeyMemory, string(data), 0)
	}

	client := messaging.NewClient(mr.Addr())
	t.Cleanup(func() { client.Close() })

	responses := response.NewStore(rdb)
	mc := memoryclient.New(memSrv.URL)
	assembler := NewAssembler("test system prompt")
	cfgStore := config.NewStore(rdb)
	logger := slog.Default()

	return NewManager(client, responses, mc, assembler, logger, 5, 3, cfgStore), mr
}

func startRecallServer(t *testing.T, capture *recallCapture) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/recall" {
			var req memoryclient.RecallRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			capture.mu.Lock()
			capture.requests = append(capture.requests, req)
			capture.mu.Unlock()

			json.NewEncoder(w).Encode(memoryclient.RecallResponse{
				Memories: []memoryclient.MemorySummary{},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func buildTestMessage(t *testing.T, sessionID, speakerID, userContent string) *messaging.Message {
	t.Helper()
	payload := messaging.ChatRequestPayload{
		SessionID: sessionID,
		SpeakerID: speakerID,
		Messages: []messaging.ChatMsg{
			{Role: "user", Content: userContent},
		},
	}
	msg, err := messaging.NewMessage(messaging.TypeChatRequest, "test", payload)
	if err != nil {
		t.Fatal(err)
	}
	msg.ReplyStream = "stream:test:reply"
	return msg
}

func TestHandleRequest_ScopeAny_NoSpeakerFilter(t *testing.T) {
	capture := &recallCapture{}
	srv := startRecallServer(t, capture)
	mgr, _ := newTestManager(t, srv, "any", "")

	msg := buildTestMessage(t, "sess-1", "alice", "hello")
	mgr.handleRequest(context.Background(), msg)

	req, ok := capture.last()
	if !ok {
		t.Fatal("expected recall request, got none")
	}
	if req.SpeakerID != "" {
		t.Errorf("scope=any: expected empty SpeakerID, got %q", req.SpeakerID)
	}
	if req.Query != "hello" {
		t.Errorf("expected query %q, got %q", "hello", req.Query)
	}
}

func TestHandleRequest_ScopeAny_UnknownSpeaker(t *testing.T) {
	capture := &recallCapture{}
	srv := startRecallServer(t, capture)
	mgr, _ := newTestManager(t, srv, "any", "")

	msg := buildTestMessage(t, "sess-2", "", "hi")
	mgr.handleRequest(context.Background(), msg)

	req, ok := capture.last()
	if !ok {
		t.Fatal("expected recall request, got none")
	}
	if req.SpeakerID != "" {
		t.Errorf("scope=any + no speaker: expected empty SpeakerID, got %q", req.SpeakerID)
	}
}

func TestHandleRequest_ScopePrimary_UsesPayloadSpeaker(t *testing.T) {
	capture := &recallCapture{}
	srv := startRecallServer(t, capture)
	mgr, _ := newTestManager(t, srv, "primary", "jason")

	msg := buildTestMessage(t, "sess-3", "alice", "hi")
	mgr.handleRequest(context.Background(), msg)

	req, ok := capture.last()
	if !ok {
		t.Fatal("expected recall request, got none")
	}
	if req.SpeakerID != "alice" {
		t.Errorf("scope=primary + speaker=alice: expected SpeakerID=%q, got %q", "alice", req.SpeakerID)
	}
}

func TestHandleRequest_ScopePrimary_FallsToPrimaryUserID(t *testing.T) {
	capture := &recallCapture{}
	srv := startRecallServer(t, capture)
	mgr, _ := newTestManager(t, srv, "primary", "jason")

	msg := buildTestMessage(t, "sess-4", "", "hi")
	mgr.handleRequest(context.Background(), msg)

	req, ok := capture.last()
	if !ok {
		t.Fatal("expected recall request, got none")
	}
	if req.SpeakerID != "jason" {
		t.Errorf("scope=primary + no speaker + primary_user_id=jason: expected SpeakerID=%q, got %q", "jason", req.SpeakerID)
	}
}

func TestHandleRequest_ScopePrimary_UnknownSpeakerLiteral(t *testing.T) {
	capture := &recallCapture{}
	srv := startRecallServer(t, capture)
	mgr, _ := newTestManager(t, srv, "primary", "jason")

	msg := buildTestMessage(t, "sess-5", "unknown", "hi")
	mgr.handleRequest(context.Background(), msg)

	req, ok := capture.last()
	if !ok {
		t.Fatal("expected recall request, got none")
	}
	if req.SpeakerID != "jason" {
		t.Errorf("scope=primary + speaker=unknown + primary_user_id=jason: expected SpeakerID=%q, got %q", "jason", req.SpeakerID)
	}
}

func TestHandleRequest_ScopeExplicit_UsesSpeaker(t *testing.T) {
	capture := &recallCapture{}
	srv := startRecallServer(t, capture)
	mgr, _ := newTestManager(t, srv, "explicit", "")

	msg := buildTestMessage(t, "sess-6", "alice", "hi")
	mgr.handleRequest(context.Background(), msg)

	req, ok := capture.last()
	if !ok {
		t.Fatal("expected recall request, got none")
	}
	if req.SpeakerID != "alice" {
		t.Errorf("scope=explicit + speaker=alice: expected SpeakerID=%q, got %q", "alice", req.SpeakerID)
	}
}

func TestHandleRequest_ScopeExplicit_SkipsRecallOnUnknown(t *testing.T) {
	capture := &recallCapture{}
	srv := startRecallServer(t, capture)
	mgr, _ := newTestManager(t, srv, "explicit", "")

	msg := buildTestMessage(t, "sess-7", "", "hi")
	mgr.handleRequest(context.Background(), msg)

	if capture.count() != 0 {
		t.Errorf("scope=explicit + no speaker: expected recall to be skipped, got %d calls", capture.count())
	}
}

func TestHandleRequest_ScopeExplicit_SkipsRecallOnUnknownLiteral(t *testing.T) {
	capture := &recallCapture{}
	srv := startRecallServer(t, capture)
	mgr, _ := newTestManager(t, srv, "explicit", "")

	msg := buildTestMessage(t, "sess-8", "unknown", "hi")
	mgr.handleRequest(context.Background(), msg)

	if capture.count() != 0 {
		t.Errorf("scope=explicit + speaker=unknown: expected recall to be skipped, got %d calls", capture.count())
	}
}

func TestHandleRequest_EmitsSpeakerIDOnOutboundPayload(t *testing.T) {
	capture := &recallCapture{}
	srv := startRecallServer(t, capture)
	mgr, mr := newTestManager(t, srv, "any", "")

	msg := buildTestMessage(t, "sess-9", "alice", "hi")
	mgr.handleRequest(context.Background(), msg)

	agentStream := "stream:agent:main-agent:requests"
	entries, err := redis.NewClient(&redis.Options{Addr: mr.Addr()}).XRange(context.Background(), agentStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("reading agent stream: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected message on agent stream, got none")
	}

	data, ok := entries[0].Values["data"].(string)
	if !ok {
		t.Fatal("missing data field in stream entry")
	}
	var published messaging.Message
	if err := json.Unmarshal([]byte(data), &published); err != nil {
		t.Fatalf("decoding published message: %v", err)
	}
	var assembled messaging.ContextAssembledPayload
	if err := json.Unmarshal(published.Payload, &assembled); err != nil {
		t.Fatalf("decoding context assembled payload: %v", err)
	}
	if assembled.SpeakerID != "alice" {
		t.Errorf("expected outbound SpeakerID=%q, got %q", "alice", assembled.SpeakerID)
	}
}
