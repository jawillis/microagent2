package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"microagent2/internal/messaging"
)

// handleBrokerSlots fetches the broker's current slot table via a
// messaging request/reply. Returns JSON {"slots": [...]} on success,
// 503 if the broker doesn't reply within the deadline.
func (s *Server) handleBrokerSlots(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	replyStream := fmt.Sprintf("stream:reply:broker-slots:%d", time.Now().UnixNano())

	req, err := messaging.NewMessage(messaging.TypeSlotSnapshotRequest, "gateway", messaging.SlotSnapshotRequestPayload{})
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	req.ReplyStream = replyStream

	if _, err := s.client.Publish(ctx, messaging.StreamBrokerSlotSnapshot, req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "broker_unreachable", "detail": err.Error()})
		return
	}

	reply, err := s.client.WaitForReply(ctx, replyStream, req.CorrelationID, 5*time.Second)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "broker_unreachable"})
		return
	}
	var payload messaging.SlotSnapshotResponsePayload
	if err := reply.DecodePayload(&payload); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "broker_bad_reply"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"slots": payload.Slots})
}