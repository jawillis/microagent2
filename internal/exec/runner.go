package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Runner spawns subprocesses per /v1/run call.
type Runner struct {
	cfg       *Config
	installer *Installer
	logger    *slog.Logger
}

// NewRunner constructs a Runner over the given dependencies.
func NewRunner(cfg *Config, installer *Installer, logger *slog.Logger) *Runner {
	return &Runner{cfg: cfg, installer: installer, logger: logger}
}

// Run validates, spawns the subprocess, captures output, and builds the
// response envelope. Lazy install is performed inline if needed.
func (r *Runner) Run(ctx context.Context, req *RunRequest, skill SkillInfo) (*RunResponse, error) {
	scriptPath, err := resolveScriptPath(skill.Root, req.Script)
	if err != nil {
		return nil, err
	}

	// Lazy install: if the skill has requirements.txt and no venv yet,
	// install now and record how long it took.
	var installDuration int64
	reqPath := filepath.Join(skill.Root, "scripts", "requirements.txt")
	if _, err := os.Stat(reqPath); err == nil {
		if r.installer.VenvPython(skill.Name) == "" {
			res := r.installer.Install(ctx, skill, PhaseLazy)
			installDuration = res.DurationMS
			if res.Status != "ok" {
				return &RunResponse{
					ExitCode:          -1,
					Stderr:            res.Error,
					WorkspaceDir:      "",
					Outputs:           []OutputFile{},
					DurationMS:        0,
					TimedOut:          false,
					InstallDurationMS: installDuration,
				}, nil
			}
		}
	}

	ws, err := Allocate(r.cfg.WorkspaceDir, req.SessionID)
	if err != nil {
		return nil, err
	}

	timeout := r.cfg.MaxTimeout
	if req.TimeoutS > 0 && time.Duration(req.TimeoutS)*time.Second < timeout {
		timeout = time.Duration(req.TimeoutS) * time.Second
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	r.logger.Info("exec_run_started",
		"skill", skill.Name,
		"script", req.Script,
		"args", req.Args,
		"session_id", req.SessionID,
		"invocation_id", ws.InvocationID,
		"workspace_dir", ws.Dir,
	)

	start := time.Now()
	exitCode, stdout, stdoutTrunc, stderr, stderrTrunc, timedOut, spawnErr := r.spawn(runCtx, req, scriptPath, skill, ws)
	duration := time.Since(start).Milliseconds()

	// Record run outcome in metadata for GC.
	_ = ws.Finalize(WorkspaceMetadata{
		Skill:    skill.Name,
		Script:   req.Script,
		Args:     req.Args,
		EndedAt:  time.Now().UTC(),
		ExitCode: exitCode,
		TimedOut: timedOut,
	})

	outputs := detectOutputs(ws.Dir)

	r.logger.Info("exec_run_finished",
		"skill", skill.Name,
		"script", req.Script,
		"invocation_id", ws.InvocationID,
		"exit_code", exitCode,
		"duration_ms", duration,
		"timed_out", timedOut,
		"stdout_bytes", len(stdout),
		"stderr_bytes", len(stderr),
	)

	if spawnErr != nil && !timedOut {
		// Hard spawn failure (e.g., fork failed). Surface via envelope, not
		// as a 500 — the contract is that /v1/run returns 200 whenever a
		// subprocess attempt was made.
		stderr = appendWithCap(stderr, "\n"+spawnErr.Error(), r.cfg.StderrCapBytes)
		if exitCode == 0 {
			exitCode = -1
		}
	}

	return &RunResponse{
		ExitCode:          exitCode,
		Stdout:            stdout,
		StdoutTruncated:   stdoutTrunc,
		Stderr:            stderr,
		StderrTruncated:   stderrTrunc,
		WorkspaceDir:      ws.Dir,
		Outputs:           outputs,
		DurationMS:        duration,
		TimedOut:          timedOut,
		InstallDurationMS: installDuration,
	}, nil
}

func (r *Runner) spawn(ctx context.Context, req *RunRequest, scriptPath string, skill SkillInfo, ws *Workspace) (exitCode int, stdout string, stdoutTrunc bool, stderr string, stderrTrunc bool, timedOut bool, err error) {
	interpreter := pickInterpreter(scriptPath, r.installer.VenvPython(skill.Name))
	args := append([]string{scriptPath}, req.Args...)

	// Use Command (not CommandContext) so we can manage cancellation
	// ourselves — Go's CommandContext only SIGKILLs the direct child, which
	// leaves descendants holding pipe FDs open and makes cmd.Wait hang when
	// Setpgid is used. We explicitly kill -pgid on ctx.Done().
	cmd := exec.Command(interpreter, args...)
	cmd.Dir = skill.Root
	cmd.Env = buildSubprocessEnv(skill.Name, r.installer.VenvDir(skill.Name), ws.Dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}

	// Capped in-memory buffers + full-fidelity files under the workspace.
	stdoutFile, err := os.Create(filepath.Join(ws.Dir, ".stdout"))
	if err != nil {
		return -1, "", false, err.Error(), false, false, err
	}
	defer stdoutFile.Close()
	stderrFile, err := os.Create(filepath.Join(ws.Dir, ".stderr"))
	if err != nil {
		return -1, "", false, err.Error(), false, false, err
	}
	defer stderrFile.Close()

	outCap := newCappedBuffer(r.cfg.StdoutCapBytes)
	errCap := newCappedBuffer(r.cfg.StderrCapBytes)

	cmd.Stdout = io.MultiWriter(stdoutFile, outCap)
	cmd.Stderr = io.MultiWriter(stderrFile, errCap)

	if startErr := cmd.Start(); startErr != nil {
		return -1, "", false, startErr.Error(), false, false, startErr
	}

	// Watch for context cancel; on trip, SIGKILL the process group so that
	// descendants release pipe FDs and cmd.Wait returns promptly.
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

	runErr := cmd.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		timedOut = true
	}

	switch {
	case timedOut:
		exitCode = -1
	case cmd.ProcessState != nil:
		exitCode = cmd.ProcessState.ExitCode()
	case runErr != nil:
		exitCode = -1
		err = runErr
	default:
		exitCode = 0
	}

	stdout, stdoutTrunc = outCap.String()
	stderr, stderrTrunc = errCap.String()
	return
}

