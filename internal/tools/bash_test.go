package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"microagent2/internal/execclient"
)

func TestBashTool_SuccessReturnsEnvelope(t *testing.T) {
	envelope := `{"exit_code":0,"stdout":"ok\n","stdout_truncated":false,"stderr":"","stderr_truncated":false,"sandbox_dir":"/sandbox/s","duration_ms":3,"timed_out":false}`
	client, _ := fakeExec(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bash" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(envelope))
	})
	tool := NewBash(client, silentToolLogger())

	out, err := tool.Invoke(context.Background(), `{"command":"echo ok","session_id":"s"}`)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(out), &got)
	if got["exit_code"].(float64) != 0 || got["stdout"].(string) != "ok\n" {
		t.Fatalf("envelope not returned verbatim: %s", out)
	}
	if got["sandbox_dir"].(string) != "/sandbox/s" {
		t.Fatalf("sandbox_dir missing: %s", out)
	}
}

func TestBashTool_MissingCommand(t *testing.T) {
	client, _ := fakeExec(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("should not have called exec")
	})
	tool := NewBash(client, silentToolLogger())

	for _, args := range []string{`{}`, `{"command":""}`, `{"command":"   ","session_id":"s"}`} {
		out, err := tool.Invoke(context.Background(), args)
		if err != nil {
			t.Fatalf("args=%s err=%v", args, err)
		}
		if out != `{"error":"command argument is required"}` {
			t.Errorf("args=%s out=%q", args, out)
		}
	}
}

func TestBashTool_MalformedArgs(t *testing.T) {
	client, _ := fakeExec(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("should not have called exec")
	})
	tool := NewBash(client, silentToolLogger())
	out, err := tool.Invoke(context.Background(), `not json`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, `{"error":"invalid arguments: `) {
		t.Fatalf("out = %q", out)
	}
}

func TestBashTool_ExecUnavailable(t *testing.T) {
	client := execclient.New("http://127.0.0.1:1", execclient.WithTimeout(1*time.Second))
	tool := NewBash(client, silentToolLogger())
	out, err := tool.Invoke(context.Background(), `{"command":"echo x","session_id":"s"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, `{"error":"exec unavailable`) {
		t.Fatalf("expected exec-unavailable envelope; got %q", out)
	}
}

func TestBashTool_Non200SurfaceStatus(t *testing.T) {
	client, _ := fakeExec(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"sandbox full"}`))
	})
	tool := NewBash(client, silentToolLogger())
	out, err := tool.Invoke(context.Background(), `{"command":"dd if=/dev/zero of=big","session_id":"s"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exec returned 409") || !strings.Contains(out, "sandbox full") {
		t.Fatalf("expected 409 surfaced; got %q", out)
	}
}

func TestBashTool_ArgsForwardedIntact(t *testing.T) {
	var seen execclient.BashRequest
	client, _ := fakeExec(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_, _ = w.Write([]byte(`{"exit_code":0}`))
	})
	tool := NewBash(client, silentToolLogger())
	_, err := tool.Invoke(context.Background(), `{"command":"uname -a","session_id":"sess-inject","timeout_s":15}`)
	if err != nil {
		t.Fatal(err)
	}
	if seen.Command != "uname -a" || seen.SessionID != "sess-inject" || seen.TimeoutS != 15 {
		t.Fatalf("req = %+v", seen)
	}
}
