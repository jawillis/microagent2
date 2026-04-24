package exec

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func capLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func TestConfig_Defaults(t *testing.T) {
	for _, k := range []string{
		"EXEC_PORT", "EXEC_MAX_TIMEOUT_S", "EXEC_STDOUT_CAP_BYTES", "EXEC_STDERR_CAP_BYTES",
		"EXEC_WORKSPACE_RETENTION_MINUTES", "EXEC_GC_INTERVAL_MINUTES", "EXEC_PREWARM_CONCURRENCY",
		"EXEC_INSTALL_TIMEOUT_S", "EXEC_SHUTDOWN_GRACE_S", "EXEC_NETWORK_DEFAULT",
		"EXEC_NETWORK_DENY_SKILLS", "SKILLS_DIR", "CACHE_DIR", "WORKSPACE_DIR", "SANDBOX_DIR",
		"EXEC_SANDBOX_RETENTION_MINUTES", "EXEC_SANDBOX_GC_INTERVAL_MINUTES",
		"PYTHON_VERSION", "UV_BIN",
	} {
		t.Setenv(k, "")
	}

	logger, _ := capLogger()
	c := Load(logger)

	if c.Port != 8085 {
		t.Errorf("Port = %d, want 8085", c.Port)
	}
	if c.MaxTimeout != 120*time.Second {
		t.Errorf("MaxTimeout = %v, want 120s", c.MaxTimeout)
	}
	if c.StdoutCapBytes != 16384 {
		t.Errorf("StdoutCapBytes = %d, want 16384", c.StdoutCapBytes)
	}
	if c.StderrCapBytes != 8192 {
		t.Errorf("StderrCapBytes = %d, want 8192", c.StderrCapBytes)
	}
	if c.WorkspaceRetention != 60*time.Minute {
		t.Errorf("WorkspaceRetention = %v, want 60m", c.WorkspaceRetention)
	}
	if c.GCInterval != 5*time.Minute {
		t.Errorf("GCInterval = %v, want 5m", c.GCInterval)
	}
	if c.PrewarmConcurrency != 4 {
		t.Errorf("PrewarmConcurrency = %d, want 4", c.PrewarmConcurrency)
	}
	if c.InstallTimeout != 600*time.Second {
		t.Errorf("InstallTimeout = %v, want 600s", c.InstallTimeout)
	}
	if c.ShutdownGrace != c.MaxTimeout+5*time.Second {
		t.Errorf("ShutdownGrace = %v, want MaxTimeout+5s", c.ShutdownGrace)
	}
	if c.NetworkDefault != NetAllow {
		t.Errorf("NetworkDefault = %q, want allow", c.NetworkDefault)
	}
	if len(c.NetworkDenySkills) != 0 {
		t.Errorf("NetworkDenySkills should be empty, got %v", c.NetworkDenySkills)
	}
	if c.SkillsDir != "/skills" {
		t.Errorf("SkillsDir = %q", c.SkillsDir)
	}
	if c.PythonVersion != "3.12" {
		t.Errorf("PythonVersion = %q", c.PythonVersion)
	}
	if c.UVBin != "uv" {
		t.Errorf("UVBin = %q", c.UVBin)
	}
	if c.SandboxDir != "/sandbox" {
		t.Errorf("SandboxDir = %q", c.SandboxDir)
	}
	if c.SandboxRetention != 60*time.Minute {
		t.Errorf("SandboxRetention = %v, want 60m", c.SandboxRetention)
	}
	if c.SandboxGCInterval != 5*time.Minute {
		t.Errorf("SandboxGCInterval = %v, want 5m", c.SandboxGCInterval)
	}
}

func TestConfig_SandboxEnvOverrides(t *testing.T) {
	t.Setenv("SANDBOX_DIR", "/tmp/sandbox")
	t.Setenv("EXEC_SANDBOX_RETENTION_MINUTES", "15")
	t.Setenv("EXEC_SANDBOX_GC_INTERVAL_MINUTES", "2")
	c := Load(slog.Default())
	if c.SandboxDir != "/tmp/sandbox" {
		t.Errorf("SandboxDir = %q", c.SandboxDir)
	}
	if c.SandboxRetention != 15*time.Minute {
		t.Errorf("SandboxRetention = %v", c.SandboxRetention)
	}
	if c.SandboxGCInterval != 2*time.Minute {
		t.Errorf("SandboxGCInterval = %v", c.SandboxGCInterval)
	}
}

