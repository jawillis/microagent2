package exec

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// GC sweeps expired workspace directories.
type GC struct {
	Root      string
	Retention time.Duration
	Interval  time.Duration
	Logger    *slog.Logger

	mu sync.Mutex
}

// Run ticks until ctx is cancelled. Intended to run in its own goroutine.
func (g *GC) Run(ctx context.Context) {
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

// RunOnce performs a single sweep. Safe to invoke synchronously on tmpfs
// exhaustion to reclaim retention-expired space.
func (g *GC) RunOnce(ctx context.Context) GCStats {
	g.mu.Lock()
	defer g.mu.Unlock()

	stats := GCStats{}
	now := time.Now().UTC()

	sessions, err := os.ReadDir(g.Root)
	if err != nil {
		if g.Logger != nil && !os.IsNotExist(err) {
			g.Logger.Warn("exec_workspace_gc_read_root", "root", g.Root, "error", err.Error())
		}
		return stats
	}

	for _, sess := range sessions {
		if ctx.Err() != nil {
			break
		}
		if !sess.IsDir() {
			continue
		}
		sessDir := filepath.Join(g.Root, sess.Name())
		invs, err := os.ReadDir(sessDir)
		if err != nil {
			continue
		}
		for _, inv := range invs {
			if ctx.Err() != nil {
				break
			}
			if !inv.IsDir() {
				continue
			}
			invDir := filepath.Join(sessDir, inv.Name())
			stats.Scanned++

			metaBytes, err := os.ReadFile(filepath.Join(invDir, ".metadata.json"))
			if err != nil {
				// In-flight invocation without metadata; skip.
				continue
			}
			var meta WorkspaceMetadata
			if err := json.Unmarshal(metaBytes, &meta); err != nil {
				continue
			}
			if meta.EndedAt.IsZero() {
				continue
			}
			if now.Sub(meta.EndedAt) < g.Retention {
				continue
			}
			size := dirSize(invDir)
			if err := os.RemoveAll(invDir); err != nil {
				if g.Logger != nil {
					g.Logger.Warn("exec_workspace_gc_remove", "dir", invDir, "error", err.Error())
				}
				continue
			}
			stats.Reclaimed++
			stats.FreedBytes += size
		}
		// Try to remove the empty session directory. Ignore failures; it
		// may have a concurrent in-flight invocation inside.
		_ = os.Remove(sessDir)
	}

	if g.Logger != nil {
		g.Logger.Info("exec_workspace_gc",
			"scanned", stats.Scanned,
			"reclaimed", stats.Reclaimed,
			"freed_bytes", stats.FreedBytes,
		)
	}
	return stats
}

// GCStats describes the outcome of a single sweep.
type GCStats struct {
	Scanned    int
	Reclaimed  int
	FreedBytes int64
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}
