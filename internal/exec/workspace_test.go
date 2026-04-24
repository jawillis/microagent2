package exec

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestAllocate_CreatesDir(t *testing.T) {
	root := t.TempDir()
	ws, err := Allocate(root, "sess-abc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Dir); err != nil {
		t.Fatalf("dir missing: %v", err)
	}
	if !strings.HasPrefix(ws.Dir, filepath.Join(root, "sess-abc")) {
		t.Fatalf("dir = %q", ws.Dir)
	}
	if len(ws.InvocationID) != 32 {
		t.Fatalf("invocation id = %q (len %d)", ws.InvocationID, len(ws.InvocationID))
	}
}

func TestAllocate_AnonymousSession(t *testing.T) {
	root := t.TempDir()
	ws, err := Allocate(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ws.Dir, filepath.Join(root, "anon")) {
		t.Fatalf("unexpected dir %q", ws.Dir)
	}
}

func TestAllocate_SanitizesSessionID(t *testing.T) {
	root := t.TempDir()
	ws, err := Allocate(root, "../../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ws.Dir, "..") {
		t.Fatalf("traversal not sanitized: %q", ws.Dir)
	}
	if !strings.HasPrefix(ws.Dir, root+string(os.PathSeparator)) {
		t.Fatalf("escaped root: %q not under %q", ws.Dir, root)
	}
}

func TestAllocate_ConcurrentDoesNotCollide(t *testing.T) {
	root := t.TempDir()
	seen := map[string]struct{}{}
	for i := 0; i < 32; i++ {
		ws, err := Allocate(root, "sess")
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[ws.InvocationID]; dup {
			t.Fatalf("collision on invocation_id %q", ws.InvocationID)
		}
		seen[ws.InvocationID] = struct{}{}
	}
}

func TestFinalize_WritesMetadata(t *testing.T) {
	root := t.TempDir()
	ws, _ := Allocate(root, "sess")
	meta := WorkspaceMetadata{
		Skill:    "demo",
		Script:   "hello.py",
		ExitCode: 0,
	}
	if err := ws.Finalize(meta); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(ws.Dir, ".metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	for _, want := range []string{`"skill": "demo"`, `"script": "hello.py"`, `"invocation_id":`} {
		if !strings.Contains(content, want) {
			t.Errorf("metadata missing %q:\n%s", want, content)
		}
	}
}

func TestGC_ReclaimsExpired(t *testing.T) {
	root := t.TempDir()
	ws, _ := Allocate(root, "sess")
	meta := WorkspaceMetadata{Skill: "demo", Script: "x.py", EndedAt: time.Now().UTC().Add(-2 * time.Hour)}
	if err := ws.Finalize(meta); err != nil {
		t.Fatal(err)
	}

	gc := &GC{
		Root:      root,
		Retention: 1 * time.Hour,
		Interval:  0,
		Logger:    silentLogger(),
	}
	stats := gc.RunOnce(context.Background())
	if stats.Reclaimed != 1 {
		t.Fatalf("reclaimed = %d, want 1", stats.Reclaimed)
	}
	if _, err := os.Stat(ws.Dir); !os.IsNotExist(err) {
		t.Fatalf("dir should be removed: %v", err)
	}
}

func TestGC_KeepsFreshEntries(t *testing.T) {
	root := t.TempDir()
	ws, _ := Allocate(root, "sess")
	meta := WorkspaceMetadata{Skill: "demo", EndedAt: time.Now().UTC()}
	_ = ws.Finalize(meta)

	gc := &GC{Root: root, Retention: 1 * time.Hour, Logger: silentLogger()}
	stats := gc.RunOnce(context.Background())
	if stats.Reclaimed != 0 {
		t.Fatalf("should not reclaim fresh entry; got %d", stats.Reclaimed)
	}
	if _, err := os.Stat(ws.Dir); err != nil {
		t.Fatalf("fresh dir removed: %v", err)
	}
}

func TestGC_IgnoresInflightWithoutMetadata(t *testing.T) {
	root := t.TempDir()
	ws, _ := Allocate(root, "sess")
	// Do NOT finalize — simulates an in-flight run.

	gc := &GC{Root: root, Retention: 1 * time.Hour, Logger: silentLogger()}
	stats := gc.RunOnce(context.Background())
	if stats.Reclaimed != 0 {
		t.Fatalf("should not reclaim in-flight entry; got %d", stats.Reclaimed)
	}
	if _, err := os.Stat(ws.Dir); err != nil {
		t.Fatalf("in-flight dir removed: %v", err)
	}
}

func TestGC_CountsFreedBytes(t *testing.T) {
	root := t.TempDir()
	ws, _ := Allocate(root, "sess")
	// Write a payload.
	payload := []byte("the quick brown fox")
	_ = os.WriteFile(filepath.Join(ws.Dir, "out.txt"), payload, 0o644)
	meta := WorkspaceMetadata{Skill: "d", EndedAt: time.Now().UTC().Add(-2 * time.Hour)}
	_ = ws.Finalize(meta)

	gc := &GC{Root: root, Retention: 1 * time.Hour, Logger: silentLogger()}
	stats := gc.RunOnce(context.Background())
	if stats.FreedBytes < int64(len(payload)) {
		t.Fatalf("FreedBytes = %d, want >= %d", stats.FreedBytes, len(payload))
	}
}

func TestGC_MissingRootIsHarmless(t *testing.T) {
	gc := &GC{Root: "/does/not/exist", Retention: time.Hour, Logger: silentLogger()}
	stats := gc.RunOnce(context.Background())
	if stats.Scanned != 0 {
		t.Fatalf("Scanned on missing root: %d", stats.Scanned)
	}
}
