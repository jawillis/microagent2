package exec

import "testing"

func TestHealth_StartingByDefault(t *testing.T) {
	h := NewHealth()
	if h.IsReady() {
		t.Fatal("should start as not ready")
	}
	s := h.Snapshot()
	if s.Status != "starting" || s.Ready {
		t.Fatalf("snapshot: %+v", s)
	}
}

func TestHealth_ReadyTransition(t *testing.T) {
	h := NewHealth()
	h.Ready()
	if !h.IsReady() {
		t.Fatal("should be ready after Ready()")
	}
	s := h.Snapshot()
	if s.Status != "ok" || !s.Ready {
		t.Fatalf("snapshot: %+v", s)
	}
}

func TestHealth_InstalledAndFailed(t *testing.T) {
	h := NewHealth()
	h.Installed("foo")
	h.InstallFailed("bar", "boom")
	s := h.Snapshot()

	if len(s.PrewarmedSkills) != 1 || s.PrewarmedSkills[0] != "foo" {
		t.Errorf("prewarmed: %+v", s.PrewarmedSkills)
	}
	if len(s.FailedInstalls) != 1 || s.FailedInstalls[0].Skill != "bar" || s.FailedInstalls[0].Error != "boom" {
		t.Errorf("failed: %+v", s.FailedInstalls)
	}
	if s.FailedInstalls[0].At == "" {
		t.Errorf("failed entry missing At")
	}
}

func TestHealth_SuccessfulRetryClearsFailure(t *testing.T) {
	h := NewHealth()
	h.InstallFailed("foo", "boom")
	h.Installed("foo") // retry succeeded

	s := h.Snapshot()
	if len(s.FailedInstalls) != 0 {
		t.Errorf("failed should be empty: %+v", s.FailedInstalls)
	}
	if len(s.PrewarmedSkills) != 1 || s.PrewarmedSkills[0] != "foo" {
		t.Errorf("prewarmed should contain foo: %+v", s.PrewarmedSkills)
	}
}

func TestHealth_FailureSupersedesSuccess(t *testing.T) {
	h := NewHealth()
	h.Installed("foo")
	h.InstallFailed("foo", "regression")

	s := h.Snapshot()
	if len(s.PrewarmedSkills) != 0 {
		t.Errorf("success should be cleared when a subsequent install fails: %+v", s.PrewarmedSkills)
	}
	if len(s.FailedInstalls) != 1 {
		t.Errorf("failed should have foo: %+v", s.FailedInstalls)
	}
}

func TestHealth_SortedOutput(t *testing.T) {
	h := NewHealth()
	for _, s := range []string{"zulu", "alpha", "mike"} {
		h.Installed(s)
	}
	for _, s := range []string{"zap", "alph"} {
		h.InstallFailed(s, "e")
	}
	snap := h.Snapshot()
	wantP := []string{"alpha", "mike", "zulu"}
	for i, want := range wantP {
		if snap.PrewarmedSkills[i] != want {
			t.Errorf("prewarm[%d] = %q, want %q", i, snap.PrewarmedSkills[i], want)
		}
	}
	wantF := []string{"alph", "zap"}
	for i, want := range wantF {
		if snap.FailedInstalls[i].Skill != want {
			t.Errorf("failed[%d] = %q, want %q", i, snap.FailedInstalls[i].Skill, want)
		}
	}
}
