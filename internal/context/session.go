package context

import (
	"context"
	"encoding/json"
	"fmt"

	"microagent2/internal/messaging"
	"github.com/redis/go-redis/v9"
)

type SessionStore struct {
	rdb *redis.Client
}

func NewSessionStore(rdb *redis.Client) *SessionStore {
	return &SessionStore{rdb: rdb}
}

func sessionKey(sessionID string) string {
	return fmt.Sprintf("session:%s:history", sessionID)
}

func (s *SessionStore) Append(ctx context.Context, sessionID string, msg messaging.ChatMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.rdb.RPush(ctx, sessionKey(sessionID), string(data)).Err()
}

func (s *SessionStore) GetHistory(ctx context.Context, sessionID string) ([]messaging.ChatMsg, error) {
	raw, err := s.rdb.LRange(ctx, sessionKey(sessionID), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	msgs := make([]messaging.ChatMsg, 0, len(raw))
	for _, r := range raw {
		var msg messaging.ChatMsg
		if err := json.Unmarshal([]byte(r), &msg); err != nil {
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}
