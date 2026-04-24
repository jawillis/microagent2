package messaging

import (
	"encoding/json"
	"testing"
)

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
