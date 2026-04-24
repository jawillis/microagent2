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

type SlotClass string

const (
	SlotClassAgent     SlotClass = "agent"
	SlotClassHindsight SlotClass = "hindsight"
)

// NormalizeClass returns the canonical class value, defaulting empty to agent.
// Returns false if the input is a non-empty unrecognized value.
func NormalizeClass(s string) (SlotClass, bool) {
	switch s {
	case "", string(SlotClassAgent):
		return SlotClassAgent, true
	case string(SlotClassHindsight):
		return SlotClassHindsight, true
	default:
		return "", false
	}
}

type SlotEntry struct {
	SlotID        int
	Class         SlotClass
	State         SlotState
	AgentID       string
	Priority      int
	AssignedAt    time.Time
	CorrelationID string
}

type SlotSnapshotEntry struct {
	SlotID   int     `json:"slot"`
	Class    string  `json:"class"`
	State    string  `json:"state"`
	AgentID  string  `json:"agent,omitempty"`
	Priority int     `json:"priority"`
	AgeS     float64 `json:"age_s"`
}

type SlotTable struct {
	mu    sync.RWMutex
	slots []SlotEntry
}

// NewSlotTable creates a table with `count` agent-class slots and zero
// hindsight-class slots. Retained for backward compatibility with callers
// that predate slot classes.
func NewSlotTable(count int) *SlotTable {
	return NewSlotTableWithClasses(count, 0)
}

// NewSlotTableWithClasses creates a table with agentCount agent-class slots
// at indices [0, agentCount) followed by hindsightCount hindsight-class slots
// at [agentCount, agentCount+hindsightCount). A slot's class never changes.
func NewSlotTableWithClasses(agentCount, hindsightCount int) *SlotTable {
	if agentCount < 0 {
		agentCount = 0
	}
	if hindsightCount < 0 {
		hindsightCount = 0
	}
	total := agentCount + hindsightCount
	slots := make([]SlotEntry, total)
	for i := 0; i < agentCount; i++ {
		slots[i] = SlotEntry{SlotID: i, Class: SlotClassAgent, State: SlotUnassigned}
	}
	for i := agentCount; i < total; i++ {
		slots[i] = SlotEntry{SlotID: i, Class: SlotClassHindsight, State: SlotUnassigned}
	}
	return &SlotTable{slots: slots}
}

func (st *SlotTable) FindUnassigned(class SlotClass) (int, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, s := range st.slots {
		if s.Class == class && s.State == SlotUnassigned {
			return s.SlotID, true
		}
	}
	return -1, false
}

// ClassOf returns the class of the slot at the given index, or empty if
// the index is out of range.
func (st *SlotTable) ClassOf(slotID int) SlotClass {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if slotID < 0 || slotID >= len(st.slots) {
		return ""
	}
	return st.slots[slotID].Class
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
	class := st.slots[slotID].Class
	st.slots[slotID] = SlotEntry{
		SlotID:        slotID,
		Class:         class,
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
			st.slots[i] = SlotEntry{SlotID: s.SlotID, Class: s.Class, State: SlotUnassigned}
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
	class := st.slots[slotID].Class
	st.slots[slotID] = SlotEntry{
		SlotID:     slotID,
		Class:      class,
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
	class := st.slots[slotID].Class
	st.slots[slotID] = SlotEntry{
		SlotID:     slotID,
		Class:      class,
		State:      SlotAssigned,
		AgentID:    agentID,
		Priority:   priority,
		AssignedAt: time.Now(),
	}
}

func (st *SlotTable) Release(slotID int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if slotID >= 0 && slotID < len(st.slots) {
		class := st.slots[slotID].Class
		st.slots[slotID] = SlotEntry{SlotID: slotID, Class: class, State: SlotUnassigned}
	}
}

func (st *SlotTable) ReleaseByAgent(agentID string) []int {
	st.mu.Lock()
	defer st.mu.Unlock()
	var released []int
	for i, s := range st.slots {
		if s.AgentID == agentID {
			st.slots[i] = SlotEntry{SlotID: i, Class: s.Class, State: SlotUnassigned}
			released = append(released, i)
		}
	}
	return released
}

// FindLowestPriorityPreemptible finds a preemptible slot within the given class.
// Only slots of the matching class are considered — no cross-class preemption.
func (st *SlotTable) FindLowestPriorityPreemptible(class SlotClass, registry interface{ IsPreemptible(string) bool }) (int, string, int, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	bestSlot := -1
	bestAgent := ""
	bestPriority := -1
	for _, s := range st.slots {
		if s.Class != class {
			continue
		}
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
			Class:    string(s.Class),
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
