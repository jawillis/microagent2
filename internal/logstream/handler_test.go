package logstream

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newMini(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return rdb, mr
}

func TestHandlerWritesToBothStdoutAndStream(t *testing.T) {
	rdb, mr := newMini(t)

	var stdoutBuf bytes.Buffer
	inner := slog.NewJSONHandler(&stdoutBuf, nil)
	pub := NewPublisher(rdb, Options{ServiceID: "test", QueueSize: 10})
	t.Cleanup(pub.Close)

	logger := slog.New(NewHandler(inner, pub))
	logger.Info("hello", "k", "v")

	// stdout captured immediately
	if !strings.Contains(stdoutBuf.String(), `"msg":"hello"`) {
		t.Fatalf("stdout missing entry: %q", stdoutBuf.String())
	}

	// stream write is async; wait briefly
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mr.Exists("log:test") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !mr.Exists("log:test") {
		t.Fatal("stream not created; publish did not happen")
	}
	entries, err := rdb.XRange(context.Background(), "log:test", "-", "+").Result()
	if err != nil {
		t.Fatalf("xrange: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("stream entries = %d, want 1", len(entries))
	}
	jsonBody := entries[0].Values["json"].(string)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(jsonBody), &parsed); err != nil {
		t.Fatalf("entry not JSON: %v", err)
	}
	if parsed["msg"] != "hello" || parsed["k"] != "v" {
		t.Fatalf("entry fields: %+v", parsed)
	}
}

func TestDisabledPublisherPassthroughOnly(t *testing.T) {
	rdb, mr := newMini(t)
	disabled := false
	pub := NewPublisher(rdb, Options{ServiceID: "test", Enabled: &disabled})
	t.Cleanup(pub.Close)

	var stdoutBuf bytes.Buffer
	inner := slog.NewJSONHandler(&stdoutBuf, nil)
	logger := slog.New(NewHandler(inner, pub))
	logger.Info("hi")

	// give time for any async work to settle
	time.Sleep(50 * time.Millisecond)
	if mr.Exists("log:test") {
		t.Fatal("disabled publisher should not create stream")
	}
	if !strings.Contains(stdoutBuf.String(), `"msg":"hi"`) {
		t.Fatalf("stdout should still receive log: %q", stdoutBuf.String())
	}
}

func TestQueueOverflowIncrementsDropCounter(t *testing.T) {
	rdb, _ := newMini(t)
	// Use a tiny queue and a Valkey that we'll block by closing the connection.
	pub := NewPublisher(rdb, Options{ServiceID: "test", QueueSize: 1})
	t.Cleanup(pub.Close)

	// Pre-fill the queue so subsequent writes must drop.
	// Send 10 log lines synchronously; by the time the drain goroutine
	// picks them up (they hit the queue one at a time), a couple should
	// hit the full-queue drop path.
	for i := 0; i < 100; i++ {
		pub.write([]byte(`{"msg":"filler"}`))
	}
	if pub.Drops() == 0 {
		t.Fatal("expected at least one drop from an overflowed queue")
	}
}

func TestOversizeEntryTruncated(t *testing.T) {
	rdb, mr := newMini(t)
	pub := NewPublisher(rdb, Options{ServiceID: "test"})
	t.Cleanup(pub.Close)

	big := strings.Repeat("x", 20*1024)
	body, _ := json.Marshal(map[string]any{"msg": "big", "payload": big})
	pub.write(body)

	// wait for publish
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mr.Exists("log:test") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, _ := rdb.XRange(context.Background(), "log:test", "-", "+").Result()
	if len(entries) == 0 {
		t.Fatal("truncated entry did not publish")
	}
	parsed := map[string]any{}
	if err := json.Unmarshal([]byte(entries[0].Values["json"].(string)), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["truncated_bytes"]; !ok {
		t.Fatalf("entry missing truncated_bytes marker: %+v", parsed)
	}
}

func TestNewLoggerDisabled(t *testing.T) {
	// rdb nil path returns a plain logger.
	l := NewLogger("x", nil, Options{})
	l.Info("hello") // should not panic; no stream side-effects
}
