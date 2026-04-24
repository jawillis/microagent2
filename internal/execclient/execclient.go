// Package execclient is the typed Go client for the exec service's HTTP API.
// It is imported by main-agent's run_skill_script tool and any future caller
// that wants to run sandboxed scripts. Wire types are reused from
// internal/exec to keep one source of truth.
package execclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"microagent2/internal/exec"
)

// Re-exported for consumers that don't want to import internal/exec directly.
type (
	RunRequest      = exec.RunRequest
	RunResponse     = exec.RunResponse
	InstallRequest  = exec.InstallRequest
	InstallResponse = exec.InstallResponse
	HealthResponse  = exec.HealthResponse
	BashRequest     = exec.BashRequest
	BashResponse    = exec.BashResponse
)

// Client is a thin HTTP wrapper over exec's JSON API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the default http.Client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.httpClient = c }
}

// WithTimeout sets the per-request timeout on the default http.Client.
// Overridden if WithHTTPClient is also passed.
func WithTimeout(d time.Duration) Option {
	return func(cl *Client) {
		if cl.httpClient != nil {
			cl.httpClient.Timeout = d
		}
	}
}

// New constructs a Client pointed at baseURL (e.g. "http://exec:8085").
// Default timeout is 130s — exec's 120s server cap plus 10s buffer so
// timed-out envelopes reach the caller before the client disconnects.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 130 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Error is returned when exec responds with a non-2xx status.
type Error struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *Error) Error() string {
	return fmt.Sprintf("execclient %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// Run invokes POST /v1/run and decodes the envelope.
func (c *Client) Run(ctx context.Context, req *RunRequest) (*RunResponse, error) {
	var resp RunResponse
	if err := c.do(ctx, http.MethodPost, "/v1/run", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Install invokes POST /v1/install for the given skill name.
func (c *Client) Install(ctx context.Context, skill string) (*InstallResponse, error) {
	var resp InstallResponse
	if err := c.do(ctx, http.MethodPost, "/v1/install", InstallRequest{Skill: skill}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Health invokes GET /v1/health.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	var resp HealthResponse
	if err := c.do(ctx, http.MethodGet, "/v1/health", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Bash invokes POST /v1/bash and decodes the envelope.
func (c *Client) Bash(ctx context.Context, req *BashRequest) (*BashResponse, error) {
	var resp BashResponse
	if err := c.do(ctx, http.MethodPost, "/v1/bash", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// do is the shared request helper. Caller-supplied ctx drives deadline;
// the client's own timeout is the ceiling.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("execclient marshal %s: %w", path, err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("execclient new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Cap response body reads to guard against pathological sizes.
	// The envelope is bounded by exec's caps; 1 MB is comfortable.
	const maxResponseBytes = 1 << 20
	buf, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("execclient read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Error{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Body:       truncate(string(buf), 200),
		}
	}

	if out != nil {
		if err := json.Unmarshal(buf, out); err != nil {
			return fmt.Errorf("execclient decode %s: %w", path, err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
