package sessionskill

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newClient(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mr
}

func TestKey(t *testing.T) {
	if got := Key("sess-abc"); got != "session:sess-abc:active-skill" {
		t.Fatalf("Key = %q", got)
	}
}

func TestGet_MissingReturnsEmpty(t *testing.T) {
	rdb, _ := newClient(t)
	name, err := Get(context.Background(), rdb, "sess-1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if name != "" {
		t.Fatalf("name = %q, want empty", name)
	}
}

func TestSet_Roundtrip(t *testing.T) {
	rdb, _ := newClient(t)
	ctx := context.Background()

	if err := Set(ctx, rdb, "sess-1", "code-review", time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	name, err := Get(ctx, rdb, "sess-1")
	if err != nil || name != "code-review" {
		t.Fatalf("Get after Set = (%q, %v)", name, err)
	}
}

func TestSet_EmptyClears(t *testing.T) {
	rdb, mr := newClient(t)
	ctx := context.Background()

	_ = Set(ctx, rdb, "sess-1", "code-review", time.Hour)
	if err := Set(ctx, rdb, "sess-1", "", 0); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if exists := mr.Exists(Key("sess-1")); exists {
		t.Fatal("key should not exist after clear")
	}
	name, err := Get(ctx, rdb, "sess-1")
	if err != nil || name != "" {
		t.Fatalf("Get after clear = (%q, %v)", name, err)
	}
}

func TestSet_TTLHonored(t *testing.T) {
	rdb, mr := newClient(t)
	ctx := context.Background()

	if err := Set(ctx, rdb, "sess-1", "foo", 2*time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ttl := mr.TTL(Key("sess-1"))
	// miniredis reports the remaining TTL; allow a small slack.
	if ttl < 2*time.Hour-time.Second || ttl > 2*time.Hour+time.Second {
		t.Fatalf("ttl = %v, want ~2h", ttl)
	}
}

func TestSet_NonPositiveTTLFallsBackToDefault(t *testing.T) {
	rdb, mr := newClient(t)
	ctx := context.Background()

	if err := Set(ctx, rdb, "sess-1", "foo", -1*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ttl := mr.TTL(Key("sess-1"))
	// Default is 24h.
	if ttl < 23*time.Hour || ttl > 25*time.Hour {
		t.Fatalf("ttl = %v, want ~24h", ttl)
	}
}

func TestSet_OverwriteRefreshesTTL(t *testing.T) {
	rdb, mr := newClient(t)
	ctx := context.Background()

	_ = Set(ctx, rdb, "sess-1", "foo", 5*time.Minute)
	mr.FastForward(4 * time.Minute)
	_ = Set(ctx, rdb, "sess-1", "bar", 2*time.Hour)

	ttl := mr.TTL(Key("sess-1"))
	if ttl < time.Hour+50*time.Minute {
		t.Fatalf("ttl not refreshed: %v", ttl)
	}
	name, _ := Get(ctx, rdb, "sess-1")
	if name != "bar" {
		t.Fatalf("name = %q", name)
	}
}

func TestGet_EmptySessionIDRejected(t *testing.T) {
	rdb, _ := newClient(t)
	if _, err := Get(context.Background(), rdb, ""); err == nil {
		t.Fatal("expected error for empty session id")
	}
}

func TestSet_EmptySessionIDRejected(t *testing.T) {
	rdb, _ := newClient(t)
	if err := Set(context.Background(), rdb, "", "foo", time.Hour); err == nil {
		t.Fatal("expected error for empty session id")
	}
}

func TestGet_ValkeyFailurePropagates(t *testing.T) {
	rdb, mr := newClient(t)
	mr.Close() // force connection failure
	_, err := Get(context.Background(), rdb, "sess-1")
	if err == nil {
		t.Fatal("expected error when Valkey is down")
	}
}
