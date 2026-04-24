package exec

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFakeUV writes a shell script to dir that pretends to be `uv`. If
// fail is true, the script exits 1 after printing `scripted failure`.
// The venv directory is faked by creating bin/python at the path that would
// be passed to `uv venv`.
func writeFakeUV(t *testing.T, dir string, fail bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake uv requires bash")
	}
	script := `#!/bin/sh
# Fake uv used in tests.
if [ "$1" = "venv" ]; then
	# args: venv --python X /path/to/venv
	venv="$4"
	mkdir -p "$venv/bin"
	# Create a placeholder python that prints version.
	printf '#!/bin/sh\necho Python 3.12.0 fake\n' > "$venv/bin/python"
	chmod +x "$venv/bin/python"
	exit 0
fi
if [ "$1" = "pip" ]; then
	if [ -n "$UV_FAIL" ]; then
		echo "scripted failure" >&2
		exit 1
	fi
	exit 0
fi
echo "unknown command: $*" >&2
exit 2
`
	if fail {
		script = strings.Replace(script, `if [ -n "$UV_FAIL" ]; then`, `if true; then`, 1)
	}
	p := filepath.Join(dir, "uv")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func testConfig(t *testing.T, uvPath string) *Config {
	t.Helper()
	return &Config{
		UVBin:              uvPath,
		CacheDir:           t.TempDir(),
		PythonVersion:      "3.12",
		InstallTimeout:     30 * time.Second,
		PrewarmConcurrency: 2,
	}
}

func makeSkillWithReqs(t *testing.T, name string, requirements string) SkillInfo {
	t.Helper()
	root := t.TempDir()
	skillRoot := filepath.Join(root, name)
	scriptsDir := filepath.Join(skillRoot, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(skillRoot, "SKILL.md"),
		[]byte("---\nname: "+name+"\ndescription: d\n---\nbody\n"), 0o644)
	if requirements != "" {
		_ = os.WriteFile(filepath.Join(scriptsDir, "requirements.txt"), []byte(requirements), 0o644)
	}
	return SkillInfo{Name: name, Root: skillRoot, Prewarm: false}
}

func TestInstall_Success(t *testing.T) {
	uvDir := t.TempDir()
	uvPath := writeFakeUV(t, uvDir, false)

	cfg := testConfig(t, uvPath)
	health := NewHealth()
	logger := silentLogger()

	skill := makeSkillWithReqs(t, "demo", "tabulate==0.9.0\n")
	inst := NewInstaller(cfg, health, logger)

	res := inst.Install(context.Background(), skill, PhaseExplicit)
	if res.Status != "ok" {
		t.Fatalf("status = %q, error = %q", res.Status, res.Error)
	}
	venvPy := inst.VenvPython("demo")
	if venvPy == "" {
		t.Fatal("venv python not created")
	}
	snap := health.Snapshot()
	if len(snap.PrewarmedSkills) != 1 || snap.PrewarmedSkills[0] != "demo" {
		t.Fatalf("health missing demo: %+v", snap.PrewarmedSkills)
	}
}

func TestInstall_FailurePopulatesHealth(t *testing.T) {
	uvDir := t.TempDir()
	uvPath := writeFakeUV(t, uvDir, true)

	cfg := testConfig(t, uvPath)
	health := NewHealth()
	skill := makeSkillWithReqs(t, "broken", "broken==not-real\n")
	inst := NewInstaller(cfg, health, silentLogger())

	res := inst.Install(context.Background(), skill, PhaseExplicit)
	if res.Status != "error" {
		t.Fatalf("status = %q", res.Status)
	}
	if !strings.Contains(res.Error, "scripted failure") {
		t.Errorf("error should carry uv stderr: %q", res.Error)
	}
	snap := health.Snapshot()
	if len(snap.FailedInstalls) != 1 || snap.FailedInstalls[0].Skill != "broken" {
		t.Fatalf("health missing failure: %+v", snap.FailedInstalls)
	}
}

func TestInstall_MissingRequirementsIsNoop(t *testing.T) {
	uvPath := writeFakeUV(t, t.TempDir(), false)
	cfg := testConfig(t, uvPath)
	health := NewHealth()
	skill := makeSkillWithReqs(t, "no-reqs", "")

	inst := NewInstaller(cfg, health, silentLogger())
	res := inst.Install(context.Background(), skill, PhaseExplicit)
	if res.Status != "ok" {
		t.Fatalf("status = %q, err = %q", res.Status, res.Error)
	}
	// No venv should exist.
	if inst.VenvPython("no-reqs") != "" {
		t.Error("no-reqs should have no venv")
	}
	// Still considered prewarmed from health's POV.
	snap := health.Snapshot()
	if len(snap.PrewarmedSkills) != 1 {
		t.Errorf("prewarmed should include no-reqs: %+v", snap.PrewarmedSkills)
	}
}

func TestInstall_ConcurrentSerializesPerSkill(t *testing.T) {
	uvPath := writeFakeUV(t, t.TempDir(), false)
	cfg := testConfig(t, uvPath)
	health := NewHealth()
	skill := makeSkillWithReqs(t, "demo", "tabulate\n")
	inst := NewInstaller(cfg, health, silentLogger())

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := inst.Install(context.Background(), skill, PhaseExplicit)
			if res.Status != "ok" {
				t.Errorf("parallel install failed: %q", res.Error)
			}
		}()
	}
	wg.Wait()
	// Still just one venv.
	if inst.VenvPython("demo") == "" {
		t.Fatal("venv missing after parallel installs")
	}
}

func TestInstall_PrewarmOnlyRunsPrewarmSkills(t *testing.T) {
	uvPath := writeFakeUV(t, t.TempDir(), false)
	cfg := testConfig(t, uvPath)
	health := NewHealth()

	warm := makeSkillWithReqs(t, "warmed", "x\n")
	warm.Prewarm = true
	cold := makeSkillWithReqs(t, "cold", "y\n")

	inst := NewInstaller(cfg, health, silentLogger())
	inst.Prewarm(context.Background(), []SkillInfo{warm, cold})

	snap := health.Snapshot()
	found := map[string]bool{}
	for _, s := range snap.PrewarmedSkills {
		found[s] = true
	}
	if !found["warmed"] {
		t.Errorf("warmed should be prewarmed: %+v", snap.PrewarmedSkills)
	}
	if found["cold"] {
		t.Errorf("cold should NOT be prewarmed: %+v", snap.PrewarmedSkills)
	}
}

func TestInstall_EnvIsolatedFromHost(t *testing.T) {
	// Write a fake uv that prints its env. Fail the install so we capture
	// stderr containing the env.
	uvDir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "venv" ]; then
	venv="$4"
	mkdir -p "$venv/bin"
	echo '#!/bin/sh' > "$venv/bin/python"
	chmod +x "$venv/bin/python"
	exit 0
fi
if [ "$1" = "pip" ]; then
	env >&2
	exit 1
fi
exit 2
`
	uvPath := filepath.Join(uvDir, "uv")
	_ = os.WriteFile(uvPath, []byte(script), 0o755)

	t.Setenv("ANTHROPIC_AUTH_TOKEN", "should-not-leak-into-uv")

	cfg := testConfig(t, uvPath)
	health := NewHealth()
	skill := makeSkillWithReqs(t, "env-check", "x\n")
	inst := NewInstaller(cfg, health, silentLogger())

	res := inst.Install(context.Background(), skill, PhaseExplicit)
	if strings.Contains(res.Error, "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("host env leaked to uv: %q", res.Error)
	}
}
