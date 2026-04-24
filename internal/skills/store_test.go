package skills

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

// ---- Added for skills-format-subdirs ----

func TestManifest_RootIsSkillDirectory(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "has-root", "---\nname: has-root\ndescription: d\n---\nbody\n")

	s := NewStore(root, silentLogger())
	m, ok := s.Get("has-root")
	if !ok {
		t.Fatal("not registered")
	}
	want := filepath.Join(root, "has-root")
	if m.Root() != want {
		t.Fatalf("Root = %q, want %q", m.Root(), want)
	}
}

// makeSkillWithFiles creates a skill directory containing SKILL.md plus
// arbitrary extra files. Returns the skills root.
func makeSkillWithFiles(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	writeSkill(t, root, name, "---\nname: "+name+"\ndescription: d\n---\nbody\n")
	dir := filepath.Join(root, name)
	for rel, contents := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return root
}

func TestReadFile_UnknownSkill(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root, silentLogger())
	body, found, err := s.ReadFile("nonexistent", "anything.md")
	if err != nil || found || body != "" {
		t.Fatalf("got (%q, %v, %v); want (\"\", false, nil)", body, found, err)
	}
}

func TestReadFile_ValidRelativePath(t *testing.T) {
	root := makeSkillWithFiles(t, "ok", map[string]string{
		"notes.md": "hello world\n",
	})
	s := NewStore(root, silentLogger())

	body, found, err := s.ReadFile("ok", "notes.md")
	if err != nil || !found {
		t.Fatalf("unexpected: found=%v err=%v", found, err)
	}
	if body != "hello world\n" {
		t.Fatalf("body: %q", body)
	}
}

func TestReadFile_NestedRelativePath(t *testing.T) {
	root := makeSkillWithFiles(t, "nest", map[string]string{
		"reference/best.md": "deep\n",
	})
	s := NewStore(root, silentLogger())

	body, found, err := s.ReadFile("nest", "reference/best.md")
	if err != nil || !found || body != "deep\n" {
		t.Fatalf("unexpected: (%q, %v, %v)", body, found, err)
	}
}

func TestReadFile_AbsolutePathRejected(t *testing.T) {
	root := makeSkillWithFiles(t, "abs", map[string]string{"x.md": "ok\n"})
	s := NewStore(root, silentLogger())

	_, found, err := s.ReadFile("abs", "/etc/passwd")
	if !found {
		t.Fatal("expected found=true for known skill")
	}
	if !errors.Is(err, ErrPathAbsolute) {
		t.Fatalf("err = %v, want ErrPathAbsolute", err)
	}
}

func TestReadFile_TraversalRejectedAndNotRead(t *testing.T) {
	root := makeSkillWithFiles(t, "trav", map[string]string{"inside.md": "safe\n"})
	// Canary file outside any skill root — must never be read.
	canary := filepath.Join(root, "CANARY_DO_NOT_READ")
	canaryContents := []byte("ATTACKER_WOULD_SEE_THIS\n")
	if err := os.WriteFile(canary, canaryContents, 0o644); err != nil {
		t.Fatalf("canary: %v", err)
	}
	s := NewStore(root, silentLogger())

	body, found, err := s.ReadFile("trav", "../CANARY_DO_NOT_READ")
	if !found {
		t.Fatal("expected found=true for known skill")
	}
	if !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("err = %v, want ErrPathEscapesRoot", err)
	}
	if bytes.Contains([]byte(body), canaryContents) {
		t.Fatal("canary contents leaked into ReadFile result")
	}
}

func TestReadFile_SKILLMdRejectedWithRedirect(t *testing.T) {
	root := makeSkillWithFiles(t, "skl", map[string]string{})
	s := NewStore(root, silentLogger())

	_, found, err := s.ReadFile("skl", "SKILL.md")
	if !found {
		t.Fatal("expected found=true")
	}
	if !errors.Is(err, ErrPathReservedSKILL) {
		t.Fatalf("err = %v, want ErrPathReservedSKILL", err)
	}
	if !strings.Contains(err.Error(), "read_skill") {
		t.Fatalf("error message should name read_skill: %v", err)
	}
}

