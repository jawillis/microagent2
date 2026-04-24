package exec

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanSkills_HappyPath(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", "---\nname: alpha\ndescription: a\n---\nbody\n")
	writeSkill(t, root, "beta", "---\nname: beta\ndescription: b\nx-microagent:\n  prewarm: true\n---\nbody\n")

	got := ScanSkills(root, silentLogger())
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Name != "alpha" || got[0].Prewarm {
		t.Errorf("alpha: %+v", got[0])
	}
	if got[1].Name != "beta" || !got[1].Prewarm {
		t.Errorf("beta: %+v", got[1])
	}
}

func TestScanSkills_MissingDirIsEmpty(t *testing.T) {
	got := ScanSkills("/does/not/exist", silentLogger())
	if len(got) != 0 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestScanSkills_MalformedFrontmatterLoggedAndSkipped(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "ok", "---\nname: ok\ndescription: d\n---\nbody\n")
	writeSkill(t, root, "bad", "no frontmatter at all\n")

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	got := ScanSkills(root, logger)
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("expected only ok; got %+v", got)
	}
	if !strings.Contains(buf.String(), "exec_skill_frontmatter_invalid") {
		t.Errorf("expected WARN log; got: %s", buf.String())
	}
}

func TestScanSkills_EmptyNameSkipped(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "noname", "---\ndescription: no name\n---\nbody\n")
	got := ScanSkills(root, silentLogger())
	if len(got) != 0 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestScanSkills_NestedDirsIgnored(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "outer", "---\nname: outer\ndescription: d\n---\nbody\n")
	_ = os.MkdirAll(filepath.Join(root, "outer", "inner"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "outer", "inner", "SKILL.md"), []byte("---\nname: inner\ndescription: d\n---\nbody\n"), 0o644)

	got := ScanSkills(root, silentLogger())
	if len(got) != 1 || got[0].Name != "outer" {
		t.Fatalf("expected only outer; got %+v", got)
	}
}

func TestScanSkills_Alphabetical(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"zulu", "alpha", "mike"} {
		writeSkill(t, root, n, "---\nname: "+n+"\ndescription: d\n---\nbody\n")
	}
	got := ScanSkills(root, silentLogger())
	for i, want := range []string{"alpha", "mike", "zulu"} {
		if got[i].Name != want {
			t.Errorf("got[%d] = %q, want %q", i, got[i].Name, want)
		}
	}
}

func TestScanSkills_PrewarmParsedFromNamespacedKey(t *testing.T) {
	root := t.TempDir()
	// Bare 'prewarm: true' should NOT be recognized — must be under x-microagent.
	writeSkill(t, root, "bare", "---\nname: bare\ndescription: d\nprewarm: true\n---\nbody\n")
	writeSkill(t, root, "nested", "---\nname: nested\ndescription: d\nx-microagent:\n  prewarm: true\n---\nbody\n")

	got := ScanSkills(root, silentLogger())
	for _, s := range got {
		if s.Name == "bare" && s.Prewarm {
			t.Errorf("bare should not be prewarm (not namespaced)")
		}
		if s.Name == "nested" && !s.Prewarm {
			t.Errorf("nested should be prewarm")
		}
	}
}

func TestLookupSkill(t *testing.T) {
	skills := []SkillInfo{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	if s, ok := LookupSkill(skills, "b"); !ok || s.Name != "b" {
		t.Fatalf("hit failed")
	}
	if _, ok := LookupSkill(skills, "missing"); ok {
		t.Fatalf("miss should return false")
	}
}
