package broker

import (
	"sync"
	"time"
)

type SlotState int

const (
	SlotUnassigned SlotState = iota
	SlotAssigned
)

type SlotEntry struct {
	SlotID    int
	State     SlotState
	AgentID   string
	Priority  int
	AssignedAt time.Time
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

func (st *SlotTable) PinSlot(slotID int, agentID string, priority int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if slotID < len(st.slots) {
		st.slots[slotID] = SlotEntry{
			SlotID:    slotID,
			State:     SlotAssigned,
			AgentID:   agentID,
			Priority:  priority,
			AssignedAt: time.Now(),
		}
	}
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
		SlotID:    slotID,
		State:     SlotAssigned,
		AgentID:   agentID,
		Priority:  priority,
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
		if s.AgentID == agentID {
			return s.SlotID, true
		}
	}
	return -1, false
}
