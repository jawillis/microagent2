package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"microagent2/internal/messaging"
	"microagent2/internal/skills"
)

func skillsStoreWith(t *testing.T, entries map[string]string) *skills.Store {
	t.Helper()
	root := t.TempDir()
	for name, desc := range entries {
		dir := filepath.Join(root, name)
		_ = os.MkdirAll(dir, 0o755)
		content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\nbody\n"
		_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644)
	}
	return skills.NewStore(root, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestInjectSkillManifest_AppendsBlock(t *testing.T) {
	store := skillsStoreWith(t, map[string]string{"a": "A", "b": "B"})
	in := []messaging.ChatMsg{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "hi"},
	}

	out := injectSkillManifest(in, store)

	wantSuffix := "\n\n<available_skills>\n- a: A\n- b: B\n</available_skills>"
	if out[0].Content != "You are helpful."+wantSuffix {
		t.Fatalf("content: %q", out[0].Content)
	}
	// Caller's slice untouched
	if in[0].Content != "You are helpful." {
		t.Fatalf("caller mutated: %q", in[0].Content)
	}
}

func TestInjectSkillManifest_NoopWhenEmptyStore(t *testing.T) {
	store := skillsStoreWith(t, nil)
	in := []messaging.ChatMsg{
		{Role: "system", Content: "prompt"},
		{Role: "user", Content: "hi"},
	}
	out := injectSkillManifest(in, store)
	if out[0].Content != "prompt" {
		t.Fatalf("content: %q", out[0].Content)
	}
}

func TestInjectSkillManifest_NoopWhenNoSystemFirst(t *testing.T) {
	store := skillsStoreWith(t, map[string]string{"a": "A"})
	in := []messaging.ChatMsg{
		{Role: "user", Content: "hi"},
	}
	out := injectSkillManifest(in, store)
	if out[0].Content != "hi" {
		t.Fatalf("content: %q", out[0].Content)
	}
}
