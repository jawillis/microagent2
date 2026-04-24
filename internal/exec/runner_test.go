package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// makeSkillWithScript creates a skill dir with SKILL.md and a script at
// scripts/<name>. Returns (skill, scriptRelPath).
func makeSkillWithScript(t *testing.T, name, scriptName, body string, mode os.FileMode) SkillInfo {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("runner tests assume POSIX")
	}
	root := t.TempDir()
	skillRoot := filepath.Join(root, name)
	scriptsDir := filepath.Join(skillRoot, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"),
		[]byte("---\nname: "+name+"\ndescription: d\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(scriptsDir, scriptName)
	if err := os.WriteFile(scriptPath, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return SkillInfo{Name: name, Root: skillRoot}
}

func testRunnerCfg(t *testing.T) *Config {
	return &Config{
		WorkspaceDir:      t.TempDir(),
		CacheDir:          t.TempDir(),
		MaxTimeout:        5 * time.Second,
		StdoutCapBytes:    32,
		StderrCapBytes:    32,
		NetworkDefault:    NetAllow,
		NetworkDenySkills: map[string]struct{}{},
	}
}

func TestResolveScriptPath_Valid(t *testing.T) {
	skill := makeSkillWithScript(t, "demo", "hello.sh", "#!/bin/sh\necho hi\n", 0o755)
	got, err := resolveScriptPath(skill.Root, "scripts/hello.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "scripts/hello.sh") {
		t.Fatalf("got %q", got)
	}
}

func TestResolveScriptPath_AbsoluteRejected(t *testing.T) {
	skill := makeSkillWithScript(t, "demo", "x.sh", "#!/bin/sh\n", 0o755)
	_, err := resolveScriptPath(skill.Root, "/etc/passwd")
	if !errors.Is(err, ErrScriptPathAbsolute) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveScriptPath_TraversalRejected(t *testing.T) {
	skill := makeSkillWithScript(t, "demo", "x.sh", "#!/bin/sh\n", 0o755)
	_, err := resolveScriptPath(skill.Root, "../../etc/passwd")
	if !errors.Is(err, ErrScriptPathEscape) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveScriptPath_MissingFile(t *testing.T) {
	skill := makeSkillWithScript(t, "demo", "x.sh", "#!/bin/sh\n", 0o755)
	_, err := resolveScriptPath(skill.Root, "scripts/does-not-exist.sh")
	if !errors.Is(err, ErrScriptMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveScriptPath_NonRegularFile(t *testing.T) {
	skill := makeSkillWithScript(t, "demo", "ok.sh", "#!/bin/sh\n", 0o755)
	// Add a directory at scripts/subdir.
	subdir := filepath.Join(skill.Root, "scripts", "subdir")
	_ = os.MkdirAll(subdir, 0o755)
	_, err := resolveScriptPath(skill.Root, "scripts/subdir")
	if !errors.Is(err, ErrScriptNotRegular) {
		t.Fatalf("err = %v", err)
	}
}

func runWith(t *testing.T, cfg *Config, skill SkillInfo, req *RunRequest) *RunResponse {
	t.Helper()
	installer := NewInstaller(cfg, NewHealth(), silentLogger())
	r := NewRunner(cfg, installer, silentLogger())
	res, err := r.Run(context.Background(), req, skill)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	return res
}

func TestRunner_SuccessfulShellScript(t *testing.T) {
	skill := makeSkillWithScript(t, "sh", "say.sh", "#!/bin/sh\necho hello\n", 0o755)
	cfg := testRunnerCfg(t)

	res := runWith(t, cfg, skill, &RunRequest{
		Skill:  "sh",
		Script: "scripts/say.sh",
	})
	if res.ExitCode != 0 {
		t.Errorf("exit = %d, stderr = %q", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "hello" {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if res.TimedOut {
		t.Error("should not have timed out")
	}
}

func TestRunner_NonZeroExitPreserved(t *testing.T) {
	skill := makeSkillWithScript(t, "fail", "boom.sh", "#!/bin/sh\nexit 7\n", 0o755)
	cfg := testRunnerCfg(t)
	res := runWith(t, cfg, skill, &RunRequest{Skill: "fail", Script: "scripts/boom.sh"})
	if res.ExitCode != 7 {
		t.Errorf("exit = %d", res.ExitCode)
	}
}

func TestRunner_StdoutCapTruncates(t *testing.T) {
	skill := makeSkillWithScript(t, "big", "dump.sh",
		"#!/bin/sh\nfor i in 1 2 3 4 5 6 7 8 9; do printf 'aaaaaaaaaa'; done\n",
		0o755)
	cfg := testRunnerCfg(t)
	cfg.StdoutCapBytes = 30
	res := runWith(t, cfg, skill, &RunRequest{Skill: "big", Script: "scripts/dump.sh"})
	if !res.StdoutTruncated {
		t.Error("stdout should be truncated")
	}
	if len(res.Stdout) > 30 {
		t.Errorf("stdout len %d exceeds cap 30", len(res.Stdout))
	}
	// Full content survives in workspace.
	full, err := os.ReadFile(filepath.Join(res.WorkspaceDir, ".stdout"))
	if err != nil {
		t.Fatal(err)
	}
	if len(full) < 80 {
		t.Errorf("full stdout file too small: %d", len(full))
	}
}

func TestRunner_StderrCaptured(t *testing.T) {
	skill := makeSkillWithScript(t, "err", "shout.sh", "#!/bin/sh\necho oops >&2\nexit 2\n", 0o755)
	cfg := testRunnerCfg(t)
	res := runWith(t, cfg, skill, &RunRequest{Skill: "err", Script: "scripts/shout.sh"})
	if res.ExitCode != 2 || !strings.Contains(res.Stderr, "oops") {
		t.Fatalf("res = %+v", res)
	}
}

func TestRunner_TimeoutFlagsAndExit(t *testing.T) {
	skill := makeSkillWithScript(t, "slow", "loop.sh", "#!/bin/sh\nsleep 10\n", 0o755)
	cfg := testRunnerCfg(t)
	cfg.MaxTimeout = 500 * time.Millisecond
	res := runWith(t, cfg, skill, &RunRequest{
		Skill:    "slow",
		Script:   "scripts/loop.sh",
		TimeoutS: 1,
	})
	if !res.TimedOut {
		t.Error("should have timed out")
	}
	if res.ExitCode != -1 {
		t.Errorf("exit = %d, want -1", res.ExitCode)
	}
}

func TestRunner_ArgsAndStdin(t *testing.T) {
	skill := makeSkillWithScript(t, "echo", "pipe.sh",
		"#!/bin/sh\necho \"arg0=$1\"\ncat - \n", 0o755)
	cfg := testRunnerCfg(t)
	cfg.StdoutCapBytes = 256
	res := runWith(t, cfg, skill, &RunRequest{
		Skill:  "echo",
		Script: "scripts/pipe.sh",
		Args:   []string{"hello"},
		Stdin:  "from-stdin\n",
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "arg0=hello") {
		t.Errorf("args not passed: stdout = %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "from-stdin") {
		t.Errorf("stdin not delivered: stdout = %q", res.Stdout)
	}
}

func TestRunner_WorkspaceEnvInjected(t *testing.T) {
	skill := makeSkillWithScript(t, "env", "ws.sh",
		"#!/bin/sh\necho WSDIR=$WORKSPACE_DIR\n", 0o755)
	cfg := testRunnerCfg(t)
	cfg.StdoutCapBytes = 4096
	res := runWith(t, cfg, skill, &RunRequest{Skill: "env", Script: "scripts/ws.sh"})
	if !strings.Contains(res.Stdout, "WSDIR="+res.WorkspaceDir) {
		t.Errorf("WORKSPACE_DIR not injected correctly: stdout %q, ws %q", res.Stdout, res.WorkspaceDir)
	}
}

func TestRunner_EnvIsolatedFromHost(t *testing.T) {
	skill := makeSkillWithScript(t, "iso", "leak.sh",
		"#!/bin/sh\nenv | grep ^ANTHROPIC || echo no-leak\n", 0o755)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "secret-value")

	cfg := testRunnerCfg(t)
	cfg.StdoutCapBytes = 256
	res := runWith(t, cfg, skill, &RunRequest{Skill: "iso", Script: "scripts/leak.sh"})
	if strings.Contains(res.Stdout, "secret-value") {
		t.Fatalf("host env leaked: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "no-leak") {
		t.Errorf("expected no-leak marker: %q", res.Stdout)
	}
}

func TestRunner_OutputFilesDetected(t *testing.T) {
	// Script writes a text output + a binary-ish output + a hidden file.
	skill := makeSkillWithScript(t, "out", "make.sh", `#!/bin/sh
printf 'hello\n' > "$WORKSPACE_DIR/note.txt"
printf '\x89PNGfake' > "$WORKSPACE_DIR/pic.png"
printf 'hidden' > "$WORKSPACE_DIR/.secret"
mkdir -p "$WORKSPACE_DIR/subdir"
printf 'nested' > "$WORKSPACE_DIR/subdir/child"
`, 0o755)
	cfg := testRunnerCfg(t)
	res := runWith(t, cfg, skill, &RunRequest{Skill: "out", Script: "scripts/make.sh"})
	if res.ExitCode != 0 {
		t.Fatal(res.Stderr)
	}

	names := map[string]OutputFile{}
	for _, o := range res.Outputs {
		names[filepath.Base(o.Path)] = o
	}
	if _, ok := names["note.txt"]; !ok {
		t.Errorf("note.txt missing: %+v", res.Outputs)
	}
	if _, ok := names["pic.png"]; !ok {
		t.Errorf("pic.png missing: %+v", res.Outputs)
	}
	if _, ok := names[".secret"]; ok {
		t.Errorf("hidden file should be excluded")
	}
	if _, ok := names["subdir"]; ok {
		t.Errorf("subdirs should be excluded")
	}
	if _, ok := names["child"]; ok {
		t.Errorf("nested files should be excluded")
	}
	if mime := names["note.txt"].MIME; mime != "text/plain" {
		t.Errorf("note.txt MIME = %q", mime)
	}
	if mime := names["pic.png"].MIME; mime != "image/png" {
		t.Errorf("pic.png MIME = %q", mime)
	}
}

func TestRunner_ProcessGroupKilledOnTimeout(t *testing.T) {
	// Parent script backgrounds a long-running child; timeout should reap both.
	skill := makeSkillWithScript(t, "pg", "parent.sh", `#!/bin/sh
sleep 30 &
echo child_pid=$! > "$WORKSPACE_DIR/child.pid"
wait
`, 0o755)
	cfg := testRunnerCfg(t)
	cfg.MaxTimeout = 400 * time.Millisecond
	res := runWith(t, cfg, skill, &RunRequest{Skill: "pg", Script: "scripts/parent.sh"})
	if !res.TimedOut {
		t.Fatal("expected timeout")
	}
	pidBytes, err := os.ReadFile(filepath.Join(res.WorkspaceDir, "child.pid"))
	if err != nil {
		t.Skipf("child.pid not written (test script raced): %v", err)
	}
	raw := strings.TrimSpace(string(pidBytes))
	pidStr := strings.TrimPrefix(raw, "child_pid=")
	if pidStr == "" || pidStr == raw {
		t.Skipf("malformed child.pid: %q", raw)
	}
	// Give the kernel a moment to reap.
	time.Sleep(200 * time.Millisecond)
	// Check the pid is no longer alive. On POSIX, `kill -0` tests process existence.
	if err := syscallKillZero(pidStr); err == nil {
		t.Errorf("orphan child process still alive: pid=%s", pidStr)
	}
}

func TestCappedBuffer_UTFBoundary(t *testing.T) {
	// A two-byte rune at the boundary should not end up half-included.
	b := newCappedBuffer(3)
	// "aé" = 0x61 0xC3 0xA9 = 3 bytes. At cap 3 we fit everything.
	b.Write([]byte{0x61, 0xC3, 0xA9})
	s, trunc := b.String()
	if s != "aé" || trunc {
		t.Fatalf("got (%q, %v)", s, trunc)
	}

	// Now cap = 2; last byte (0xA9) would break utf8. Expect trimmed to "a".
	b2 := newCappedBuffer(2)
	b2.Write([]byte{0x61, 0xC3, 0xA9})
	s2, trunc2 := b2.String()
	if s2 != "a" || !trunc2 {
		t.Fatalf("got (%q, %v)", s2, trunc2)
	}
}
