package broker

import (
	"testing"
)

func TestSlotTableProvisionalLifecycle(t *testing.T) {
	st := NewSlotTable(2)

	if !st.AssignProvisional(0, "a", 0, "corr-1") {
		t.Fatal("provisional assign should succeed on empty slot")
	}
	if _, ok := st.FindUnassigned(SlotClassAgent); ok && st.slots[0].State == SlotUnassigned {
		t.Fatal("provisional slot should count as occupied")
	}
	slotID, ok := st.CommitAssignment("corr-1")
	if !ok || slotID != 0 {
		t.Fatalf("commit: ok=%v slot=%d", ok, slotID)
	}
	if st.slots[0].State != SlotAssigned {
		t.Fatalf("state after commit: %v", st.slots[0].State)
	}
	if st.slots[0].CorrelationID != "" {
		t.Fatalf("correlation id should be cleared after commit: %q", st.slots[0].CorrelationID)
	}
}

func TestSlotTableRevertProvisional(t *testing.T) {
	st := NewSlotTable(1)
	st.AssignProvisional(0, "a", 0, "corr-1")
	slot, ok := st.RevertProvisional("corr-1")
	if !ok || slot != 0 {
		t.Fatalf("revert: ok=%v slot=%d", ok, slot)
	}
	if st.slots[0].State != SlotUnassigned {
		t.Fatalf("state after revert: %v", st.slots[0].State)
	}
	// Second revert is a no-op
	if _, ok := st.RevertProvisional("corr-1"); ok {
		t.Fatal("second revert should be no-op")
	}
}

func TestSlotTableLateAckAfterRevert(t *testing.T) {
	st := NewSlotTable(1)
	st.AssignProvisional(0, "a", 0, "corr-late")
	st.RevertProvisional("corr-late")
	if _, ok := st.CommitAssignment("corr-late"); ok {
		t.Fatal("commit of reverted correlation id should fail")
	}
}

func TestSlotTableAssignCollision(t *testing.T) {
	st := NewSlotTable(1)
	st.AssignProvisional(0, "a", 0, "corr-1")
	if st.AssignProvisional(0, "b", 0, "corr-2") {
		t.Fatal("provisional assign should fail on occupied slot")
	}
	if st.Assign(0, "b", 0) {
		t.Fatal("assign should fail on occupied slot")
	}
}

func TestSlotTableReleaseByAgent(t *testing.T) {
	st := NewSlotTable(3)
	st.Assign(0, "a", 0)
	st.Assign(2, "a", 0)
	released := st.ReleaseByAgent("a")
	if len(released) != 2 {
		t.Fatalf("released: %v", released)
	}
	if _, ok := st.GetByAgent("a"); ok {
		t.Fatal("agent should own no slots after release")
	}
}

func TestSlotTableReleaseByAgentNoOp(t *testing.T) {
	st := NewSlotTable(2)
	released := st.ReleaseByAgent("ghost")
	if len(released) != 0 {
		t.Fatalf("expected empty release list, got %v", released)
	}
}

func TestSlotTableIsOwnedBy(t *testing.T) {
	st := NewSlotTable(2)
	st.Assign(0, "a", 0)
	st.AssignProvisional(1, "b", 0, "corr")

	if !st.IsOwnedBy(0, "a") {
		t.Fatal("committed assignment should be owned")
	}
	if st.IsOwnedBy(0, "b") {
		t.Fatal("wrong agent should not own slot")
	}
	if !st.IsOwnedBy(1, "b") {
		t.Fatal("provisional slot owned by agent should be considered owned (act-on-slot proves receipt)")
	}
	if st.IsOwnedBy(-1, "a") || st.IsOwnedBy(99, "a") {
		t.Fatal("out-of-range should not be owned")
	}
}

func TestSlotTableSnapshot(t *testing.T) {
	st := NewSlotTable(3)
	st.Assign(0, "a", 0)
	st.AssignProvisional(1, "b", 1, "corr")

	snap := st.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot length: %d", len(snap))
	}
	if snap[0].State != "assigned" || snap[0].AgentID != "a" {
		t.Fatalf("snap[0]: %+v", snap[0])
	}
	if snap[0].Class != "agent" {
		t.Fatalf("snap[0] class: %q", snap[0].Class)
	}
	if snap[1].State != "provisional" || snap[1].AgentID != "b" {
		t.Fatalf("snap[1]: %+v", snap[1])
	}
	if snap[2].State != "unassigned" || snap[2].AgentID != "" {
		t.Fatalf("snap[2]: %+v", snap[2])
	}
}

