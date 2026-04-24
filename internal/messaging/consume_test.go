package messaging

import (
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want errClass
	}{
		{"nil", nil, errClassNil},
		{"redis.Nil", redis.Nil, errClassNil},
		{"wrapped redis.Nil", errors.New("wrapped: " + redis.Nil.Error()), errClassUnknown},
		{"nogroup", errors.New("NOGROUP No such consumer group"), errClassNoGroup},
		{"no such key", errors.New("ERR no such key"), errClassNoGroup},
		{"unknown", errors.New("something else"), errClassUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyError(tt.err); got != tt.want {
				t.Fatalf("classifyError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}

	// Verify errors.Is on direct redis.Nil
	if classifyError(redis.Nil) != errClassNil {
		t.Fatal("redis.Nil should classify as nil")
	}
}

func TestNextBackoff(t *testing.T) {
	b := consumeBackoffStart
	for i := 0; i < 20; i++ {
		b = nextBackoff(b)
		if b > consumeBackoffCap {
			t.Fatalf("backoff exceeded cap: %v > %v", b, consumeBackoffCap)
		}
	}
	if b != consumeBackoffCap {
		t.Fatalf("backoff should reach cap, got %v", b)
	}
}

func TestConsumeStatsZeroValue(t *testing.T) {
	var s ConsumeStats
	if s.Handled.Load() != 0 || s.HandlerErrors.Load() != 0 || s.Recoveries.Load() != 0 || s.PhantomLogs.Load() != 0 {
		t.Fatal("zero-value ConsumeStats should have all counters at 0")
	}
	s.Handled.Add(1)
	s.HandlerErrors.Add(2)
	s.Recoveries.Add(3)
	s.PhantomLogs.Add(4)
	if s.Handled.Load() != 1 || s.HandlerErrors.Load() != 2 || s.Recoveries.Load() != 3 || s.PhantomLogs.Load() != 4 {
		t.Fatal("ConsumeStats counters did not update correctly")
	}
}
