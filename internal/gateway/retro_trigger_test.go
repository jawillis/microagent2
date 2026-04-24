package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"microagent2/internal/messaging"
)

// TestCompletedTurnPublishesSessionEvent ensures the retro-agent's inactivity
// trigger actually gets fed: every completed turn on /v1/responses or
// /v1/chat/completions must publish a SessionEventPayload on channel:events
// so the trigger arms its timer. Without this, retro only runs on manual
// dashboard triggers.
func TestCompletedTurnPublishesSessionEvent(t *testing.T) {
	srv, _ := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Subscribe BEFORE issuing the request. miniredis delivers synchronously
	// but the subscribe side still needs to be live.
	sub := srv.client.PubSubSubscribe(ctx, messaging.ChannelEvents)
	defer sub.Close()

	// Fake agent responds so the request can complete.
	group := fmt.Sprintf("cg:retrotest-%d", time.Now().UnixNano())
	_ = srv.client.EnsureGroup(ctx, messaging.StreamGatewayRequests, group)
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			msgs, ids, err := srv.client.ReadGroup(ctx, messaging.StreamGatewayRequests, group, "fake", 1, 200*time.Millisecond)
			if err != nil || len(msgs) == 0 {
				continue
			}
			req := msgs[0]
			_ = srv.client.Ack(ctx, messaging.StreamGatewayRequests, group, ids[0])
			reply, _ := messaging.NewReply(req, messaging.TypeChatResponse, "main-agent", messaging.ChatResponsePayload{
				Content: "ok",
				Done:    true,
			})
			_, _ = srv.client.Publish(ctx, req.ReplyStream, reply)
			return
		}
	}()

	body := `{"input":"hi","model":"test","stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	<-done

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}

	// Pick up the session id the gateway minted.
	var resp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SessionID == "" {
		t.Fatal("gateway did not return a session_id")
	}

	// Read one message off channel:events with a short timeout.
	select {
	case msg := <-sub.Channel():
		if msg == nil {
			t.Fatal("nil pub/sub message")
		}
		var m messaging.Message
		if err := json.Unmarshal([]byte(msg.Payload), &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m.Type != messaging.TypeSessionEvent {
			t.Fatalf("type: %q", m.Type)
		}
		var payload messaging.SessionEventPayload
		if err := m.DecodePayload(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.SessionID != resp.SessionID {
			t.Fatalf("session_id mismatch: got %q want %q", payload.SessionID, resp.SessionID)
		}
		if payload.Event != "turn_completed" {
			t.Fatalf("event: %q want turn_completed", payload.Event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no session event published on channel:events after turn completed — retro inactivity trigger will never fire")
	}
}
