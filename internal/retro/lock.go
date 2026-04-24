package retro

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const lockTTL = 300 * time.Second

func lockKey(sessionID string, jobType JobType) string {
	return fmt.Sprintf("retro:lock:%s:%s", sessionID, jobType)
}

func AcquireLock(ctx context.Context, rdb *redis.Client, sessionID string, jobType JobType) (bool, error) {
	ok, err := rdb.SetNX(ctx, lockKey(sessionID, jobType), "locked", lockTTL).Result()
	if err != nil {
		return false, fmt.Errorf("acquire retro lock: %w", err)
	}
	return ok, nil
}

func ReleaseLock(ctx context.Context, rdb *redis.Client, sessionID string, jobType JobType) {
	_ = rdb.Del(ctx, lockKey(sessionID, jobType)).Err()
}
