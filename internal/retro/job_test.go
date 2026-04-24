package retro

import (
	"strings"
	"testing"

	"microagent2/internal/memoryclient"
)

// --- buildExtractRetainRequest ---

func TestBuildExtractRetainRequest_ProvenanceDefaultsExplicit(t *testing.T) {
	req := buildExtractRetainRequest(ExtractedMemory{
		Content: "Jason prefers dark roast coffee",
	})
	if req.Metadata["provenance"] != "explicit" {
		t.Fatalf("provenance = %q", req.Metadata["provenance"])
	}
}

func TestBuildExtractRetainRequest_ConfidenceSerializedAsString(t *testing.T) {
	req := buildExtractRetainRequest(ExtractedMemory{
		Content:    "c",
		Confidence: 0.65,
	})
	if req.Metadata["confidence"] != "0.65" {
		t.Fatalf("confidence = %q", req.Metadata["confidence"])
	}
}

func TestBuildExtractRetainRequest_StripsCorrectionsTag(t *testing.T) {
	req := buildExtractRetainRequest(ExtractedMemory{
		Content: "Jason now prefers light roast",
		Tags:    []string{"preferences", "coffee", "corrections"},
	})
	for _, tag := range req.Tags {
		if strings.EqualFold(tag, "corrections") || strings.EqualFold(tag, "correction") {
			t.Fatalf("corrections tag should be stripped, got %v", req.Tags)
		}
	}
}

func TestBuildExtractRetainRequest_IsCorrectionGoesToMetadata(t *testing.T) {
	req := buildExtractRetainRequest(ExtractedMemory{
		Content:      "Jason now prefers light roast",
		Tags:         []string{"preferences", "coffee"},
		IsCorrection: true,
	})
	if req.Metadata["is_correction"] != "true" {
		t.Fatalf("is_correction metadata = %q; want true", req.Metadata["is_correction"])
	}
}

func TestBuildExtractRetainRequest_NoObservationScopes(t *testing.T) {
	// Leave observation_scopes empty so Hindsight's default "combined" is
	// used. "per_tag" creates one observation entry per tag, which duplicates
	// memories that legitimately span multiple tags.
	req := buildExtractRetainRequest(ExtractedMemory{Content: "x", Tags: []string{"preferences"}})
	if req.ObservationScopes != "" {
		t.Fatalf("observation_scopes = %q; want empty", req.ObservationScopes)
	}
}

func TestBuildExtractRetainRequest_MemoryTypeGoesToMetadata(t *testing.T) {
	req := buildExtractRetainRequest(ExtractedMemory{Content: "x", MemoryType: "preference"})
	if req.Metadata["memory_type_hint"] != "preference" {
		t.Fatalf("memory_type_hint = %q", req.Metadata["memory_type_hint"])
	}
}

func TestBuildExtractRetainRequest_TrimsContent(t *testing.T) {
	req := buildExtractRetainRequest(ExtractedMemory{Content: "  hello  "})
	if req.Content != "hello" {
		t.Fatalf("content = %q", req.Content)
	}
}

func TestBuildExtractRetainRequest_DedupesTags(t *testing.T) {
	req := buildExtractRetainRequest(ExtractedMemory{
		Content: "x",
		Tags:    []string{"coffee", "coffee", "morning", ""},
	})
	if len(req.Tags) != 2 || req.Tags[0] != "coffee" || req.Tags[1] != "morning" {
		t.Fatalf("tags = %v", req.Tags)
	}
}

// --- buildSkillRetainRequest ---

func TestBuildSkillRetainRequest_AlwaysIncludesSkillTag(t *testing.T) {
	req := buildSkillRetainRequest(ExtractedSkill{
		Concept:      "Diagnosing flaky CI tests",
		ProblemClass: "flaky tests",
		Approach:     "isolate shared fixtures",
		Outcome:      "green",
	})
	found := false
	for _, t := range req.Tags {
		if t == "skill" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("tags = %v", req.Tags)
	}
}

func TestBuildSkillRetainRequest_ProvenanceImplicit(t *testing.T) {
	req := buildSkillRetainRequest(ExtractedSkill{Concept: "x", ProblemClass: "p"})
	if req.Metadata["provenance"] != "implicit" {
		t.Fatalf("provenance = %q", req.Metadata["provenance"])
	}
}

func TestBuildSkillRetainRequest_EmptyConceptReturnsEmpty(t *testing.T) {
	req := buildSkillRetainRequest(ExtractedSkill{})
	if req.Content != "" {
		t.Fatalf("content = %q; want empty", req.Content)
	}
}

func TestBuildSkillRetainRequest_FallsBackToProblemClassForContent(t *testing.T) {
	req := buildSkillRetainRequest(ExtractedSkill{
		ProblemClass: "flaky CI tests",
		Approach:     "isolate fixtures",
	})
	if !containsLine(req.Content, "flaky CI tests") {
		t.Fatalf("content missing problem_class: %q", req.Content)
	}
}

// --- parseMemories / parseSkills ---

func TestParseMemories_StripsJSONFences(t *testing.T) {
	mems, err := parseMemories("```json\n[{\"content\":\"hi\"}]\n```")
	if err != nil {
		t.Fatalf("parseMemories: %v", err)
	}
	if len(mems) != 1 || mems[0].Content != "hi" {
		t.Fatalf("mems = %+v", mems)
	}
}

func TestParseMemories_EmptyArray(t *testing.T) {
	mems, err := parseMemories("[]")
	if err != nil {
		t.Fatalf("parseMemories: %v", err)
	}
	if len(mems) != 0 {
		t.Fatalf("mems = %+v", mems)
	}
}

func TestParseSkills_StripsFences(t *testing.T) {
	skills, err := parseSkills("```\n[{\"problem_class\":\"x\"}]\n```")
	if err != nil {
		t.Fatalf("parseSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("skills = %+v", skills)
	}
}

// --- isDuplicateSkill ---

func TestIsDuplicateSkill_HighOverlapFlaggedAsDuplicate(t *testing.T) {
	existing := []memoryclient.MemorySummary{{Content: "flaky intermittent CI tests"}}
	skill := ExtractedSkill{ProblemClass: "flaky intermittent CI tests"}
	if !isDuplicateSkill(skill, existing, 0.85) {
		t.Fatal("expected duplicate")
	}
}

func TestIsDuplicateSkill_LowOverlapNotFlagged(t *testing.T) {
	existing := []memoryclient.MemorySummary{{Content: "completely unrelated words"}}
	skill := ExtractedSkill{ProblemClass: "flaky intermittent CI tests"}
	if isDuplicateSkill(skill, existing, 0.85) {
		t.Fatal("did not expect duplicate")
	}
}

func TestIsDuplicateSkill_EmptyExistingNotDuplicate(t *testing.T) {
	if isDuplicateSkill(ExtractedSkill{ProblemClass: "x"}, nil, 0.85) {
		t.Fatal("empty existing should not be duplicate")
	}
}

// --- helpers ---

func containsLine(s, line string) bool {
	for _, seg := range splitLines(s) {
		if seg == line || seg == "Problem: "+line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i, r := range s {
		if r == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
