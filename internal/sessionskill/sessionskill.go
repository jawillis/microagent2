// Package sessionskill stores the "active skill" for a conversation session
// in Valkey. Scoped narrowly to skill state for now; rename to sessionstate
// when/if a second per-session concern lands.
package sessionskill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Key returns the Valkey key used to store a session's active skill.
func Key(sessionID string) string {
	return fmt.Sprintf("session:%s:active-skill", sessionID)
}

// Get returns the active skill name for sessionID, or "" if no active skill
// is set. A missing key is NOT an error; only Valkey transport failures are.
func Get(ctx context.Context, rdb *redis.Client, sessionID string) (string, error) {
	if sessionID == "" {
		return "", errors.New("sessionskill: sessionID required")
	}
	v, err := rdb.Get(ctx, Key(sessionID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", nil
		}
		return "", err
	}
	return v, nil
}

// Set writes the active skill name for sessionID with the given TTL. If name
// is empty, the key is deleted instead (explicit clear). TTL is ignored on
// clear; if non-positive on set, a 24h default is used.
func Set(ctx context.Context, rdb *redis.Client, sessionID, name string, ttl time.Duration) error {
	if sessionID == "" {
		return errors.New("sessionskill: sessionID required")
	}
	if name == "" {
		return rdb.Del(ctx, Key(sessionID)).Err()
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return rdb.Set(ctx, Key(sessionID), name, ttl).Err()
}
