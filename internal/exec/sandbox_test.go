package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSandboxFor_CreatesDirAndMarker(t *testing.T) {
	root := t.TempDir()
	dir, err := SandboxFor(root, "sess-abc")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "sess-abc") {
		t.Fatalf("dir = %q", dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, lastAccessMarker)); err != nil {
		t.Fatalf(".last_access missing: %v", err)
	}
}

func TestSandboxFor_ReusesSameDir(t *testing.T) {
	root := t.TempDir()
	dir1, _ := SandboxFor(root, "sess-1")
	// Write a file to prove persistence.
	if err := os.WriteFile(filepath.Join(dir1, "persist.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir2, _ := SandboxFor(root, "sess-1")
	if dir1 != dir2 {
		t.Fatalf("paths differ: %q vs %q", dir1, dir2)
	}
	if _, err := os.Stat(filepath.Join(dir2, "persist.txt")); err != nil {
		t.Fatalf("file not preserved: %v", err)
	}
}

func TestSandboxFor_DifferentSessionsIsolated(t *testing.T) {
	root := t.TempDir()
	a, _ := SandboxFor(root, "sess-a")
	b, _ := SandboxFor(root, "sess-b")
	if a == b {
		t.Fatal("sessions share a dir")
	}
	_ = os.WriteFile(filepath.Join(a, "in-a.txt"), []byte("x"), 0o644)
	if _, err := os.Stat(filepath.Join(b, "in-a.txt")); err == nil {
		t.Fatal("b should not see a's file")
	}
}

func TestSandboxFor_TraversalSanitized(t *testing.T) {
	root := t.TempDir()
	dir, err := SandboxFor(root, "../../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		t.Fatalf("traversal not sanitized: %q not under %q", dir, root)
	}
	if strings.Contains(dir, "..") {
		t.Fatalf("traversal survived: %q", dir)
	}
}

func TestSandboxFor_EmptySessionBecomesAnon(t *testing.T) {
	root := t.TempDir()
	dir, err := SandboxFor(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "anon") {
		t.Fatalf("dir = %q, want anon", dir)
	}
}

func TestSandboxFor_TouchesLastAccess(t *testing.T) {
	root := t.TempDir()
	dir, _ := SandboxFor(root, "sess-touch")
	info1, _ := os.Stat(filepath.Join(dir, lastAccessMarker))

	// Ensure a measurable mtime delta.
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, lastAccessMarker), past, past); err != nil {
		t.Fatal(err)
	}

	_, _ = SandboxFor(root, "sess-touch")
	info2, _ := os.Stat(filepath.Join(dir, lastAccessMarker))
	if !info2.ModTime().After(info1.ModTime().Add(-time.Minute)) {
		t.Fatalf("mtime not bumped; info2 = %v", info2.ModTime())
	}
}

func TestSandboxGC_ReclaimsInactive(t *testing.T) {
	root := t.TempDir()
	dir, _ := SandboxFor(root, "old")
	// Backdate the marker so GC views the session as stale.
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, lastAccessMarker), old, old)

	gc := &SandboxGC{Root: root, Retention: 1 * time.Hour, Logger: silentLogger()}
	stats := gc.RunOnce(context.Background())
	if stats.Reclaimed != 1 {
		t.Fatalf("reclaimed = %d", stats.Reclaimed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir should be gone: %v", err)
	}
}

func TestSandboxGC_KeepsActive(t *testing.T) {
	root := t.TempDir()
	dir, _ := SandboxFor(root, "fresh")
	gc := &SandboxGC{Root: root, Retention: 1 * time.Hour, Logger: silentLogger()}
	stats := gc.RunOnce(context.Background())
	if stats.Reclaimed != 0 {
		t.Fatalf("should keep fresh session")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir vanished: %v", err)
	}
}

func TestSandboxGC_MissingMarkerFallsBackToDirMtime(t *testing.T) {
	root := t.TempDir()
	dir, _ := SandboxFor(root, "no-marker")
	// Delete .last_access and backdate the directory itself.
	_ = os.Remove(filepath.Join(dir, lastAccessMarker))
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(dir, old, old)

	gc := &SandboxGC{Root: root, Retention: 1 * time.Hour, Logger: silentLogger()}
	stats := gc.RunOnce(context.Background())
	if stats.Reclaimed != 1 {
		t.Fatalf("should reclaim via directory mtime; got %d", stats.Reclaimed)
	}
}

func TestSandboxGC_FreedBytesAccounted(t *testing.T) {
	root := t.TempDir()
	dir, _ := SandboxFor(root, "big")
	payload := []byte("hello worlds")
	_ = os.WriteFile(filepath.Join(dir, "artifact.txt"), payload, 0o644)
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, lastAccessMarker), old, old)

	gc := &SandboxGC{Root: root, Retention: 1 * time.Hour, Logger: silentLogger()}
	stats := gc.RunOnce(context.Background())
	if stats.FreedBytes < int64(len(payload)) {
		t.Fatalf("FreedBytes = %d, want >= %d", stats.FreedBytes, len(payload))
	}
}

func TestSandboxGC_MissingRootIsHarmless(t *testing.T) {
	gc := &SandboxGC{Root: "/does/not/exist", Retention: time.Hour, Logger: silentLogger()}
	if stats := gc.RunOnce(context.Background()); stats.Scanned != 0 {
		t.Fatalf("Scanned on missing root: %d", stats.Scanned)
	}
}
