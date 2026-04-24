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
	}, "")
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
	req := buildSkillRetainRequest(ExtractedSkill{Concept: "x", ProblemClass: "p"}, "")
	if req.Metadata["provenance"] != "implicit" {
		t.Fatalf("provenance = %q", req.Metadata["provenance"])
	}
}

func TestBuildSkillRetainRequest_EmptyConceptReturnsEmpty(t *testing.T) {
	req := buildSkillRetainRequest(ExtractedSkill{}, "")
	if req.Content != "" {
		t.Fatalf("content = %q; want empty", req.Content)
	}
}

func TestBuildSkillRetainRequest_FallsBackToProblemClassForContent(t *testing.T) {
	req := buildSkillRetainRequest(ExtractedSkill{
		ProblemClass: "flaky CI tests",
		Approach:     "isolate fixtures",
	}, "")
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

// --- speaker / fact_type / entities ---

func TestBuildExtractRetainRequest_SpeakerAboutSelf(t *testing.T) {
	req := buildExtractRetainRequest(ExtractedMemory{
		Content:   "I prefer dark roast coffee",
		SpeakerID: "jason",
		FactType:  "person_fact",
		Entities:  []string{"jason"},
	})
	if req.Metadata["speaker_id"] != "jason" {
		t.Fatalf("speaker_id = %q; want jason", req.Metadata["speaker_id"])
	}
	if req.Metadata["fact_type"] != "person_fact" {
		t.Fatalf("fact_type = %q; want person_fact", req.Metadata["fact_type"])
	}
	if len(req.Entities) != 1 || req.Entities[0] != "jason" {
		t.Fatalf("entities = %v; want [jason]", req.Entities)
	}
}

func TestBuildExtractRetainRequest_SpeakerAboutOther(t *testing.T) {
	req := buildExtractRetainRequest(ExtractedMemory{
		Content:   "Alice likes green tea",
		SpeakerID: "jason",
		FactType:  "person_fact",
		Entities:  []string{"alice"},
	})
	if req.Metadata["speaker_id"] != "jason" {
		t.Fatalf("speaker_id = %q; want jason (who stated it)", req.Metadata["speaker_id"])
	}
	if len(req.Entities) != 1 || req.Entities[0] != "alice" {
		t.Fatalf("entities = %v; want [alice] (who it's about)", req.Entities)
	}
}

func TestBuildExtractRetainRequest_WorldFact(t *testing.T) {
	req := buildExtractRetainRequest(ExtractedMemory{
		Content:   "Go is a compiled language",
		SpeakerID: "jason",
		FactType:  "world_fact",
	})
	if req.Metadata["fact_type"] != "world_fact" {
		t.Fatalf("fact_type = %q; want world_fact", req.Metadata["fact_type"])
	}
	if len(req.Entities) != 0 {
		t.Fatalf("entities = %v; want empty", req.Entities)
	}
}

func TestBuildExtractRetainRequest_OmittedSpeakerNotInMetadata(t *testing.T) {
	req := buildExtractRetainRequest(ExtractedMemory{Content: "hello"})
	if _, ok := req.Metadata["speaker_id"]; ok {
		t.Fatalf("speaker_id should be absent when not set; got %q", req.Metadata["speaker_id"])
	}
}

func TestBuildSkillRetainRequest_EmitsSpeakerID(t *testing.T) {
	req := buildSkillRetainRequest(ExtractedSkill{
		Concept:      "Diagnosing flaky tests",
		ProblemClass: "flaky tests",
	}, "alice")
	if req.Metadata["speaker_id"] != "alice" {
		t.Fatalf("speaker_id = %q; want alice", req.Metadata["speaker_id"])
	}
}

func TestBuildSkillRetainRequest_NoSpeakerWhenEmpty(t *testing.T) {
	req := buildSkillRetainRequest(ExtractedSkill{
		Concept:      "Diagnosing flaky tests",
		ProblemClass: "flaky tests",
	}, "")
	if _, ok := req.Metadata["speaker_id"]; ok {
		t.Fatalf("speaker_id should be absent; got %q", req.Metadata["speaker_id"])
	}
}

func TestBuildMemoryExtractionPrompt_IncludesSpeakerContext(t *testing.T) {
	msgs := buildMemoryExtractionPrompt(nil, "alice")
	if len(msgs) < 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if !strings.Contains(msgs[1].Content, "Speaker ID for this conversation: alice") {
		t.Fatalf("user prompt missing speaker context: %q", msgs[1].Content)
	}
}

func TestBuildMemoryExtractionPrompt_NoSpeakerOmitsContext(t *testing.T) {
	msgs := buildMemoryExtractionPrompt(nil, "")
	if strings.Contains(msgs[1].Content, "Speaker ID") {
		t.Fatalf("user prompt should not contain speaker context when empty")
	}
}

func TestParseMemories_SpeakerAndFactType(t *testing.T) {
	input := `[{"content":"likes tea","speaker_id":"alice","fact_type":"person_fact","entities":["alice"]}]`
	mems, err := parseMemories(input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("expected 1, got %d", len(mems))
	}
	if mems[0].SpeakerID != "alice" {
		t.Fatalf("speaker_id = %q", mems[0].SpeakerID)
	}
	if mems[0].FactType != "person_fact" {
		t.Fatalf("fact_type = %q", mems[0].FactType)
	}
	if len(mems[0].Entities) != 1 || mems[0].Entities[0] != "alice" {
		t.Fatalf("entities = %v", mems[0].Entities)
	}
}
