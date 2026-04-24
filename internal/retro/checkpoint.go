package retro

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
)

type Checkpoint struct {
	ProcessedTurns int `json:"processed_turns"`
}

type CheckpointStore struct {
	rdb *redis.Client
	mu  sync.Mutex
}

func NewCheckpointStore(rdb *redis.Client) *CheckpointStore {
	return &CheckpointStore{rdb: rdb}
}

func checkpointKey(sessionID string, jobType JobType) string {
	return fmt.Sprintf("retro:checkpoint:%s:%s", sessionID, jobType)
}

func (s *CheckpointStore) Save(sessionID string, jobType JobType, cp *Checkpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(cp)
	if err != nil {
		return
	}
	_ = s.rdb.Set(context.Background(), checkpointKey(sessionID, jobType), string(data), 0).Err()
}

func (s *CheckpointStore) Load(sessionID string, jobType JobType) *Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()

	val, err := s.rdb.Get(context.Background(), checkpointKey(sessionID, jobType)).Result()
	if err != nil {
		return nil
	}
	var cp Checkpoint
	if err := json.Unmarshal([]byte(val), &cp); err != nil {
		return nil
	}
	return &cp
}

func (s *CheckpointStore) Clear(sessionID string, jobType JobType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.rdb.Del(context.Background(), checkpointKey(sessionID, jobType)).Err()
}
