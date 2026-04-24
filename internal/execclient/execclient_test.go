package execclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNew_DefaultsAndTrim(t *testing.T) {
	c := New("http://exec:8085/")
	if c.baseURL != "http://exec:8085" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
	if c.httpClient.Timeout != 130*time.Second {
		t.Errorf("default timeout = %v", c.httpClient.Timeout)
	}
}

func TestRun_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/run" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var req RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Skill != "demo" || req.Script != "scripts/x.py" {
			t.Errorf("request: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(RunResponse{
			ExitCode: 0,
			Stdout:   "hello",
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	resp, err := c.Run(context.Background(), &RunRequest{Skill: "demo", Script: "scripts/x.py"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != 0 || resp.Stdout != "hello" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestRun_Non200ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.Run(context.Background(), &RunRequest{Skill: "x", Script: "y"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("not an *Error: %T %v", err, err)
	}
	if ce.StatusCode != 500 {
		t.Errorf("status = %d", ce.StatusCode)
	}
	if !strings.Contains(ce.Body, "internal") {
		t.Errorf("body: %q", ce.Body)
	}
}

func TestRun_ContextCancelReturnsPromptly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Safety valve: return after 5s regardless so the test doesn't hang
		// its own server shutdown if context cancel propagation is flaky.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Run(ctx, &RunRequest{Skill: "x", Script: "y"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancel error")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("client should have returned quickly on ctx cancel; took %v", elapsed)
	}
}

func TestRun_BadJSONDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.Run(context.Background(), &RunRequest{Skill: "x", Script: "y"})
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("err should mention decode: %v", err)
	}
}

func TestInstall_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/install" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var req InstallRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Skill != "demo" {
			t.Errorf("skill = %q", req.Skill)
		}
		_ = json.NewEncoder(w).Encode(InstallResponse{Status: "ok", DurationMS: 42})
	}))
	defer srv.Close()

	c := New(srv.URL)
	resp, err := c.Install(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ok" || resp.DurationMS != 42 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestHealth_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" || r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok", Ready: true, PrewarmedSkills: []string{}})
	}))
	defer srv.Close()

	c := New(srv.URL)
	resp, err := c.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ok" || !resp.Ready {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestWithHTTPClient_Override(t *testing.T) {
	custom := &http.Client{Timeout: 5 * time.Second}
	c := New("http://x", WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Fatal("custom client not applied")
	}
}

func TestWithTimeout_Override(t *testing.T) {
	c := New("http://x", WithTimeout(7*time.Second))
	if c.httpClient.Timeout != 7*time.Second {
		t.Fatalf("timeout = %v", c.httpClient.Timeout)
	}
}
