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

func testBashCfg(t *testing.T) *Config {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash runner assumes POSIX")
	}
	return &Config{
		SandboxDir:     t.TempDir(),
		MaxTimeout:     5 * time.Second,
		StdoutCapBytes: 256,
		StderrCapBytes: 256,
	}
}

func runBash(t *testing.T, cfg *Config, req *BashRequest) *BashResponse {
	t.Helper()
	r := NewBashRunner(cfg, silentLogger())
	resp, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return resp
}

func TestBashRunner_Echo(t *testing.T) {
	cfg := testBashCfg(t)
	resp := runBash(t, cfg, &BashRequest{Command: "echo hi", SessionID: "s1"})
	if resp.ExitCode != 0 {
		t.Errorf("exit = %d, stderr = %q", resp.ExitCode, resp.Stderr)
	}
	if strings.TrimSpace(resp.Stdout) != "hi" {
		t.Errorf("stdout = %q", resp.Stdout)
	}
	if resp.SandboxDir != filepath.Join(cfg.SandboxDir, "s1") {
		t.Errorf("sandbox_dir = %q", resp.SandboxDir)
	}
}

func TestBashRunner_NonZeroExit(t *testing.T) {
	cfg := testBashCfg(t)
	resp := runBash(t, cfg, &BashRequest{Command: "exit 7", SessionID: "s"})
	if resp.ExitCode != 7 || resp.TimedOut {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestBashRunner_ShellMetaChars(t *testing.T) {
	cfg := testBashCfg(t)
	resp := runBash(t, cfg, &BashRequest{Command: "echo one && echo two", SessionID: "s"})
	if resp.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr = %q", resp.ExitCode, resp.Stderr)
	}
	if !strings.Contains(resp.Stdout, "one") || !strings.Contains(resp.Stdout, "two") {
		t.Fatalf("stdout = %q", resp.Stdout)
	}
}

func TestBashRunner_StderrCaptured(t *testing.T) {
	cfg := testBashCfg(t)
	resp := runBash(t, cfg, &BashRequest{Command: "echo boom >&2; exit 2", SessionID: "s"})
	if resp.ExitCode != 2 || !strings.Contains(resp.Stderr, "boom") {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestBashRunner_TimeoutFlagsAndExit(t *testing.T) {
	cfg := testBashCfg(t)
	cfg.MaxTimeout = 400 * time.Millisecond
	resp := runBash(t, cfg, &BashRequest{Command: "sleep 10", SessionID: "s"})
	if !resp.TimedOut || resp.ExitCode != -1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestBashRunner_StdoutCapTruncates(t *testing.T) {
	cfg := testBashCfg(t)
	cfg.StdoutCapBytes = 30
	resp := runBash(t, cfg, &BashRequest{
		Command:   `for i in 1 2 3 4 5 6 7 8 9; do printf 'aaaaaaaaaa'; done`,
		SessionID: "s",
	})
	if !resp.StdoutTruncated || len(resp.Stdout) > 30 {
		t.Fatalf("truncated = %v, len = %d", resp.StdoutTruncated, len(resp.Stdout))
	}
	full, err := os.ReadFile(filepath.Join(resp.SandboxDir, ".stdout"))
	if err != nil {
		t.Fatal(err)
	}
	if len(full) < 80 {
		t.Fatalf("full stdout too short: %d", len(full))
	}
}

func TestBashRunner_FilesPersistAcrossCalls(t *testing.T) {
	cfg := testBashCfg(t)
	r := NewBashRunner(cfg, silentLogger())

	r1, err := r.Run(context.Background(), &BashRequest{Command: "touch persist.txt", SessionID: "same"})
	if err != nil || r1.ExitCode != 0 {
		t.Fatalf("first call failed: %v / %+v", err, r1)
	}
	r2, err := r.Run(context.Background(), &BashRequest{Command: "ls", SessionID: "same"})
	if err != nil || r2.ExitCode != 0 {
		t.Fatalf("second call failed: %v / %+v", err, r2)
	}
	if !strings.Contains(r2.Stdout, "persist.txt") {
		t.Fatalf("persistence broken; stdout = %q", r2.Stdout)
	}
}

func TestBashRunner_DifferentSessionsIsolated(t *testing.T) {
	cfg := testBashCfg(t)
	r := NewBashRunner(cfg, silentLogger())

	_, _ = r.Run(context.Background(), &BashRequest{Command: "touch in-a.txt", SessionID: "sa"})
	resp, _ := r.Run(context.Background(), &BashRequest{Command: "ls", SessionID: "sb"})
	if strings.Contains(resp.Stdout, "in-a.txt") {
		t.Fatalf("cross-session leak: %q", resp.Stdout)
	}
}

func TestBashRunner_NoInheritedSecrets(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "SHOULD-NOT-LEAK")
	cfg := testBashCfg(t)
	resp := runBash(t, cfg, &BashRequest{Command: "env", SessionID: "s"})
	if strings.Contains(resp.Stdout, "SHOULD-NOT-LEAK") {
		t.Fatalf("host env leaked: %q", resp.Stdout)
	}
	if strings.Contains(resp.Stdout, "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("env var name leaked: %q", resp.Stdout)
	}
}

func TestBashRunner_HomeIsSandboxDir(t *testing.T) {
	cfg := testBashCfg(t)
	resp := runBash(t, cfg, &BashRequest{Command: "echo $HOME", SessionID: "s"})
	if strings.TrimSpace(resp.Stdout) != resp.SandboxDir {
		t.Fatalf("HOME = %q, want %q", resp.Stdout, resp.SandboxDir)
	}
}

func TestBashRunner_NoWorkspaceDirEnv(t *testing.T) {
	cfg := testBashCfg(t)
	resp := runBash(t, cfg, &BashRequest{Command: "echo WS=${WORKSPACE_DIR:-unset}", SessionID: "s"})
	if !strings.Contains(resp.Stdout, "WS=unset") {
		t.Fatalf("WORKSPACE_DIR leaked: %q", resp.Stdout)
	}
}

func TestBashRunner_PerSessionMutexSerializes(t *testing.T) {
	cfg := testBashCfg(t)
	r := NewBashRunner(cfg, silentLogger())

	// Two commands for the same session: second should not run before the
	// first finishes. We prove this by having the first write, sleep, then
	// the second read. If they raced, the second might read "before" state.
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan string, 2)
	go func() {
		defer wg.Done()
		_, _ = r.Run(context.Background(), &BashRequest{
			Command:   "echo first > marker.txt && sleep 0.3 && echo done >> marker.txt",
			SessionID: "race",
		})
	}()
	time.Sleep(50 * time.Millisecond)
	go func() {
		defer wg.Done()
		resp, _ := r.Run(context.Background(), &BashRequest{
			Command:   "cat marker.txt",
			SessionID: "race",
		})
		results <- resp.Stdout
	}()
	wg.Wait()
	close(results)
	out := <-results
	if !strings.Contains(out, "first") || !strings.Contains(out, "done") {
		t.Fatalf("mutex failed; second saw %q", out)
	}
}

func TestBashRunner_MissingCommandError(t *testing.T) {
	cfg := testBashCfg(t)
	r := NewBashRunner(cfg, silentLogger())
	if _, err := r.Run(context.Background(), &BashRequest{SessionID: "s"}); err == nil {
		t.Fatal("missing command should error")
	}
}

func TestBashRunner_MissingSessionIDError(t *testing.T) {
	cfg := testBashCfg(t)
	r := NewBashRunner(cfg, silentLogger())
	if _, err := r.Run(context.Background(), &BashRequest{Command: "echo hi"}); err == nil {
		t.Fatal("missing session_id should error")
	}
}

func TestBashRunner_ProcessGroupKilledOnTimeout(t *testing.T) {
	cfg := testBashCfg(t)
	cfg.MaxTimeout = 400 * time.Millisecond
	r := NewBashRunner(cfg, silentLogger())

	resp, _ := r.Run(context.Background(), &BashRequest{
		Command:   `sleep 30 & echo child_pid=$! > child.pid; wait`,
		SessionID: "pg",
	})
	if !resp.TimedOut {
		t.Fatal("expected timeout")
	}
	pidBytes, err := os.ReadFile(filepath.Join(resp.SandboxDir, "child.pid"))
	if err != nil {
		t.Skipf("child.pid not written: %v", err)
	}
	raw := strings.TrimSpace(string(pidBytes))
	pidStr := strings.TrimPrefix(raw, "child_pid=")
	if pidStr == "" || pidStr == raw {
		t.Skipf("malformed child.pid: %q", raw)
	}
	time.Sleep(200 * time.Millisecond)
	if err := syscallKillZero(pidStr); err == nil {
		t.Errorf("orphan process survived: pid=%s", pidStr)
	}
}
