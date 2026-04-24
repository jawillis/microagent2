package messaging

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type MessageType string

const (
	TypeSlotRequest     MessageType = "slot_request"
	TypeSlotAssigned    MessageType = "slot_assigned"
	TypeSlotAssignedAck MessageType = "slot_assigned_ack"
	TypeSlotRelease     MessageType = "slot_release"
	TypeSlotAvailable   MessageType = "slot_available"
	TypePreempt         MessageType = "preempt"
	TypePreemptAck      MessageType = "preempt_ack"
	TypeRegister        MessageType = "register"
	TypeDeregister      MessageType = "deregister"
	TypeHeartbeat       MessageType = "heartbeat"
	TypeChatRequest     MessageType = "chat_request"
	TypeChatResponse    MessageType = "chat_response"
	TypeToken           MessageType = "token"
	TypeTokenDone       MessageType = "token_done"
	TypeSessionEvent    MessageType = "session_event"
	TypeContextAssembled MessageType = "context_assembled"
	TypeRetroTrigger     MessageType = "retro_trigger"
)

type Message struct {
	Type          MessageType     `json:"type"`
	CorrelationID string          `json:"correlation_id"`
	Timestamp     int64           `json:"timestamp"`
	Source        string          `json:"source"`
	ReplyStream   string          `json:"reply_stream,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

func NewCorrelationID() string {
	return uuid.New().String()
}

func NewMessage(msgType MessageType, source string, payload any) (*Message, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Message{
		Type:          msgType,
		CorrelationID: NewCorrelationID(),
		Timestamp:     time.Now().UnixMilli(),
		Source:        source,
		Payload:       p,
	}, nil
}

func NewReply(original *Message, msgType MessageType, source string, payload any) (*Message, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Message{
		Type:          msgType,
		CorrelationID: original.CorrelationID,
		Timestamp:     time.Now().UnixMilli(),
		Source:        source,
		Payload:       p,
	}, nil
}

func (m *Message) DecodePayload(v any) error {
	return json.Unmarshal(m.Payload, v)
}

func (m *Message) Encode() (map[string]any, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return map[string]any{"data": string(data)}, nil
}

func DecodeFromStream(values map[string]any) (*Message, error) {
	data, ok := values["data"].(string)
	if !ok {
		return nil, ErrInvalidMessage
	}
	var msg Message
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