func TestReadFile_SymlinkInsideRootAllowed(t *testing.T) {
	root := makeSkillWithFiles(t, "sym", map[string]string{
		"real.md": "real content\n",
	})
	link := filepath.Join(root, "sym", "alias.md")
	if err := os.Symlink("real.md", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	s := NewStore(root, silentLogger())

	body, found, err := s.ReadFile("sym", "alias.md")
	if err != nil || !found {
		t.Fatalf("unexpected: found=%v err=%v", found, err)
	}
	if body != "real content\n" {
		t.Fatalf("body: %q", body)
	}
}

func TestReadFile_SymlinkEscapingRejected(t *testing.T) {
	root := makeSkillWithFiles(t, "esc", map[string]string{})
	// Target outside the skill root.
	outside := filepath.Join(root, "outside.md")
	if err := os.WriteFile(outside, []byte("leaked\n"), 0o644); err != nil {
		t.Fatalf("outside: %v", err)
	}
	link := filepath.Join(root, "esc", "outbound.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	s := NewStore(root, silentLogger())

	body, found, err := s.ReadFile("esc", "outbound.md")
	if !found {
		t.Fatal("expected found=true")
	}
	if !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("err = %v, want ErrPathEscapesRoot", err)
	}
	if strings.Contains(body, "leaked") {
		t.Fatal("escaped symlink leaked contents")
	}
}

func TestReadFile_DirectoryRejected(t *testing.T) {
	root := makeSkillWithFiles(t, "dir", map[string]string{
		"subdir/placeholder": "x\n",
	})
	s := NewStore(root, silentLogger())

	_, found, err := s.ReadFile("dir", "subdir")
	if !found {
		t.Fatal("expected found=true")
	}
	if !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("err = %v, want ErrNotRegularFile", err)
	}
}

func TestReadFile_OversizeRejectedWithoutReadingContents(t *testing.T) {
	root := makeSkillWithFiles(t, "big", map[string]string{})
	// Write a 1 KB file but set the cap to 100 bytes.
	bigPath := filepath.Join(root, "big", "huge.md")
	contents := bytes.Repeat([]byte{'A'}, 1024)
	if err := os.WriteFile(bigPath, contents, 0o644); err != nil {
		t.Fatalf("big: %v", err)
	}

	t.Setenv(fileMaxBytesEnv, "100")
	s := NewStore(root, silentLogger())

	body, found, err := s.ReadFile("big", "huge.md")
	if !found {
		t.Fatal("expected found=true")
	}
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want 'too large' error", err)
	}
	if len(body) != 0 {
		t.Fatal("oversize file should not return any contents")
	}
}

func TestReadFile_NonexistentFileWithinValidSkill(t *testing.T) {
	root := makeSkillWithFiles(t, "missing", map[string]string{"present.md": "ok\n"})
	s := NewStore(root, silentLogger())

	_, found, err := s.ReadFile("missing", "absent.md")
	if !found {
		t.Fatal("expected found=true for known skill with missing file")
	}
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("err = %v, want ErrFileNotFound", err)
	}
}

func TestReadFile_DefaultSizeCap(t *testing.T) {
	t.Setenv(fileMaxBytesEnv, "")
	root := t.TempDir()
	s := NewStore(root, silentLogger())
	if s.FileMaxBytes() != defaultFileMaxBytes {
		t.Fatalf("cap = %d, want %d", s.FileMaxBytes(), defaultFileMaxBytes)
	}
}

func TestReadFile_ConfiguredSizeCap(t *testing.T) {
	t.Setenv(fileMaxBytesEnv, "1048576")
	root := t.TempDir()
	s := NewStore(root, silentLogger())
	if s.FileMaxBytes() != 1048576 {
		t.Fatalf("cap = %d, want 1048576", s.FileMaxBytes())
	}
}

func TestReadFile_InvalidSizeCapFallsBack(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	t.Setenv(fileMaxBytesEnv, "not-a-number")
	root := t.TempDir()
	s := NewStore(root, logger)
	if s.FileMaxBytes() != defaultFileMaxBytes {
		t.Fatalf("cap = %d, want default %d", s.FileMaxBytes(), defaultFileMaxBytes)
	}
	if !strings.Contains(buf.String(), "skill_file_max_bytes_invalid") {
		t.Fatalf("expected WARN log, got: %s", buf.String())
	}
}

func TestReadFile_InvalidNegativeSizeCapFallsBack(t *testing.T) {
	t.Setenv(fileMaxBytesEnv, "-42")
	root := t.TempDir()
	s := NewStore(root, silentLogger())
	if s.FileMaxBytes() != defaultFileMaxBytes {
		t.Fatalf("cap = %d, want default %d", s.FileMaxBytes(), defaultFileMaxBytes)
	}
}
