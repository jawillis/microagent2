package retro

import (
	"context"
	"io"
	"log/slog"
	"testing"

	appcontext "microagent2/internal/context"
)

type fakeMuninn struct {
	consolidated []consolidateCall
	evolved      []evolveCall
	deleted      []string
	failNext     bool
}

type consolidateCall struct {
	ids           []string
	mergedContent string
}
type evolveCall struct {
	id      string
	content string
}

func (f *fakeMuninn) Recall(ctx context.Context, query string, limit int) ([]appcontext.Memory, error) {
	return nil, nil
}
func (f *fakeMuninn) Consolidate(ctx context.Context, ids []string, merged string) (string, error) {
	if f.failNext {
		f.failNext = false
		return "", errFake
	}
	f.consolidated = append(f.consolidated, consolidateCall{ids: ids, mergedContent: merged})
	return "new-id", nil
}
func (f *fakeMuninn) Evolve(ctx context.Context, id, content, summary string) (string, error) {
	f.evolved = append(f.evolved, evolveCall{id: id, content: content})
	return "new-id", nil
}
func (f *fakeMuninn) Delete(ctx context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }

const errFake = sentinelErr("fake failure")

func newCurationJobForTest(muninn curationMuninn) *CurationJob {
	return &CurationJob{
		muninn:     muninn,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		categories: []string{"test"},
	}
}

func entries(ids ...string) []appcontext.Memory {
	m := make([]appcontext.Memory, len(ids))
	for i, id := range ids {
		m[i] = appcontext.Memory{ID: id, Content: "content-" + id}
	}
	return m
}

func TestCuration_MergeAction(t *testing.T) {
	f := &fakeMuninn{}
	j := newCurationJobForTest(f)
	s := j.executeCurationActions(context.Background(), "fact", entries("a", "b", "c"),
		[]curationAction{{Action: "merge", Indices: []int{0, 1}, MergedContent: "merged"}})

	if s.merged != 1 {
		t.Errorf("merged = %d, want 1", s.merged)
	}
	if len(f.consolidated) != 1 {
		t.Fatalf("consolidated calls = %d, want 1", len(f.consolidated))
	}
	if f.consolidated[0].ids[0] != "a" || f.consolidated[0].ids[1] != "b" {
		t.Errorf("ids = %v, want [a b]", f.consolidated[0].ids)
	}
	if f.consolidated[0].mergedContent != "merged" {
		t.Errorf("merged_content = %q", f.consolidated[0].mergedContent)
	}
}

func TestCuration_EvolveAction(t *testing.T) {
	f := &fakeMuninn{}
	j := newCurationJobForTest(f)
	s := j.executeCurationActions(context.Background(), "fact", entries("a", "b"),
		[]curationAction{{Action: "evolve", Indices: []int{1}, MergedContent: "refined"}})

	if s.evolved != 1 {
		t.Errorf("evolved = %d, want 1", s.evolved)
	}
	if len(f.evolved) != 1 || f.evolved[0].id != "b" || f.evolved[0].content != "refined" {
		t.Errorf("evolved call wrong: %+v", f.evolved)
	}
}

