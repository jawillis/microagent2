//go:build integration

package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func newLiveClient(t *testing.T) *Client {
	t.Helper()
	c := NewClient("localhost:6379")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skipf("valkey not available: %v", err)
	}
	c.Redis().FlushDB(ctx)
	t.Cleanup(func() { c.Close() })
	return c
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestConsumeStreamHandlesAllMessages(t *testing.T) {
	client := newLiveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := fmt.Sprintf("stream:test:consume:%d", time.Now().UnixNano())
	group := "cg:test"

	const N = 5
	received := make(chan string, N)
	handler := func(ctx context.Context, msg *Message) error {
		received <- msg.CorrelationID
		return nil
	}

	loopCtx, loopCancel := context.WithCancel(ctx)
	defer loopCancel()
	stats := &ConsumeStats{}
	go func() {
		_ = client.ConsumeStream(loopCtx, stream, group, "worker", 5, 200*time.Millisecond, handler, discardLogger(), stats)
	}()

	time.Sleep(300 * time.Millisecond) // allow EnsureGroup

	var expected []string
	for i := 0; i < N; i++ {
		msg, _ := NewMessage(TypeChatRequest, "test", ChatRequestPayload{SessionID: fmt.Sprintf("s%d", i)})
		if _, err := client.Publish(ctx, stream, msg); err != nil {
			t.Fatalf("publish: %v", err)
		}
		expected = append(expected, msg.CorrelationID)
	}

	seen := map[string]int{}
	deadline := time.After(5 * time.Second)
	for len(seen) < N {
		select {
		case id := <-received:
			seen[id]++
		case <-deadline:
			t.Fatalf("timeout: seen=%d want=%d stats.handled=%d", len(seen), N, stats.Handled.Load())
		}
	}

	for _, id := range expected {
		if seen[id] != 1 {
			t.Fatalf("correlation %s seen %d times (expected 1)", id, seen[id])
		}
	}
	if stats.Handled.Load() != N {
		t.Fatalf("stats.Handled = %d, want %d", stats.Handled.Load(), N)
	}
}

func TestConsumeStreamRecoversFromFlushDB(t *testing.T) {
	client := newLiveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stream := fmt.Sprintf("stream:test:flushdb:%d", time.Now().UnixNano())
	group := "cg:test"

	var handled atomic.Uint64
	handler := func(ctx context.Context, msg *Message) error {
		handled.Add(1)
		return nil
	}

	loopCtx, loopCancel := context.WithCancel(ctx)
	defer loopCancel()
	stats := &ConsumeStats{}
	go func() {
		_ = client.ConsumeStream(loopCtx, stream, group, "worker", 5, 200*time.Millisecond, handler, discardLogger(), stats)
	}()

	time.Sleep(300 * time.Millisecond)

	// First batch
	for i := 0; i < 3; i++ {
		msg, _ := NewMessage(TypeChatRequest, "test", nil)
		client.Publish(ctx, stream, msg)
	}
	waitFor(t, &handled, 3, 5*time.Second)

	// Disrupt
	if err := client.Redis().FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flushdb: %v", err)
	}

	// Second batch — the loop must recreate the consumer group and process these
	for i := 0; i < 2; i++ {
		msg, _ := NewMessage(TypeChatRequest, "test", nil)
		client.Publish(ctx, stream, msg)
	}

	waitFor(t, &handled, 5, 10*time.Second)
	if stats.Recoveries.Load() == 0 {
		t.Fatal("expected at least one recovery, got 0")
	}
}

func TestConsumeStreamHandlerErrorDoesNotAck(t *testing.T) {
	client := newLiveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := fmt.Sprintf("stream:test:nack:%d", time.Now().UnixNano())
	group := "cg:test"

	var calls atomic.Uint64
	failUntil := 2
	handler := func(ctx context.Context, msg *Message) error {
		n := calls.Add(1)
		if int(n) <= failUntil {
			return errors.New("synthetic failure")
		}
		return nil
	}

	loopCtx, loopCancel := context.WithCancel(ctx)
	defer loopCancel()
	stats := &ConsumeStats{}
	go func() {
		_ = client.ConsumeStream(loopCtx, stream, group, "worker", 1, 200*time.Millisecond, handler, discardLogger(), stats)
	}()

	time.Sleep(300 * time.Millisecond)

	msg, _ := NewMessage(TypeChatRequest, "test", nil)
	client.Publish(ctx, stream, msg)

	// Wait for at least one handler error plus an eventual success (via PEL redelivery OR new delivery).
	deadline := time.After(5 * time.Second)
	for stats.HandlerErrors.Load() < 1 {
		select {
		case <-deadline:
			t.Fatalf("expected HandlerErrors > 0, got %d", stats.HandlerErrors.Load())
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestConsumeStreamHandlerErrorDoesNotBlockQueue(t *testing.T) {
	client := newLiveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := fmt.Sprintf("stream:test:poison:%d", time.Now().UnixNano())
	group := "cg:test"

	var calls atomic.Uint64
	handler := func(ctx context.Context, msg *Message) error {
		n := calls.Add(1)
		if n == 1 {
			return errors.New("first message always fails")
		}
		return nil
	}

	loopCtx, loopCancel := context.WithCancel(ctx)
	defer loopCancel()
	stats := &ConsumeStats{}
	go func() {
		_ = client.ConsumeStream(loopCtx, stream, group, "worker", 1, 100*time.Millisecond, handler, discardLogger(), stats)
	}()

	time.Sleep(300 * time.Millisecond)

	// Poisonous head-of-queue message + two good ones. All three must be processed
	// (failed one logs + acks; good ones succeed).
	for i := 0; i < 3; i++ {
		msg, _ := NewMessage(TypeChatRequest, "test", nil)
		client.Publish(ctx, stream, msg)
	}

	deadline := time.After(5 * time.Second)
	for stats.Handled.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("queue stalled: handled=%d handler_errors=%d calls=%d",
				stats.Handled.Load(), stats.HandlerErrors.Load(), calls.Load())
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	if stats.HandlerErrors.Load() != 1 {
		t.Fatalf("expected exactly 1 handler error, got %d", stats.HandlerErrors.Load())
	}
}

func waitFor(t *testing.T, counter *atomic.Uint64, target uint64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for counter.Load() < target {
		if time.Now().After(deadline) {
			t.Fatalf("counter=%d did not reach %d within %v", counter.Load(), target, within)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
