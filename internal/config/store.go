package config

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

const (
	KeyChat   = "config:chat"
	KeyMemory = "config:memory"
	KeyBroker = "config:broker"
	KeyRetro  = "config:retro"
)

var validSections = map[string]string{
	"chat":   KeyChat,
	"memory": KeyMemory,
	"broker": KeyBroker,
	"retro":  KeyRetro,
}

type Store struct {
	rdb *redis.Client
}

func NewStore(rdb *redis.Client) *Store {
	return &Store{rdb: rdb}
}

func ValidSection(section string) bool {
	_, ok := validSections[section]
	return ok
}

func (s *Store) Load(ctx context.Context, key string, dest any) error {
	data, err := s.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), dest)
}

func (s *Store) Save(ctx context.Context, section string, values json.RawMessage) error {
	key, ok := validSections[section]
	if !ok {
		return ErrInvalidSection
	}

	existing, err := s.rdb.Get(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return err
	}

	merged := make(map[string]any)
	if existing != "" {
		if err := json.Unmarshal([]byte(existing), &merged); err != nil {
			return err
		}
	}

	var incoming map[string]any
	if err := json.Unmarshal(values, &incoming); err != nil {
		return err
	}
	for k, v := range incoming {
		merged[k] = v
	}

	data, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, key, data, 0).Err()
}

func (s *Store) ReadAll(ctx context.Context) (map[string]json.RawMessage, error) {
	result := make(map[string]json.RawMessage)
	for section, key := range validSections {
		data, err := s.rdb.Get(ctx, key).Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		result[section] = json.RawMessage(data)
	}
	return result, nil
}
