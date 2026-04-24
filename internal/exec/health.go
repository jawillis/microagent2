package exec

import (
	"sort"
	"sync"
	"time"
)

// Health tracks the service's liveness + install state. Thread-safe.
type Health struct {
	mu         sync.Mutex
	ready      bool
	prewarmed  map[string]struct{}
	failed     map[string]failedEntry
}

type failedEntry struct {
	error string
	at    time.Time
}

// NewHealth returns a Health initialized to "starting" state.
func NewHealth() *Health {
	return &Health{
		prewarmed: map[string]struct{}{},
		failed:    map[string]failedEntry{},
	}
}

// Ready flips status to "ok". Idempotent.
func (h *Health) Ready() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ready = true
}

// IsReady reports whether the service has finished its initial prewarm.
func (h *Health) IsReady() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ready
}

// Installed marks a skill as successfully installed and clears any prior
// failure for that skill.
func (h *Health) Installed(skill string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prewarmed[skill] = struct{}{}
	delete(h.failed, skill)
}

// InstallFailed records a failed install. Calling Installed later clears it.
func (h *Health) InstallFailed(skill string, err string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failed[skill] = failedEntry{error: err, at: time.Now().UTC()}
	delete(h.prewarmed, skill)
}

// Snapshot returns a point-in-time copy of health state safe to serialize.
func (h *Health) Snapshot() HealthResponse {
	h.mu.Lock()
	defer h.mu.Unlock()

	resp := HealthResponse{
		Ready:           h.ready,
		PrewarmedSkills: make([]string, 0, len(h.prewarmed)),
		FailedInstalls:  make([]FailedInstall, 0, len(h.failed)),
	}
	if h.ready {
		resp.Status = "ok"
	} else {
		resp.Status = "starting"
	}
	for skill := range h.prewarmed {
		resp.PrewarmedSkills = append(resp.PrewarmedSkills, skill)
	}
	sort.Strings(resp.PrewarmedSkills)
	for skill, entry := range h.failed {
		resp.FailedInstalls = append(resp.FailedInstalls, FailedInstall{
			Skill: skill,
			Error: entry.error,
			At:    entry.at.Format(time.RFC3339),
		})
	}
	sort.Slice(resp.FailedInstalls, func(i, j int) bool {
		return resp.FailedInstalls[i].Skill < resp.FailedInstalls[j].Skill
	})
	return resp
}
