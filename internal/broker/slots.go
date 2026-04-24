package broker

import (
	"sync"
	"time"
)

type SlotState int

const (
	SlotUnassigned SlotState = iota
	SlotProvisional
	SlotAssigned
)

func (s SlotState) String() string {
	switch s {
	case SlotUnassigned:
		return "unassigned"
	case SlotProvisional:
		return "provisional"
	case SlotAssigned:
		return "assigned"
	default:
		return "unknown"
	}
}

type SlotEntry struct {
	SlotID        int
	State         SlotState
	AgentID       string
	Priority      int
	AssignedAt    time.Time
	CorrelationID string
}

type SlotSnapshotEntry struct {
	SlotID   int       `json:"slot"`
	State    string    `json:"state"`
	AgentID  string    `json:"agent,omitempty"`
	Priority int       `json:"priority"`
	AgeS     float64   `json:"age_s"`
}

type SlotTable struct {
	mu    sync.RWMutex
	slots []SlotEntry
}

func NewSlotTable(count int) *SlotTable {
	slots := make([]SlotEntry, count)
	for i := range slots {
		slots[i] = SlotEntry{SlotID: i, State: SlotUnassigned}
	}
	return &SlotTable{slots: slots}
}

func (st *SlotTable) FindUnassigned() (int, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, s := range st.slots {
		if s.State == SlotUnassigned {
			return s.SlotID, true
		}
	}
	return -1, false
}

func (st *SlotTable) AssignProvisional(slotID int, agentID string, priority int, correlationID string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if slotID < 0 || slotID >= len(st.slots) {
		return false
	}
	if st.slots[slotID].State != SlotUnassigned {
		return false
	}
	st.slots[slotID] = SlotEntry{
		SlotID:        slotID,
		State:         SlotProvisional,
		AgentID:       agentID,
		Priority:      priority,
		AssignedAt:    time.Now(),
		CorrelationID: correlationID,
	}
	return true
}

func (st *SlotTable) CommitAssignment(correlationID string) (int, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i, s := range st.slots {
		if s.State == SlotProvisional && s.CorrelationID == correlationID {
			st.slots[i].State = SlotAssigned
			st.slots[i].CorrelationID = ""
			return s.SlotID, true
		}
	}
	return -1, false
}

func (st *SlotTable) RevertProvisional(correlationID string) (int, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i, s := range st.slots {
		if s.State == SlotProvisional && s.CorrelationID == correlationID {
			st.slots[i] = SlotEntry{SlotID: s.SlotID, State: SlotUnassigned}
			return s.SlotID, true
		}
	}
	return -1, false
}

func (st *SlotTable) Assign(slotID int, agentID string, priority int) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if slotID >= len(st.slots) {
		return false
	}
	if st.slots[slotID].State != SlotUnassigned {
		return false
	}
	st.slots[slotID] = SlotEntry{
		SlotID:     slotID,
		State:      SlotAssigned,
		AgentID:    agentID,
		Priority:   priority,
		AssignedAt: time.Now(),
	}
	return true
}

func (st *SlotTable) ForceAssign(slotID int, agentID string, priority int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if slotID >= len(st.slots) {
		return
	}
	st.slots[slotID] = SlotEntry{
		SlotID:     slotID,
		State:      SlotAssigned,
		AgentID:    agentID,
		Priority:   priority,
		AssignedAt: time.Now(),
	}
}

func (st *SlotTable) Release(slotID int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if slotID < len(st.slots) {
		st.slots[slotID] = SlotEntry{SlotID: slotID, State: SlotUnassigned}
	}
}

func (st *SlotTable) ReleaseByAgent(agentID string) []int {
	st.mu.Lock()
	defer st.mu.Unlock()
	var released []int
	for i, s := range st.slots {
		if s.AgentID == agentID {
			st.slots[i] = SlotEntry{SlotID: i, State: SlotUnassigned}
			released = append(released, i)
		}
	}
	return released
}

func (st *SlotTable) FindLowestPriorityPreemptible(registry interface{ IsPreemptible(string) bool }) (int, string, int, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	bestSlot := -1
	bestAgent := ""
	bestPriority := -1
	for _, s := range st.slots {
		if s.State == SlotAssigned && registry.IsPreemptible(s.AgentID) {
			if bestSlot == -1 || s.Priority > bestPriority {
				bestSlot = s.SlotID
				bestAgent = s.AgentID
				bestPriority = s.Priority
			}
		}
	}
	if bestSlot == -1 {
		return -1, "", -1, false
	}
	return bestSlot, bestAgent, bestPriority, true
}

func (st *SlotTable) GetByAgent(agentID string) (int, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, s := range st.slots {
		if s.AgentID == agentID && s.State == SlotAssigned {
			return s.SlotID, true
		}
	}
	return -1, false
}

func (st *SlotTable) IsOwnedBy(slotID int, agentID string) bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if slotID < 0 || slotID >= len(st.slots) {
		return false
	}
	s := st.slots[slotID]
	// Accept both Provisional and Assigned: the agent demonstrably received
	// the slot-assigned reply (otherwise it could not be sending this request).
	// The reclaim timer is the sole authority for reverting unacked provisional
	// assignments; once the agent acts on the slot, it owns it.
	return (s.State == SlotAssigned || s.State == SlotProvisional) && s.AgentID == agentID
}

func (st *SlotTable) Snapshot() []SlotSnapshotEntry {
	st.mu.RLock()
	defer st.mu.RUnlock()
	now := time.Now()
	out := make([]SlotSnapshotEntry, len(st.slots))
	for i, s := range st.slots {
		entry := SlotSnapshotEntry{
			SlotID:   s.SlotID,
			State:    s.State.String(),
			Priority: s.Priority,
		}
		if s.State != SlotUnassigned {
			entry.AgentID = s.AgentID
			entry.AgeS = now.Sub(s.AssignedAt).Seconds()
		}
		out[i] = entry
	}
	return out
}