func TestConfig_SandboxInvalidFallsBack(t *testing.T) {
	t.Setenv("EXEC_SANDBOX_RETENTION_MINUTES", "not-numeric")
	c := Load(slog.Default())
	if c.SandboxRetention != 60*time.Minute {
		t.Errorf("should fall back to 60m, got %v", c.SandboxRetention)
	}
}

func TestConfig_EnvOverrides(t *testing.T) {
	t.Setenv("EXEC_PORT", "9999")
	t.Setenv("EXEC_MAX_TIMEOUT_S", "30")
	t.Setenv("EXEC_STDOUT_CAP_BYTES", "4096")
	t.Setenv("EXEC_NETWORK_DEFAULT", "deny")
	t.Setenv("EXEC_NETWORK_DENY_SKILLS", "foo,bar, baz ")
	t.Setenv("SKILLS_DIR", "/tmp/skills")

	c := Load(slog.Default())
	if c.Port != 9999 {
		t.Errorf("Port = %d", c.Port)
	}
	if c.MaxTimeout != 30*time.Second {
		t.Errorf("MaxTimeout = %v", c.MaxTimeout)
	}
	if c.StdoutCapBytes != 4096 {
		t.Errorf("StdoutCapBytes = %d", c.StdoutCapBytes)
	}
	if c.NetworkDefault != NetDeny {
		t.Errorf("NetworkDefault = %q", c.NetworkDefault)
	}
	for _, skill := range []string{"foo", "bar", "baz"} {
		if _, ok := c.NetworkDenySkills[skill]; !ok {
			t.Errorf("deny set missing %q: %v", skill, c.NetworkDenySkills)
		}
	}
	if c.SkillsDir != "/tmp/skills" {
		t.Errorf("SkillsDir = %q", c.SkillsDir)
	}
}

func TestConfig_InvalidValuesWarnAndFallback(t *testing.T) {
	t.Setenv("EXEC_PORT", "not-a-number")
	t.Setenv("EXEC_STDOUT_CAP_BYTES", "-5")
	t.Setenv("EXEC_NETWORK_DEFAULT", "sometimes")

	logger, buf := capLogger()
	c := Load(logger)
	if c.Port != 8085 {
		t.Errorf("Port did not fall back: %d", c.Port)
	}
	if c.StdoutCapBytes != 16384 {
		t.Errorf("StdoutCapBytes did not fall back: %d", c.StdoutCapBytes)
	}
	if c.NetworkDefault != NetAllow {
		t.Errorf("NetworkDefault did not fall back: %q", c.NetworkDefault)
	}
	for _, key := range []string{"EXEC_PORT", "EXEC_STDOUT_CAP_BYTES", "EXEC_NETWORK_DEFAULT"} {
		if !strings.Contains(buf.String(), key) {
			t.Errorf("expected WARN log to mention %s; got:\n%s", key, buf.String())
		}
	}
}

func TestConfig_ShutdownGraceExplicitOverride(t *testing.T) {
	t.Setenv("EXEC_MAX_TIMEOUT_S", "60")
	t.Setenv("EXEC_SHUTDOWN_GRACE_S", "30")
	c := Load(slog.Default())
	if c.ShutdownGrace != 30*time.Second {
		t.Errorf("ShutdownGrace = %v, want 30s (explicit override)", c.ShutdownGrace)
	}
}

func TestConfig_DenySkillsEmptyEntriesIgnored(t *testing.T) {
	t.Setenv("EXEC_NETWORK_DENY_SKILLS", ",  ,foo,,")
	c := Load(slog.Default())
	if len(c.NetworkDenySkills) != 1 {
		t.Fatalf("expected 1 entry, got %v", c.NetworkDenySkills)
	}
	if _, ok := c.NetworkDenySkills["foo"]; !ok {
		t.Fatal("foo should be denied")
	}
}
