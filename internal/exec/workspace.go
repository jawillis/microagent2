package exec

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Workspace represents one invocation's scratch directory. Created before
// subprocess spawn, finalized with metadata when the subprocess returns,
// later reclaimed by GC.
type Workspace struct {
	Dir          string
	SessionID    string
	InvocationID string
	StartedAt    time.Time
}

// WorkspaceMetadata is persisted as .metadata.json for GC to consult.
type WorkspaceMetadata struct {
	Skill        string    `json:"skill"`
	Script       string    `json:"script"`
	Args         []string  `json:"args,omitempty"`
	InvocationID string    `json:"invocation_id"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at"`
	ExitCode     int       `json:"exit_code"`
	TimedOut     bool      `json:"timed_out"`
}

// newInvocationID returns 16 bytes of hex randomness. 128-bit ids are
// effectively collision-free for workspace scoping.
func newInvocationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// sanitizeSessionID replaces path separators to prevent traversal via
// session-id injection. Empty → "anon".
func sanitizeSessionID(s string) string {
	if s == "" {
		return "anon"
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/' || c == '\\' || c == '.':
			out = append(out, '_')
		case c < 0x20 || c >= 0x7f:
			out = append(out, '_')
		default:
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "anon"
	}
	return string(out)
}

// Allocate creates a fresh per-invocation workspace under root.
func Allocate(root, sessionID string) (*Workspace, error) {
	invID, err := newInvocationID()
	if err != nil {
		return nil, err
	}
	safe := sanitizeSessionID(sessionID)
	dir := filepath.Join(root, safe, invID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// tmpfs exhaustion surfaces as ENOSPC from MkdirAll.
		if isNoSpace(err) {
			return nil, ErrWorkspaceFull
		}
		return nil, fmt.Errorf("workspace allocate: %w", err)
	}
	return &Workspace{
		Dir:          dir,
		SessionID:    safe,
		InvocationID: invID,
		StartedAt:    time.Now().UTC(),
	}, nil
}

// Finalize writes .metadata.json so GC can later decide whether to reclaim.
func (w *Workspace) Finalize(meta WorkspaceMetadata) error {
	meta.InvocationID = w.InvocationID
	meta.StartedAt = w.StartedAt
	if meta.EndedAt.IsZero() {
		meta.EndedAt = time.Now().UTC()
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.Dir, ".metadata.json"), b, 0o644)
}

func isNoSpace(err error) bool {
	// os errors wrap the underlying syscall error; check the string rather
	// than importing syscall directly (cross-platform).
	return err != nil && (errorContains(err, "no space left on device") ||
		errorContains(err, "disk quota exceeded"))
}

func errorContains(err error, substr string) bool {
	if err == nil {
		return false
	}
	return containsFold(err.Error(), substr)
}

// containsFold is a light case-insensitive substring check without importing strings.
func containsFold(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a, b := s[i+j], substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
