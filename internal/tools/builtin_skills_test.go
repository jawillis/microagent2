package tools

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"microagent2/internal/skills"
)

func populatedStore(t *testing.T) *skills.Store {
	t.Helper()
	root := t.TempDir()
	for _, s := range []struct{ name, desc, body string }{
		{"a", "first", "a body"},
		{"b", "second", "b body"},
	} {
		dir := filepath.Join(root, s.name)
		_ = os.MkdirAll(dir, 0o755)
		content := "---\nname: " + s.name + "\ndescription: " + s.desc + "\n---\n\n" + s.body + "\n"
		_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644)
	}
	return skills.NewStore(root, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestListSkills_Empty(t *testing.T) {
	store := skills.NewStore(t.TempDir(), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	tool := NewListSkills(store)
	out, err := tool.Invoke(context.Background(), "{}")
	if err != nil {
		t.Fatal(err)
	}
	if out != "[]" {
		t.Fatalf("out: %q", out)
	}
}

func TestListSkills_Populated(t *testing.T) {
	tool := NewListSkills(populatedStore(t))
	out, err := tool.Invoke(context.Background(), "{}")
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"a","description":"first"},{"name":"b","description":"second"}]`
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
}

func TestListSkills_IgnoresArgs(t *testing.T) {
	tool := NewListSkills(populatedStore(t))
	out, err := tool.Invoke(context.Background(), `{"garbage": true}`)
	if err != nil {
		t.Fatal(err)
	}
	if out == "" || out == "[]" {
		t.Fatalf("expected populated list, got %q", out)
	}
}

func TestReadSkill_Hit(t *testing.T) {
	tool := NewReadSkill(populatedStore(t))
	out, err := tool.Invoke(context.Background(), `{"name":"a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "a body\n" {
		t.Fatalf("body: %q", out)
	}
}

func TestReadSkill_Miss(t *testing.T) {
	tool := NewReadSkill(populatedStore(t))
	out, err := tool.Invoke(context.Background(), `{"name":"missing"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"error":"skill not found: missing"}` {
		t.Fatalf("out: %q", out)
	}
}

func TestReadSkill_EmptyName(t *testing.T) {
	tool := NewReadSkill(populatedStore(t))
	out, err := tool.Invoke(context.Background(), `{"name":""}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"error":"name argument is required"}` {
		t.Fatalf("out: %q", out)
	}
}

func TestReadSkill_MalformedArgs(t *testing.T) {
	tool := NewReadSkill(populatedStore(t))
	out, err := tool.Invoke(context.Background(), `not json`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < len(`{"error":"invalid arguments: `) || out[:len(`{"error":"invalid arguments: `)] != `{"error":"invalid arguments: ` {
		t.Fatalf("out: %q", out)
	}
}
