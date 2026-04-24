package tools

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

// storeWithFile creates a store containing one skill "demo" with the given
// extra files (path relative to the skill root).
func storeWithFile(t *testing.T, files map[string]string) *skills.Store {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: d\n---\nbody\n"), 0o644)
	for rel, contents := range files {
		full := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(contents), 0o644)
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

// ---- Added for skills-format-subdirs ----

func TestReadSkillFile_Hit(t *testing.T) {
	store := storeWithFile(t, map[string]string{"notes.md": "hello\n"})
	tool := NewReadSkillFile(store)
	out, err := tool.Invoke(context.Background(), `{"skill":"demo","path":"notes.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello\n" {
		t.Fatalf("out: %q", out)
	}
}

func TestReadSkillFile_UnknownSkill(t *testing.T) {
	store := storeWithFile(t, nil)
	tool := NewReadSkillFile(store)
	out, err := tool.Invoke(context.Background(), `{"skill":"missing","path":"x.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"error":"skill not found: missing"}` {
		t.Fatalf("out: %q", out)
	}
}

func TestReadSkillFile_MalformedArgs(t *testing.T) {
	store := storeWithFile(t, nil)
	tool := NewReadSkillFile(store)
	out, err := tool.Invoke(context.Background(), `not json`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, `{"error":"invalid arguments: `) {
		t.Fatalf("out: %q", out)
	}
}

func TestReadSkillFile_MissingArgs(t *testing.T) {
	store := storeWithFile(t, nil)
	tool := NewReadSkillFile(store)

	for _, args := range []string{`{}`, `{"skill":""}`, `{"path":"x.md"}`, `{"skill":"demo","path":""}`} {
		out, err := tool.Invoke(context.Background(), args)
		if err != nil {
			t.Fatalf("args=%s err=%v", args, err)
		}
		if out != `{"error":"skill and path arguments are required"}` {
			t.Fatalf("args=%s out=%q", args, out)
		}
	}
}

func TestReadSkillFile_PathRejectionPassesThrough(t *testing.T) {
	store := storeWithFile(t, nil)
	tool := NewReadSkillFile(store)

	out, err := tool.Invoke(context.Background(), `{"skill":"demo","path":"../../etc/passwd"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, `{"error":"`) {
		t.Fatalf("out: %q", out)
	}
	if !strings.Contains(out, "escape") && !strings.Contains(out, "root") {
		t.Fatalf("expected error message to mention escape/root; got: %q", out)
	}
}

func TestReadSkillFile_SKILLMdRedirects(t *testing.T) {
	store := storeWithFile(t, nil)
	tool := NewReadSkillFile(store)

	out, err := tool.Invoke(context.Background(), `{"skill":"demo","path":"SKILL.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, `{"error":"`) {
		t.Fatalf("expected error envelope, got %q", out)
	}
	if !strings.Contains(out, "read_skill") {
		t.Fatalf("error should redirect to read_skill, got %q", out)
	}
}