func TestCuration_DeleteAction(t *testing.T) {
	f := &fakeMuninn{}
	j := newCurationJobForTest(f)
	s := j.executeCurationActions(context.Background(), "fact", entries("a", "b"),
		[]curationAction{{Action: "delete", Indices: []int{0}}})

	if s.deleted != 1 {
		t.Errorf("deleted = %d, want 1", s.deleted)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "a" {
		t.Errorf("deleted = %v", f.deleted)
	}
}

func TestCuration_UnknownActionSkipped(t *testing.T) {
	f := &fakeMuninn{}
	j := newCurationJobForTest(f)
	s := j.executeCurationActions(context.Background(), "fact", entries("a", "b"),
		[]curationAction{{Action: "purge", Indices: []int{0}}})

	if s.skipped != 1 {
		t.Errorf("skipped = %d, want 1", s.skipped)
	}
	if s.merged+s.evolved+s.deleted != 0 {
		t.Errorf("unexpected action count: %+v", s)
	}
	if len(f.consolidated)+len(f.evolved)+len(f.deleted) != 0 {
		t.Errorf("Muninn was called for an unknown action")
	}
}

func TestCuration_MergeWithInsufficientIndicesSkipped(t *testing.T) {
	f := &fakeMuninn{}
	j := newCurationJobForTest(f)
	s := j.executeCurationActions(context.Background(), "fact", entries("a", "b"),
		[]curationAction{{Action: "merge", Indices: []int{0}, MergedContent: "x"}})

	if s.skipped != 1 {
		t.Errorf("skipped = %d, want 1", s.skipped)
	}
	if len(f.consolidated) != 0 {
		t.Errorf("should not have called Consolidate: %v", f.consolidated)
	}
}

func TestCuration_EvolveWithoutMergedContentSkipped(t *testing.T) {
	f := &fakeMuninn{}
	j := newCurationJobForTest(f)
	s := j.executeCurationActions(context.Background(), "fact", entries("a"),
		[]curationAction{{Action: "evolve", Indices: []int{0}, MergedContent: "   "}})

	if s.skipped != 1 {
		t.Errorf("skipped = %d, want 1", s.skipped)
	}
}

func TestCuration_IndexOutOfRangeSkipped(t *testing.T) {
	f := &fakeMuninn{}
	j := newCurationJobForTest(f)
	s := j.executeCurationActions(context.Background(), "fact", entries("a"),
		[]curationAction{{Action: "delete", Indices: []int{5}}})

	if s.skipped != 1 {
		t.Errorf("skipped = %d, want 1", s.skipped)
	}
	if len(f.deleted) != 0 {
		t.Errorf("should not have called Delete")
	}
}

func TestCuration_FailedActionDoesNotAbortBatch(t *testing.T) {
	f := &fakeMuninn{failNext: true}
	j := newCurationJobForTest(f)
	s := j.executeCurationActions(context.Background(), "fact", entries("a", "b", "c"),
		[]curationAction{
			{Action: "merge", Indices: []int{0, 1}, MergedContent: "x"},
			{Action: "delete", Indices: []int{2}},
		})

	if s.merged != 0 {
		t.Errorf("merged = %d, want 0 (first call failed)", s.merged)
	}
	if s.deleted != 1 {
		t.Errorf("deleted = %d, want 1 (second call should still fire)", s.deleted)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "c" {
		t.Errorf("delete not executed: %v", f.deleted)
	}
}

func TestMemoryToStoredSpec_ValidatesEnumAndConfidence(t *testing.T) {
	cases := []struct {
		name    string
		in      ExtractedMemory
		wantMT  string
		wantCnf float64
	}{
		{
			name:    "valid enum and confidence",
			in:      ExtractedMemory{Concept: "c", Content: "x", MemoryType: "fact", Confidence: 0.8},
			wantMT:  "fact",
			wantCnf: 0.8,
		},
		{
			name:    "invalid enum is dropped",
			in:      ExtractedMemory{Concept: "c", Content: "x", MemoryType: "nonsense", Confidence: 0.5},
			wantMT:  "",
			wantCnf: 0.5,
		},
		{
			name:    "out of range confidence dropped",
			in:      ExtractedMemory{Concept: "c", Content: "x", MemoryType: "fact", Confidence: 1.5},
			wantMT:  "fact",
			wantCnf: 0.0,
		},
		{
			name:    "zero confidence dropped",
			in:      ExtractedMemory{Concept: "c", Content: "x", MemoryType: "fact", Confidence: 0.0},
			wantMT:  "fact",
			wantCnf: 0.0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := memoryToStoredSpec(tc.in)
			if got.MemoryType != tc.wantMT {
				t.Errorf("MemoryType = %q, want %q", got.MemoryType, tc.wantMT)
			}
			if got.Confidence != tc.wantCnf {
				t.Errorf("Confidence = %v, want %v", got.Confidence, tc.wantCnf)
			}
		})
	}
}

func TestSkillToStoredSpec_PinsMemoryTypeProcedure(t *testing.T) {
	got := skillToStoredSpec(ExtractedSkill{
		Concept:      "Approach for flaky tests",
		ProblemClass: "flaky tests",
		Approach:     "isolate fixtures",
		Outcome:      "stable",
		Confidence:   0.9,
	})
	if got.MemoryType != "procedure" {
		t.Errorf("MemoryType = %q, want procedure", got.MemoryType)
	}
	if got.Concept != "Approach for flaky tests" {
		t.Errorf("Concept = %q", got.Concept)
	}
}
