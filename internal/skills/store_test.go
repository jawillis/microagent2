package skills

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, root, name, contents string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestStore_ValidSkillParses(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "estimate-tokens", `---
name: estimate-tokens
description: Estimate token count for a body of text
---

Apply the 4-chars-per-token rule of thumb.
`)

	s := NewStore(root, silentLogger())
	m, ok := s.Get("estimate-tokens")
	if !ok {
		t.Fatal("skill not registered")
	}
	if m.Name != "estimate-tokens" || m.Description != "Estimate token count for a body of text" {
		t.Fatalf("manifest: %+v", m)
	}
	if m.Body() != "Apply the 4-chars-per-token rule of thumb.\n" {
		t.Fatalf("body: %q", m.Body())
	}
}

func TestStore_MissingNameSkipped(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "bad", `---
description: no name here
---

body
`)
	s := NewStore(root, silentLogger())
	if _, ok := s.Get("bad"); ok {
		t.Fatal("skill with missing name should not be registered")
	}
	if len(s.List()) != 0 {
		t.Fatalf("expected empty list, got %d", len(s.List()))
	}
}

func TestStore_MalformedFrontmatterSkipped(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "bad", "no frontmatter here, just markdown\n")
	s := NewStore(root, silentLogger())
	if len(s.List()) != 0 {
		t.Fatalf("expected empty list, got %d", len(s.List()))
	}
}

func TestStore_MissingRootNonFatal(t *testing.T) {
	s := NewStore("/does/not/exist/ever", silentLogger())
	if len(s.List()) != 0 {
		t.Fatalf("expected empty list, got %d", len(s.List()))
	}
}

func TestStore_AlphabeticalOrder(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"c", "a", "b"} {
		writeSkill(t, root, n, "---\nname: "+n+"\ndescription: desc-"+n+"\n---\nbody\n")
	}
	s := NewStore(root, silentLogger())
	list := s.List()
	if len(list) != 3 {
		t.Fatalf("len: %d", len(list))
	}
	for i, want := range []string{"a", "b", "c"} {
		if list[i].Name != want {
			t.Fatalf("list[%d] = %q want %q", i, list[i].Name, want)
		}
	}
}

func TestStore_BodyExactBytes(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "x", "---\nname: x\ndescription: d\n---\n\nline1\nline2\n")
	s := NewStore(root, silentLogger())
	body, ok := s.Body("x")
	if !ok {
		t.Fatal("body not found")
	}
	if body != "line1\nline2\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestStore_OptionalFieldsParsed(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "optional", `---
name: optional
description: has extras
allowed-tools:
  - list_skills
  - read_skill
model: gpt-4
---

body
`)
	s := NewStore(root, silentLogger())
	m, ok := s.Get("optional")
	if !ok {
		t.Fatal("not found")
	}
	if len(m.AllowedTools) != 2 || m.AllowedTools[0] != "list_skills" || m.AllowedTools[1] != "read_skill" {
		t.Fatalf("allowed-tools: %+v", m.AllowedTools)
	}
	if m.Model != "gpt-4" {
		t.Fatalf("model: %q", m.Model)
	}
}
