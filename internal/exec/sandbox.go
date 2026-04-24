package exec

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// lastAccessMarker is the hidden file whose mtime drives touch-based
// retention for the agent's session-persistent sandbox.
const lastAccessMarker = ".last_access"

// SandboxFor ensures a session-persistent sandbox directory exists under
// root and bumps its last-access marker. Returns the absolute path to the
// directory. Safe to call concurrently for the same session (os.MkdirAll is
// idempotent), but callers should hold a per-session mutex around any
// filesystem mutation that follows.
func SandboxFor(root, sessionID string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("sandbox root is empty")
	}
	safe := sanitizeSessionID(sessionID)
	dir := filepath.Join(root, safe)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if isNoSpace(err) {
			return "", ErrWorkspaceFull
		}
		return "", fmt.Errorf("sandbox allocate: %w", err)
	}
	if err := touchLastAccess(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// touchLastAccess creates or updates .last_access in dir so the GC loop
// treats the session as freshly used.
func touchLastAccess(dir string) error {
	path := filepath.Join(dir, lastAccessMarker)
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return nil
	}
	// File doesn't exist yet — create it.
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("touch last_access: %w", err)
	}
	return f.Close()
}

// SandboxGC sweeps sandbox directories whose last-access age exceeds the
// configured retention. Mirrors workspace GC but with a different retention
// driver (touch-based rather than completion-based).
type SandboxGC struct {
	Root      string
	Retention time.Duration
	Interval  time.Duration
	Logger    *slog.Logger

	mu sync.Mutex
}

// SandboxGCStats describes the outcome of a single sweep.
type SandboxGCStats struct {
	Scanned    int
	Reclaimed  int
	FreedBytes int64
}

// Run ticks until ctx is cancelled.
func (g *SandboxGC) Run(ctx context.Context) {
	if g.Interval <= 0 {
		return
	}
	t := time.NewTicker(g.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.RunOnce(ctx)
		}
	}
}

// RunOnce walks the sandbox root once and removes expired session dirs.
// Safe to call synchronously when the tmpfs is full.
func (g *SandboxGC) RunOnce(ctx context.Context) SandboxGCStats {
	g.mu.Lock()
	defer g.mu.Unlock()

	stats := SandboxGCStats{}
	now := time.Now()

	entries, err := os.ReadDir(g.Root)
	if err != nil {
		if g.Logger != nil && !os.IsNotExist(err) {
			g.Logger.Warn("exec_sandbox_gc_read_root", "root", g.Root, "error", err.Error())
		}
		return stats
	}

	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(g.Root, e.Name())
		stats.Scanned++

		age := g.sessionAge(dir, now)
		if age < g.Retention {
			continue
		}
		size := dirSize(dir)
		if err := os.RemoveAll(dir); err != nil {
			if g.Logger != nil {
				g.Logger.Warn("exec_sandbox_gc_remove", "dir", dir, "error", err.Error())
			}
			continue
		}
		stats.Reclaimed++
		stats.FreedBytes += size
	}

	if g.Logger != nil {
		g.Logger.Info("exec_sandbox_gc",
			"scanned", stats.Scanned,
			"reclaimed", stats.Reclaimed,
			"freed_bytes", stats.FreedBytes,
		)
	}
	return stats
}

// sessionAge returns the elapsed time since the session's last activity.
// Uses `.last_access` mtime when present, otherwise falls back to the
// directory's own mtime so an agent that deletes the marker still ages out.
func (g *SandboxGC) sessionAge(dir string, now time.Time) time.Duration {
	if info, err := os.Stat(filepath.Join(dir, lastAccessMarker)); err == nil {
		return now.Sub(info.ModTime())
	}
	if info, err := os.Stat(dir); err == nil {
		return now.Sub(info.ModTime())
	}
	return 0
}
