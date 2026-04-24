package exec

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// InstallPhase labels the provenance of an install call for logging.
type InstallPhase string

const (
	PhasePrewarm  InstallPhase = "prewarm"
	PhaseLazy     InstallPhase = "lazy"
	PhaseExplicit InstallPhase = "explicit"
)

// InstallResult reports the outcome of one install call.
type InstallResult struct {
	Status     string // "ok" | "error"
	DurationMS int64
	Error      string // populated when Status == "error"
}

// Installer runs uv to create per-skill venvs and install dependencies.
type Installer struct {
	cfg    *Config
	health *Health
	logger *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-skill install serialization
}

// NewInstaller constructs an Installer over the given config/health.
func NewInstaller(cfg *Config, health *Health, logger *slog.Logger) *Installer {
	return &Installer{
		cfg:    cfg,
		health: health,
		logger: logger,
		locks:  map[string]*sync.Mutex{},
	}
}

// Install ensures <cache>/<skill>/venv exists and has requirements installed.
// Serialized per skill. Missing requirements.txt is a no-op success.
func (i *Installer) Install(ctx context.Context, skill SkillInfo, phase InstallPhase) InstallResult {
	lock := i.lockFor(skill.Name)
	lock.Lock()
	defer lock.Unlock()

	start := time.Now()
	i.logger.Info("exec_install_started", "skill", skill.Name, "phase", string(phase))

	reqPath := filepath.Join(skill.Root, "scripts", "requirements.txt")
	if _, err := os.Stat(reqPath); os.IsNotExist(err) {
		// No dep file → nothing to install, treat as ok.
		i.health.Installed(skill.Name)
		res := InstallResult{Status: "ok", DurationMS: time.Since(start).Milliseconds()}
		i.logger.Info("exec_install_finished", "skill", skill.Name, "phase", string(phase), "status", res.Status, "duration_ms", res.DurationMS)
		return res
	}

	venvDir := filepath.Join(i.cfg.CacheDir, skill.Name, "venv")
	if err := os.MkdirAll(filepath.Dir(venvDir), 0o755); err != nil {
		return i.finishFailed(start, skill.Name, phase, fmt.Sprintf("mkdir cache: %s", err.Error()))
	}

	// Create venv if absent. "uv venv" is idempotent but creating only when
	// missing keeps the happy-path log quiet.
	if _, err := os.Stat(filepath.Join(venvDir, "bin", "python")); os.IsNotExist(err) {
		if out, err := i.runUV(ctx, "venv", "--python", i.cfg.PythonVersion, venvDir); err != nil {
			return i.finishFailed(start, skill.Name, phase, fmt.Sprintf("uv venv: %s: %s", err.Error(), out))
		}
	}

	// Install requirements.
	if out, err := i.runUV(ctx, "pip", "install", "-r", reqPath, "--python", filepath.Join(venvDir, "bin", "python")); err != nil {
		return i.finishFailed(start, skill.Name, phase, fmt.Sprintf("uv pip install: %s: %s", err.Error(), out))
	}

	i.health.Installed(skill.Name)
	res := InstallResult{Status: "ok", DurationMS: time.Since(start).Milliseconds()}
	i.logger.Info("exec_install_finished", "skill", skill.Name, "phase", string(phase), "status", res.Status, "duration_ms", res.DurationMS)
	return res
}

// VenvPython returns the absolute path to the cached interpreter for a skill,
// or empty string if no venv has been provisioned.
func (i *Installer) VenvPython(skill string) string {
	p := filepath.Join(i.cfg.CacheDir, skill, "venv", "bin", "python")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// VenvDir returns the venv root path (may not exist yet).
func (i *Installer) VenvDir(skill string) string {
	return filepath.Join(i.cfg.CacheDir, skill, "venv")
}

// Prewarm installs every skill with x-microagent.prewarm: true concurrently,
// capped by the configured concurrency. Returns after all attempts finish;
// failures are logged and recorded in health but do not abort the sweep.
func (i *Installer) Prewarm(ctx context.Context, skills []SkillInfo) {
	if i.cfg.PrewarmConcurrency < 1 {
		i.cfg.PrewarmConcurrency = 1
	}
	sem := make(chan struct{}, i.cfg.PrewarmConcurrency)
	var wg sync.WaitGroup
	for _, s := range skills {
		if !s.Prewarm {
			continue
		}
		s := s
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			installCtx, cancel := context.WithTimeout(ctx, i.cfg.InstallTimeout)
			defer cancel()
			_ = i.Install(installCtx, s, PhasePrewarm)
		}()
	}
	wg.Wait()
}

func (i *Installer) lockFor(skill string) *sync.Mutex {
	i.mu.Lock()
	defer i.mu.Unlock()
	lock, ok := i.locks[skill]
	if !ok {
		lock = &sync.Mutex{}
		i.locks[skill] = lock
	}
	return lock
}

func (i *Installer) runUV(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, i.cfg.UVBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Minimal env for uv: PATH only (uv resolves Python itself via --python).
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=/tmp",
	}
	err := cmd.Run()
	// Concatenate for error message readability; caller decides what to log.
	combined := stderr.String()
	if combined == "" {
		combined = stdout.String()
	}
	return combined, err
}

func (i *Installer) finishFailed(start time.Time, skill string, phase InstallPhase, msg string) InstallResult {
	dur := time.Since(start).Milliseconds()
	i.health.InstallFailed(skill, msg)
	i.logger.Error("exec_install_finished", "skill", skill, "phase", string(phase), "status", "error", "duration_ms", dur, "error", msg)
	return InstallResult{Status: "error", DurationMS: dur, Error: msg}
}
