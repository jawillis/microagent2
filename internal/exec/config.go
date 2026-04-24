// Package exec implements the microagent2 exec service: a sandboxed HTTP
// code-execution runtime whose first consumer is the skills-script-execution
// change, and whose contract is defined in openspec/specs/code-execution.
package exec

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all env-driven runtime settings. Defaults are applied by Load
// when a variable is unset or invalid; invalid values emit a WARN log and
// fall back to the default rather than failing startup.
type Config struct {
	Port                     int
	MaxTimeout               time.Duration
	StdoutCapBytes           int
	StderrCapBytes           int
	WorkspaceRetention       time.Duration
	GCInterval               time.Duration
	PrewarmConcurrency       int
	InstallTimeout           time.Duration
	ShutdownGrace            time.Duration
	NetworkDefault           NetMode
	NetworkDenySkills        map[string]struct{}
	SkillsDir                string
	CacheDir                 string
	WorkspaceDir             string
	PythonVersion            string
	UVBin                    string
}

// NetMode is the binary network-policy decision for v1.
type NetMode string

const (
	NetAllow NetMode = "allow"
	NetDeny  NetMode = "deny"
)

// Load builds a Config from os.Getenv, applying defaults and warning the
// logger on malformed inputs.
func Load(logger *slog.Logger) *Config {
	c := &Config{
		Port:               envInt(logger, "EXEC_PORT", 8085),
		MaxTimeout:         time.Duration(envInt(logger, "EXEC_MAX_TIMEOUT_S", 120)) * time.Second,
		StdoutCapBytes:     envInt(logger, "EXEC_STDOUT_CAP_BYTES", 16384),
		StderrCapBytes:     envInt(logger, "EXEC_STDERR_CAP_BYTES", 8192),
		WorkspaceRetention: time.Duration(envInt(logger, "EXEC_WORKSPACE_RETENTION_MINUTES", 60)) * time.Minute,
		GCInterval:         time.Duration(envInt(logger, "EXEC_GC_INTERVAL_MINUTES", 5)) * time.Minute,
		PrewarmConcurrency: envInt(logger, "EXEC_PREWARM_CONCURRENCY", 4),
		InstallTimeout:     time.Duration(envInt(logger, "EXEC_INSTALL_TIMEOUT_S", 600)) * time.Second,
		NetworkDefault:     envNetMode(logger, "EXEC_NETWORK_DEFAULT", NetAllow),
		NetworkDenySkills:  envSkillSet(logger, "EXEC_NETWORK_DENY_SKILLS"),
		SkillsDir:          envOr("SKILLS_DIR", "/skills"),
		CacheDir:           envOr("CACHE_DIR", "/cache"),
		WorkspaceDir:       envOr("WORKSPACE_DIR", "/workspace"),
		PythonVersion:      envOr("PYTHON_VERSION", "3.12"),
		UVBin:              envOr("UV_BIN", "uv"),
	}
	// Shutdown grace defaults to MaxTimeout + 5s unless explicitly set.
	if raw := os.Getenv("EXEC_SHUTDOWN_GRACE_S"); raw != "" {
		c.ShutdownGrace = time.Duration(envInt(logger, "EXEC_SHUTDOWN_GRACE_S", int(c.MaxTimeout/time.Second)+5)) * time.Second
	} else {
		c.ShutdownGrace = c.MaxTimeout + 5*time.Second
	}
	return c
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(logger *slog.Logger, key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		if logger != nil {
			logger.Warn("exec_config_invalid", "key", key, "value", raw, "fallback", fallback)
		}
		return fallback
	}
	return n
}

func envNetMode(logger *slog.Logger, key string, fallback NetMode) NetMode {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch NetMode(raw) {
	case NetAllow, NetDeny:
		return NetMode(raw)
	}
	if logger != nil {
		logger.Warn("exec_config_invalid", "key", key, "value", raw, "fallback", string(fallback))
	}
	return fallback
}

func envSkillSet(logger *slog.Logger, key string) map[string]struct{} {
	raw := os.Getenv(key)
	out := map[string]struct{}{}
	if raw == "" {
		return out
	}
	for _, entry := range strings.Split(raw, ",") {
		name := strings.TrimSpace(entry)
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

// String hides secrets and gives operators a one-line view on startup.
func (c *Config) String() string {
	return fmt.Sprintf(
		"port=%d max_timeout=%s stdout_cap=%d stderr_cap=%d retention=%s gc=%s prewarm=%d install_timeout=%s shutdown_grace=%s net_default=%s net_deny=%d skills_dir=%s cache_dir=%s workspace_dir=%s python=%s",
		c.Port, c.MaxTimeout, c.StdoutCapBytes, c.StderrCapBytes,
		c.WorkspaceRetention, c.GCInterval, c.PrewarmConcurrency,
		c.InstallTimeout, c.ShutdownGrace, c.NetworkDefault,
		len(c.NetworkDenySkills), c.SkillsDir, c.CacheDir,
		c.WorkspaceDir, c.PythonVersion,
	)
}
