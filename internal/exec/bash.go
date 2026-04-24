package exec

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// BashRunner spawns shell subprocesses for POST /v1/bash. Files live in
// /sandbox/<session>/ and persist across calls for the same session.
type BashRunner struct {
	cfg    *Config
	logger *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewBashRunner constructs a BashRunner bound to the given config.
func NewBashRunner(cfg *Config, logger *slog.Logger) *BashRunner {
	return &BashRunner{
		cfg:    cfg,
		logger: logger,
		locks:  map[string]*sync.Mutex{},
	}
}

// Run executes req.Command inside the session's persistent sandbox,
// returning the envelope synchronously. Per-session mutex serializes
// concurrent calls for the same session.
func (b *BashRunner) Run(ctx context.Context, req *BashRequest) (*BashResponse, error) {
	if req.Command == "" {
		return nil, errors.New("command is required")
	}
	if req.SessionID == "" {
		return nil, errors.New("session_id is required")
	}

	lock := b.lockFor(req.SessionID)
	lock.Lock()
	defer lock.Unlock()

	dir, err := SandboxFor(b.cfg.SandboxDir, req.SessionID)
	if err != nil {
		return nil, err
	}

	timeout := b.cfg.MaxTimeout
	if req.TimeoutS > 0 && time.Duration(req.TimeoutS)*time.Second < timeout {
		timeout = time.Duration(req.TimeoutS) * time.Second
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	b.logger.Info("exec_bash_started",
		"session_id", req.SessionID,
		"command_length", len(req.Command),
		"timeout_s", int(timeout/time.Second),
	)

	start := time.Now()
	exitCode, stdout, stdoutTrunc, stderr, stderrTrunc, timedOut := b.spawn(runCtx, req.Command, dir)
	duration := time.Since(start).Milliseconds()

	// Refresh the last-access marker post-run too, so a command that ran
	// for minutes doesn't leave the marker stale from before the spawn.
	_ = touchLastAccess(dir)

	b.logger.Info("exec_bash_finished",
		"session_id", req.SessionID,
		"exit_code", exitCode,
		"duration_ms", duration,
		"timed_out", timedOut,
		"stdout_bytes", len(stdout),
		"stderr_bytes", len(stderr),
	)

	return &BashResponse{
		ExitCode:        exitCode,
		Stdout:          stdout,
		StdoutTruncated: stdoutTrunc,
		Stderr:          stderr,
		StderrTruncated: stderrTrunc,
		SandboxDir:      dir,
		DurationMS:      duration,
		TimedOut:        timedOut,
	}, nil
}

func (b *BashRunner) lockFor(sessionID string) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	lock, ok := b.locks[sessionID]
	if !ok {
		lock = &sync.Mutex{}
		b.locks[sessionID] = lock
	}
	return lock
}

// spawn runs `sh -c "<command>"` in dir with bash-specific env isolation.
// Mirrors runner.spawn's deadline + process-group-kill pattern; see there
// for the rationale (CommandContext's direct-child SIGKILL leaves pipe FDs
// held by descendants).
func (b *BashRunner) spawn(ctx context.Context, command, dir string) (exitCode int, stdout string, stdoutTrunc bool, stderr string, stderrTrunc bool, timedOut bool) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = buildBashEnv(dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutFile, err := os.Create(filepath.Join(dir, ".stdout"))
	if err != nil {
		return -1, "", false, err.Error(), false, false
	}
	defer stdoutFile.Close()
	stderrFile, err := os.Create(filepath.Join(dir, ".stderr"))
	if err != nil {
		return -1, "", false, err.Error(), false, false
	}
	defer stderrFile.Close()

	outCap := newCappedBuffer(b.cfg.StdoutCapBytes)
	errCap := newCappedBuffer(b.cfg.StderrCapBytes)

	cmd.Stdout = io.MultiWriter(stdoutFile, outCap)
	cmd.Stderr = io.MultiWriter(stderrFile, errCap)

	if startErr := cmd.Start(); startErr != nil {
		return -1, "", false, startErr.Error(), false, false
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()

	_ = cmd.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		timedOut = true
	}

	switch {
	case timedOut:
		exitCode = -1
	case cmd.ProcessState != nil:
		exitCode = cmd.ProcessState.ExitCode()
	default:
		exitCode = 0
	}

	stdout, stdoutTrunc = outCap.String()
	stderr, stderrTrunc = errCap.String()
	return
}

// buildBashEnv constructs the subprocess environment from scratch. No
// WORKSPACE_DIR (that's reserved for /v1/run). No secrets from the host.
func buildBashEnv(sandboxDir string) []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + sandboxDir,
		"LANG=C.UTF-8",
		"PYTHONUNBUFFERED=1",
	}
}
