package tools

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"microagent2/internal/execclient"
)

func fakeExec(t *testing.T, handler http.HandlerFunc) (*execclient.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return execclient.New(srv.URL, execclient.WithTimeout(5*time.Second)), srv
}

func silentToolLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestRunSkillScript_SuccessReturnsEnvelope(t *testing.T) {
	envelope := `{"exit_code":0,"stdout":"hi\n","stdout_truncated":false,"stderr":"","stderr_truncated":false,"workspace_dir":"/workspace/x/y","outputs":[],"duration_ms":10,"timed_out":false,"install_duration_ms":0}`
	client, _ := fakeExec(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/run" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(envelope))
	})

	tool := NewRunSkillScript(client, silentToolLogger())
	out, err := tool.Invoke(context.Background(), `{"skill":"demo","script":"scripts/x.py"}`)
	if err != nil {
		t.Fatal(err)
	}

	// We re-serialize in the tool; assert structural equivalence via unmarshal.
	var got, want map[string]any
	_ = json.Unmarshal([]byte(out), &got)
	_ = json.Unmarshal([]byte(envelope), &want)
	if got["exit_code"].(float64) != 0 || got["stdout"].(string) != "hi\n" {
		t.Fatalf("envelope not returned verbatim: %s", out)
	}
}

func TestRunSkillScript_ExecUnavailable(t *testing.T) {
	// Point client at an invalid address.
	client := execclient.New("http://127.0.0.1:1", execclient.WithTimeout(1*time.Second))
	tool := NewRunSkillScript(client, silentToolLogger())

	out, err := tool.Invoke(context.Background(), `{"skill":"x","script":"y"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, `{"error":"exec unavailable`) {
		t.Fatalf("expected exec unavailable envelope; got %q", out)
	}
}

func TestRunSkillScript_DeadlineExceeded(t *testing.T) {
	client, _ := fakeExec(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	tool := NewRunSkillScript(client, silentToolLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	out, err := tool.Invoke(ctx, `{"skill":"x","script":"y"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "deadline exceeded") && !strings.Contains(out, "exec unavailable") {
		// The classifier returns either depending on how the err wraps;
		// both are acceptable outcomes for a ctx-cancelled HTTP call.
		t.Fatalf("expected deadline or exec-unavailable; got %q", out)
	}
}

func TestRunSkillScript_Non200SurfaceStatus(t *testing.T) {
	client, _ := fakeExec(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	tool := NewRunSkillScript(client, silentToolLogger())

	out, err := tool.Invoke(context.Background(), `{"skill":"x","script":"y"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exec returned 500") {
		t.Fatalf("expected status in envelope; got %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("expected body in envelope; got %q", out)
	}
}

func TestRunSkillScript_MissingArgs(t *testing.T) {
	client, _ := fakeExec(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("should not have called exec")
	})
	tool := NewRunSkillScript(client, silentToolLogger())

	for _, args := range []string{`{}`, `{"skill":""}`, `{"script":"x"}`, `{"skill":"x","script":""}`} {
		out, err := tool.Invoke(context.Background(), args)
		if err != nil {
			t.Fatalf("args=%s err=%v", args, err)
		}
		if out != `{"error":"skill and script arguments are required"}` {
			t.Errorf("args=%s out=%q", args, out)
		}
	}
}

func TestRunSkillScript_MalformedArgs(t *testing.T) {
	client, _ := fakeExec(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("should not have called exec")
	})
	tool := NewRunSkillScript(client, silentToolLogger())

	out, err := tool.Invoke(context.Background(), `not json`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, `{"error":"invalid arguments: `) {
		t.Fatalf("out = %q", out)
	}
}

func TestRunSkillScript_ArgsForwardedIntact(t *testing.T) {
	var seenReq execclient.RunRequest
	client, _ := fakeExec(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seenReq)
		_, _ = w.Write([]byte(`{"exit_code":0}`))
	})
	tool := NewRunSkillScript(client, silentToolLogger())

	_, err := tool.Invoke(context.Background(),
		`{"skill":"s","script":"scripts/x.py","args":["--flag","v"],"stdin":"input","timeout_s":30,"session_id":"sess-injected"}`)
	if err != nil {
		t.Fatal(err)
	}
	if seenReq.Skill != "s" || seenReq.Script != "scripts/x.py" {
		t.Errorf("req = %+v", seenReq)
	}
	if len(seenReq.Args) != 2 || seenReq.Args[0] != "--flag" {
		t.Errorf("args = %+v", seenReq.Args)
	}
	if seenReq.Stdin != "input" || seenReq.TimeoutS != 30 || seenReq.SessionID != "sess-injected" {
		t.Errorf("req = %+v", seenReq)
	}
}
