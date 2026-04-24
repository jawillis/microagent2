// Package memoryclient is the typed Go client for memory-service's HTTP API.
// It is the only boundary other microagent2 services speak through to reach
// memory — context-manager, retro-agent, and future curiosity/proactive agents
// call these methods. No consumer calls hindsight directly.
package memoryclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a thin HTTP wrapper over memory-service's JSON API.
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

// New constructs a Client.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Error is the error returned when memory-service responds with a non-2xx status.
type Error struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *Error) Error() string {
	return fmt.Sprintf("memoryclient %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// --- Request/response types ---

// RetainRequest asks memory-service to store a memory. Provenance defaults to
// "explicit" on the server side if not set here. Numeric fields like
// confidence or salience go in Metadata serialized as strings.
type RetainRequest struct {
	Content           string            `json:"content"`
	Tags              []string          `json:"tags,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	Context           string            `json:"context,omitempty"`
	Timestamp         string            `json:"timestamp,omitempty"`
	ObservationScopes string            `json:"observation_scopes,omitempty"`
	DocumentID        string            `json:"document_id,omitempty"`
}

// RetainResponse is memory-service's retain reply.
type RetainResponse struct {
	Success    bool   `json:"success"`
	BankID     string `json:"bank_id"`
	ItemsCount int    `json:"items_count"`
	Async      bool   `json:"async"`
}

// RecallRequest asks memory-service for memories matching a query.
type RecallRequest struct {
	Query string   `json:"query"`
	Limit int      `json:"limit,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	Types []string `json:"types,omitempty"` // defaults to ["world","experience"] server-side
}

// MemorySummary is a consumer-facing memory projection. This is deliberately
// narrower than Hindsight's RecallResult — consumers get what they need to
// render memories, not the full upstream shape.
type MemorySummary struct {
	ID       string            `json:"id"`
	Content  string            `json:"content"`
	Tags     []string          `json:"tags,omitempty"`
	Score    float64           `json:"score,omitempty"`
	Type     string            `json:"type,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// RecallResponse holds recall results plus optional source-fact provenance.
type RecallResponse struct {
	Memories    []MemorySummary          `json:"memories"`
	SourceFacts map[string]MemorySummary `json:"source_facts,omitempty"`
}

// ReflectRequest asks memory-service for a synthesized answer.
type ReflectRequest struct {
	Query string `json:"query"`
	Tags  []string `json:"tags,omitempty"`
}

// ReflectResponse is memory-service's reflect reply.
type ReflectResponse struct {
	Text string `json:"text"`
}

// ForgetRequest asks memory-service to delete a memory — either by ID or by
// best-match recall against a description.
type ForgetRequest struct {
	MemoryID string `json:"memory_id,omitempty"`
	Query    string `json:"query,omitempty"`
}

// ForgetResponse reports what was deleted.
type ForgetResponse struct {
	DeletedID string `json:"deleted_id"`
}

// --- HTTP plumbing ---

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	full := c.baseURL + path

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("memoryclient: marshal %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, full, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if cid, ok := CorrelationID(ctx); ok {
		req.Header.Set("X-Correlation-ID", cid)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("memoryclient: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Error{StatusCode: resp.StatusCode, Method: method, Path: path, Body: string(respBody)}
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("memoryclient: decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

// --- API ---

// Retain stores a memory.
func (c *Client) Retain(ctx context.Context, req RetainRequest) (*RetainResponse, error) {
	var out RetainResponse
	if err := c.do(ctx, http.MethodPost, "/retain", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Recall searches memories.
func (c *Client) Recall(ctx context.Context, req RecallRequest) (*RecallResponse, error) {
	var out RecallResponse
	if err := c.do(ctx, http.MethodPost, "/recall", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Reflect asks for synthesis.
func (c *Client) Reflect(ctx context.Context, req ReflectRequest) (*ReflectResponse, error) {
	var out ReflectResponse
	if err := c.do(ctx, http.MethodPost, "/reflect", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Forget deletes a memory.
func (c *Client) Forget(ctx context.Context, req ForgetRequest) (*ForgetResponse, error) {
	var out ForgetResponse
	if err := c.do(ctx, http.MethodPost, "/forget", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- correlation plumbing ---

type correlationCtxKey struct{}

// WithCorrelationID attaches a correlation ID to ctx; all subsequent calls
// via this client will forward it as X-Correlation-ID to memory-service.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationCtxKey{}, id)
}

// CorrelationID extracts a correlation ID previously attached via WithCorrelationID.
func CorrelationID(ctx context.Context) (string, bool) {
	v := ctx.Value(correlationCtxKey{})
	if v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}
