package messaging

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

type ConsumeStats struct {
	Handled       atomic.Uint64
	HandlerErrors atomic.Uint64
	Recoveries    atomic.Uint64
	PhantomLogs   atomic.Uint64
}

type ConsumeHandler func(ctx context.Context, msg *Message) error

const (
	consumeBackoffStart     = 100 * time.Millisecond
	consumeBackoffCap       = 5 * time.Second
	consumeDegradedAfter    = 10
	consumeRecoveryLogEvery = 10 * time.Second
)

func phantomCheckInterval() time.Duration {
	if v := os.Getenv("CONSUME_PHANTOM_CHECK_S"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 30 * time.Second
}

// ConsumeStream runs a resilient consumer-group read loop for the given stream.
// Returns only on ctx.Done() or an unrecoverable error class.
func (c *Client) ConsumeStream(
	ctx context.Context,
	stream, group, consumer string,
	count int64,
	block time.Duration,
	handler ConsumeHandler,
	logger *slog.Logger,
	stats *ConsumeStats,
) error {
	if stats == nil {
		stats = &ConsumeStats{}
	}
	if logger == nil {
		logger = slog.Default()
	}

	if err := c.EnsureGroup(ctx, stream, group); err != nil {
		return err
	}

	backoff := consumeBackoffStart
	unknownErrorStreak := 0
	lastRecoveryLog := time.Time{}

	ticker := time.NewTicker(phantomCheckInterval())
	defer ticker.Stop()

	lastHandledBaseline := uint64(0)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.checkPhantomConsume(ctx, stream, group, stats, &lastHandledBaseline, logger)
			logger.Info("consume_stream_stats",
				"stream", stream,
				"group", group,
				"handled", stats.Handled.Load(),
				"handler_errors", stats.HandlerErrors.Load(),
				"recoveries", stats.Recoveries.Load(),
				"phantom_logs", stats.PhantomLogs.Load(),
			)
		default:
		}

		msgs, ids, err := c.ReadGroup(ctx, stream, group, consumer, count, block)
		if err != nil {
			switch classifyError(err) {
			case errClassNil:
				// normal: no messages in block window
				unknownErrorStreak = 0
				backoff = consumeBackoffStart
				continue
			case errClassNoGroup:
				stats.Recoveries.Add(1)
				if time.Since(lastRecoveryLog) > consumeRecoveryLogEvery {
					logger.Warn("consumer_group_missing_recovering",
						"stream", stream,
						"group", group,
						"error", err.Error(),
					)
					lastRecoveryLog = time.Now()
				} else {
					logger.Debug("consumer_group_missing_recovering",
						"stream", stream, "group", group, "error", err.Error())
				}
				if recErr := c.EnsureGroup(ctx, stream, group); recErr != nil {
					logger.Error("consumer_group_recreate_failed",
						"stream", stream, "group", group, "error", recErr.Error())
					sleepOrDone(ctx, backoff)
					backoff = nextBackoff(backoff)
				}
				continue
			default:
				unknownErrorStreak++
				logger.Warn("consume_stream_error",
					"stream", stream, "group", group, "error", err.Error(), "streak", unknownErrorStreak)
				if unknownErrorStreak == consumeDegradedAfter {
					logger.Error("consumer_loop_degraded",
						"stream", stream, "group", group, "error", err.Error(), "streak", unknownErrorStreak)
				}
				if sleepOrDone(ctx, backoff) {
					return ctx.Err()
				}
				backoff = nextBackoff(backoff)
				continue
			}
		}

		unknownErrorStreak = 0
		backoff = consumeBackoffStart

		for i, msg := range msgs {
			id := ids[i]
			handlerErr := handler(ctx, msg)
			if handlerErr != nil {
				stats.HandlerErrors.Add(1)
				logger.Error("consume_handler_error",
					"stream", stream, "group", group, "id", id,
					"correlation_id", msg.CorrelationID,
					"error", handlerErr.Error(),
				)
			} else {
				stats.Handled.Add(1)
			}
			// Always ACK. We rely on the handler being the final line of defense
			// for this message. Leaving messages in PEL doesn't help us because
			// XREADGROUP > never redelivers them — they'd just block the queue.
			if ackErr := c.Ack(ctx, stream, group, id); ackErr != nil {
				logger.Warn("consume_ack_failed",
					"stream", stream, "group", group, "id", id, "error", ackErr.Error())
			}
		}
	}
}

type errClass int

const (
	errClassUnknown errClass = iota
	errClassNil
	errClassNoGroup
)

func classifyError(err error) errClass {
	if err == nil {
		return errClassNil
	}
	if errors.Is(err, redis.Nil) {
		return errClassNil
	}
	text := err.Error()
	if strings.HasPrefix(text, "NOGROUP") || strings.Contains(text, "no such key") {
		return errClassNoGroup
	}
	return errClassUnknown
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > consumeBackoffCap {
		return consumeBackoffCap
	}
	return next
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

func (c *Client) checkPhantomConsume(
	ctx context.Context,
	stream, group string,
	stats *ConsumeStats,
	lastBaseline *uint64,
	logger *slog.Logger,
) {
	groups, err := c.rdb.XInfoGroups(ctx, stream).Result()
	if err != nil {
		return
	}
	for _, g := range groups {
		if g.Name != group {
			continue
		}
		entriesRead := uint64(0)
		if g.EntriesRead > 0 {
			entriesRead = uint64(g.EntriesRead)
		}
		handled := stats.Handled.Load()
		if entriesRead <= handled {
			*lastBaseline = handled
			return
		}
		// Delta > 0. Require persistence across two consecutive checks.
		if *lastBaseline == handled && entriesRead > handled {
			delta := entriesRead - handled
			logger.Warn("phantom_consume_detected",
				"stream", stream, "group", group,
				"entries_read", entriesRead,
				"handled_count", handled,
				"delta", delta,
			)
			stats.PhantomLogs.Add(1)
		}
		*lastBaseline = handled
		return
	}
}
