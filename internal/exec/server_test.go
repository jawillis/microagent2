package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func setupServer(t *testing.T, cfg *Config, skills []SkillInfo) *httptest.Server {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("exec server tests assume POSIX")
	}
	logger := silentLogger()
	health := NewHealth()
	inst := NewInstaller(cfg, health, logger)
	runner := NewRunner(cfg, inst, logger)
	s := NewServer(cfg, runner, inst, health, logger, skills)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func doPost(t *testing.T, ts *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func doGet(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readJSON(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("json: %v; body: %s", err, b)
	}
}

func TestServer_UnknownPathReturns404(t *testing.T) {
	ts := setupServer(t, testRunnerCfg(t), nil)
	resp := doGet(t, ts, "/bogus")
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]string
	readJSON(t, resp, &body)
	if body["error"] != "not found" {
		t.Fatalf("body: %+v", body)
	}
}

func TestServer_WrongMethodReturns405(t *testing.T) {
	ts := setupServer(t, testRunnerCfg(t), nil)
	resp := doGet(t, ts, "/v1/run")
	if resp.StatusCode != 405 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	resp = doPost(t, ts, "/v1/health", nil)
	if resp.StatusCode != 405 {
		t.Fatalf("health POST status = %d", resp.StatusCode)
	}
}

func TestServer_HealthReturnsStarting(t *testing.T) {
	ts := setupServer(t, testRunnerCfg(t), nil)
	resp := doGet(t, ts, "/v1/health")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body HealthResponse
	readJSON(t, resp, &body)
	if body.Status != "starting" || body.Ready {
		t.Fatalf("body: %+v", body)
	}
	if body.PrewarmedSkills == nil {
		t.Errorf("prewarmed should be empty array, not null")
	}
	if body.FailedInstalls == nil {
		t.Errorf("failed should be empty array, not null")
	}
}

func TestServer_RunUnknownSkillReturns400(t *testing.T) {
	ts := setupServer(t, testRunnerCfg(t), nil)
	resp := doPost(t, ts, "/v1/run", RunRequest{Skill: "none", Script: "x.sh"})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestServer_RunMissingFieldsReturns400(t *testing.T) {
	ts := setupServer(t, testRunnerCfg(t), nil)
	resp := doPost(t, ts, "/v1/run", RunRequest{Skill: "x"})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestServer_RunSuccess(t *testing.T) {
	skill := makeSkillWithScript(t, "demo", "hi.sh", "#!/bin/sh\necho ok\n", 0o755)
	cfg := testRunnerCfg(t)
	ts := setupServer(t, cfg, []SkillInfo{skill})

	resp := doPost(t, ts, "/v1/run", RunRequest{Skill: "demo", Script: "scripts/hi.sh"})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body RunResponse
	readJSON(t, resp, &body)
	if body.ExitCode != 0 || strings.TrimSpace(body.Stdout) != "ok" {
		t.Fatalf("body = %+v", body)
	}
}

func TestServer_RunScriptTraversalReturns400(t *testing.T) {
	skill := makeSkillWithScript(t, "ok", "hi.sh", "#!/bin/sh\necho hi\n", 0o755)
	ts := setupServer(t, testRunnerCfg(t), []SkillInfo{skill})
	resp := doPost(t, ts, "/v1/run", RunRequest{Skill: "ok", Script: "../../../etc/passwd"})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestServer_RunNetworkDeniedReturns400(t *testing.T) {
	skill := makeSkillWithScript(t, "denied", "x.sh", "#!/bin/sh\necho hi\n", 0o755)
	cfg := testRunnerCfg(t)
	cfg.NetworkDefault = NetAllow
	cfg.NetworkDenySkills = map[string]struct{}{"denied": {}}
	ts := setupServer(t, cfg, []SkillInfo{skill})

	resp := doPost(t, ts, "/v1/run", RunRequest{Skill: "denied", Script: "scripts/x.sh"})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]string
	readJSON(t, resp, &body)
	if !strings.Contains(body["error"], "denies") {
		t.Fatalf("error: %q", body["error"])
	}
}

func TestServer_InstallOK(t *testing.T) {
	uvPath := writeFakeUV(t, t.TempDir(), false)
	cfg := &Config{
		UVBin:          uvPath,
		CacheDir:       t.TempDir(),
		WorkspaceDir:   t.TempDir(),
		MaxTimeout:     5 * time.Second,
		StdoutCapBytes: 256,
		StderrCapBytes: 128,
		InstallTimeout: 10 * time.Second,
		PythonVersion:  "3.12",
	}
	skill := makeSkillWithScript(t, "inst", "x.sh", "#!/bin/sh\necho x\n", 0o755)
	// Add requirements.txt so install is non-trivial.
	_ = os.MkdirAll(filepath.Join(skill.Root, "scripts"), 0o755)
	_ = os.WriteFile(filepath.Join(skill.Root, "scripts", "requirements.txt"), []byte("x==1\n"), 0o644)

	ts := setupServer(t, cfg, []SkillInfo{skill})
	resp := doPost(t, ts, "/v1/install", InstallRequest{Skill: "inst"})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body InstallResponse
	readJSON(t, resp, &body)
	if body.Status != "ok" {
		t.Fatalf("body = %+v", body)
	}
}

func TestServer_InstallMissingFieldReturns400(t *testing.T) {
	ts := setupServer(t, testRunnerCfg(t), nil)
	resp := doPost(t, ts, "/v1/install", map[string]string{})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestServer_MalformedBodyReturns400(t *testing.T) {
	ts := setupServer(t, testRunnerCfg(t), nil)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/run", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestServer_BodyTooLargeReturns400(t *testing.T) {
	ts := setupServer(t, testRunnerCfg(t), nil)
	big := strings.Repeat("a", 2<<20) // 2 MB, over the 1 MB cap
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/run",
		strings.NewReader(`{"skill":"x","script":"y","stdin":"`+big+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestServer_RunCtxCancelsSubprocess(t *testing.T) {
	skill := makeSkillWithScript(t, "slow", "loop.sh", "#!/bin/sh\nsleep 10\n", 0o755)
	cfg := testRunnerCfg(t)
	cfg.MaxTimeout = 500 * time.Millisecond
	ts := setupServer(t, cfg, []SkillInfo{skill})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/run",
		bytes.NewReader(mustJSON(t, RunRequest{Skill: "slow", Script: "scripts/loop.sh"})))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body RunResponse
	readJSON(t, resp, &body)
	if !body.TimedOut {
		t.Fatalf("expected timeout; body = %+v", body)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