func TestSlotTableWithClassesInitialization(t *testing.T) {
	st := NewSlotTableWithClasses(2, 3)
	snap := st.Snapshot()
	if len(snap) != 5 {
		t.Fatalf("want 5 slots, got %d", len(snap))
	}
	for i := 0; i < 2; i++ {
		if snap[i].Class != "agent" {
			t.Fatalf("slot %d class = %q, want agent", i, snap[i].Class)
		}
	}
	for i := 2; i < 5; i++ {
		if snap[i].Class != "hindsight" {
			t.Fatalf("slot %d class = %q, want hindsight", i, snap[i].Class)
		}
	}
}

func TestFindUnassignedHonorsClass(t *testing.T) {
	st := NewSlotTableWithClasses(2, 2)
	// Only hindsight slots are free; agent request must not match them.
	st.Assign(0, "a1", 0)
	st.Assign(1, "a2", 0)

	_, okAgent := st.FindUnassigned(SlotClassAgent)
	if okAgent {
		t.Fatal("agent class should have no free slots")
	}
	sid, okHind := st.FindUnassigned(SlotClassHindsight)
	if !okHind || sid < 2 {
		t.Fatalf("hindsight FindUnassigned = %d, ok=%v; want >=2 and true", sid, okHind)
	}
}

type stubRegistry struct{}

func (stubRegistry) IsPreemptible(string) bool { return true }

func TestFindLowestPriorityPreemptibleHonorsClass(t *testing.T) {
	st := NewSlotTableWithClasses(2, 2)
	st.Assign(0, "agent-0", 5)
	st.Assign(1, "agent-1", 3)
	st.Assign(2, "hind-0", 9) // much lower-priority but in hindsight class
	st.Assign(3, "hind-1", 7)

	slot, agent, _, ok := st.FindLowestPriorityPreemptible(SlotClassAgent, stubRegistry{})
	if !ok {
		t.Fatal("expected an agent-class victim")
	}
	if agent != "agent-0" || slot != 0 {
		t.Fatalf("agent victim = (%s,%d); want (agent-0,0)", agent, slot)
	}
	slot, agent, _, ok = st.FindLowestPriorityPreemptible(SlotClassHindsight, stubRegistry{})
	if !ok {
		t.Fatal("expected a hindsight-class victim")
	}
	if agent != "hind-0" || slot != 2 {
		t.Fatalf("hindsight victim = (%s,%d); want (hind-0,2)", agent, slot)
	}
}

func TestClassOf(t *testing.T) {
	st := NewSlotTableWithClasses(1, 1)
	if st.ClassOf(0) != SlotClassAgent {
		t.Fatalf("slot 0 class = %q", st.ClassOf(0))
	}
	if st.ClassOf(1) != SlotClassHindsight {
		t.Fatalf("slot 1 class = %q", st.ClassOf(1))
	}
	if st.ClassOf(-1) != "" || st.ClassOf(99) != "" {
		t.Fatal("out-of-range ClassOf should return empty")
	}
}

func TestNormalizeClass(t *testing.T) {
	cases := []struct {
		in      string
		wantCls SlotClass
		wantOK  bool
	}{
		{"", SlotClassAgent, true},
		{"agent", SlotClassAgent, true},
		{"hindsight", SlotClassHindsight, true},
		{"bogus", "", false},
		{"AGENT", "", false}, // case-sensitive
	}
	for _, tc := range cases {
		got, ok := NormalizeClass(tc.in)
		if got != tc.wantCls || ok != tc.wantOK {
			t.Errorf("NormalizeClass(%q) = (%q, %v); want (%q, %v)", tc.in, got, ok, tc.wantCls, tc.wantOK)
		}
	}
}

func TestReleasePreservesClass(t *testing.T) {
	st := NewSlotTableWithClasses(1, 1)
	st.Assign(1, "hind-a", 0)
	st.Release(1)
	if st.ClassOf(1) != SlotClassHindsight {
		t.Fatalf("class lost after release: %q", st.ClassOf(1))
	}
	if st.slots[1].State != SlotUnassigned {
		t.Fatal("slot should be unassigned after release")
	}
}