// resolveScriptPath validates req.Script and returns an absolute path under
// skillRoot. Rejects absolute paths, traversal, non-regular files.
func resolveScriptPath(skillRoot, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("%w: script is required", ErrInvalidRequest)
	}
	if filepath.IsAbs(rel) {
		return "", ErrScriptPathAbsolute
	}
	clean := filepath.Clean(rel)
	for _, seg := range strings.Split(clean, string(filepath.Separator)) {
		if seg == ".." {
			return "", ErrScriptPathEscape
		}
	}
	abs := filepath.Join(skillRoot, clean)

	rootResolved, err := filepath.EvalSymlinks(skillRoot)
	if err != nil {
		return "", fmt.Errorf("resolve skill root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrScriptMissing
		}
		return "", fmt.Errorf("resolve script: %w", err)
	}
	rootSep := rootResolved + string(filepath.Separator)
	if resolved != rootResolved && !strings.HasPrefix(resolved+string(filepath.Separator), rootSep) {
		return "", ErrScriptPathEscape
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", ErrScriptNotRegular
	}
	return resolved, nil
}

// pickInterpreter returns the program to exec. Python scripts get the skill's
// venv python when available; shell scripts go through `sh`. Anything else
// is invoked directly (relying on its shebang if executable).
func pickInterpreter(scriptPath, venvPython string) string {
	ext := strings.ToLower(filepath.Ext(scriptPath))
	switch ext {
	case ".py":
		if venvPython != "" {
			return venvPython
		}
		return "python3"
	case ".sh":
		return "sh"
	}
	return scriptPath
}

// buildSubprocessEnv returns a minimal env constructed from scratch per spec.
// Nothing from os.Environ() is propagated.
func buildSubprocessEnv(skillName, venvDir, workspace string) []string {
	path := "/usr/local/bin:/usr/bin:/bin"
	if venvDir != "" {
		if _, err := os.Stat(filepath.Join(venvDir, "bin")); err == nil {
			path = filepath.Join(venvDir, "bin") + ":" + path
		}
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + workspace,
		"WORKSPACE_DIR=" + workspace,
		"LANG=C.UTF-8",
		"PYTHONUNBUFFERED=1",
	}
	if venvDir != "" {
		if _, err := os.Stat(venvDir); err == nil {
			env = append(env, "VIRTUAL_ENV="+venvDir)
		}
	}
	return env
}

// cappedBuffer is an io.Writer that keeps at most capBytes of data. Writes
// beyond the cap silently drop; the rune-aware String() trims the tail to
// a valid UTF-8 boundary and reports whether any data was dropped.
type cappedBuffer struct {
	cap       int
	buf       bytes.Buffer
	truncated bool
}

func newCappedBuffer(cap int) *cappedBuffer {
	if cap < 0 {
		cap = 0
	}
	return &cappedBuffer{cap: cap}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len() >= c.cap {
		c.truncated = true
		return len(p), nil
	}
	room := c.cap - c.buf.Len()
	if len(p) <= room {
		return c.buf.Write(p)
	}
	c.buf.Write(p[:room])
	c.truncated = true
	return len(p), nil
}

func (c *cappedBuffer) String() (string, bool) {
	data := c.buf.Bytes()
	if !utf8.Valid(data) {
		// Trim the tail until we hit a valid rune boundary.
		for i := len(data); i > 0; i-- {
			if utf8.Valid(data[:i]) {
				data = data[:i]
				break
			}
		}
	}
	return string(data), c.truncated
}

// appendWithCap appends s to dst, trimming to keep within cap. Used for
// spawn-error messages attached to stderr.
func appendWithCap(dst, s string, cap int) string {
	combined := dst + s
	if cap <= 0 || len(combined) <= cap {
		return combined
	}
	return combined[:cap]
}

// detectOutputs enumerates regular, non-hidden files at the top level of
// workspace and returns them sorted by name with MIME + size.
func detectOutputs(workspace string) []OutputFile {
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return nil
	}
	out := make([]OutputFile, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !e.Type().IsRegular() {
			continue
		}
		full := filepath.Join(workspace, name)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		out = append(out, OutputFile{
			Path:  full,
			MIME:  detectMIME(full, info.Size()),
			Bytes: info.Size(),
		})
	}
	return out
}

func detectMIME(path string, size int64) string {
	ext := strings.ToLower(filepath.Ext(path))
	if m := mimeByExt(ext); m != "" {
		return m
	}
	// Fallback: sniff the first 512 bytes.
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return http.DetectContentType(buf[:n])
}

func mimeByExt(ext string) string {
	switch ext {
	case ".txt":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".html":
		return "text/html"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".csv":
		return "text/csv"
	}
	return ""
}

// errNotExecutable keeps error messages stable across OS reporting variance.
var errNotExecutable = errors.New("script is not executable")
