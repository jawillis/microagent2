// Package hindsight is a typed Go client for Hindsight's REST API (v0.5.0).
// Methods mirror the endpoints memory-service uses: retain, recall, reflect,
// memory history/delete, bank list/create/config, directive CRUD, webhook CRUD,
// and consolidate.
package hindsight

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a thin HTTP wrapper over Hindsight's v1 REST API. Callers supply
// the bank ID per call rather than constructing a per-bank client, which
// matches memory-service's single-bank model but leaves the option open.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	namespace  string // defaults to "default"
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the default http.Client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.httpClient = c }
}

// WithNamespace overrides the path namespace (Hindsight uses /v1/default/banks
// by default — "default" is the namespace).
func WithNamespace(ns string) Option {
	return func(cl *Client) { cl.namespace = ns }
}

// New constructs a Client.
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		namespace:  "default",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Error is the error returned when Hindsight responds with a non-2xx status.
type Error struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *Error) Error() string {
	return fmt.Sprintf("hindsight %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// do executes an HTTP request. If out is non-nil and the response is 2xx, the
// body is JSON-decoded into out. Non-2xx responses produce a *Error.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	full := c.baseURL + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("hindsight: marshal %s %s body: %w", method, path, err)
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
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("hindsight: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Error{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Body:       string(respBody),
		}
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("hindsight: decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}

func (c *Client) bankPath(bankID, suffix string) string {
	return fmt.Sprintf("/v1/%s/banks/%s%s", c.namespace, bankID, suffix)
}

// --- Memories ---

// Retain stores one or more memory items in the bank.
func (c *Client) Retain(ctx context.Context, bankID string, req RetainRequest) (*RetainResponse, error) {
	var out RetainResponse
	if err := c.do(ctx, http.MethodPost, c.bankPath(bankID, "/memories"), nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Recall searches the bank for memories matching the query.
func (c *Client) Recall(ctx context.Context, bankID string, req RecallRequest) (*RecallResponse, error) {
	var out RecallResponse
	if err := c.do(ctx, http.MethodPost, c.bankPath(bankID, "/memories/recall"), nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteMemory removes a memory by ID.
func (c *Client) DeleteMemory(ctx context.Context, bankID, memoryID string) error {
	return c.do(ctx, http.MethodDelete, c.bankPath(bankID, "/memories/"+memoryID), nil, nil, nil)
}

// GetMemoryHistory returns the change log for a memory.
func (c *Client) GetMemoryHistory(ctx context.Context, bankID, memoryID string) ([]MemoryHistoryEntry, error) {
	var out []MemoryHistoryEntry
	if err := c.do(ctx, http.MethodGet, c.bankPath(bankID, "/memories/"+memoryID+"/history"), nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- Reflect ---

// Reflect runs synthesis against the bank.
func (c *Client) Reflect(ctx context.Context, bankID string, req ReflectRequest) (*ReflectResponse, error) {
	var out ReflectResponse
	if err := c.do(ctx, http.MethodPost, c.bankPath(bankID, "/reflect"), nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Banks ---

// ListBanks enumerates all banks in the namespace.
func (c *Client) ListBanks(ctx context.Context) (*BankListResponse, error) {
	var out BankListResponse
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/v1/%s/banks", c.namespace), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateBank idempotently creates-or-updates a bank by ID.
// Hindsight exposes this as PUT /banks/{bank_id}.
func (c *Client) CreateBank(ctx context.Context, req CreateBankRequest) (*BankListItem, error) {
	if req.BankID == "" {
		return nil, fmt.Errorf("hindsight: CreateBank: BankID is required")
	}
	var out BankListItem
	if err := c.do(ctx, http.MethodPut, c.bankPath(req.BankID, ""), nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetBankConfig returns the resolved config + bank-level overrides.
func (c *Client) GetBankConfig(ctx context.Context, bankID string) (*BankConfigResponse, error) {
	var out BankConfigResponse
	if err := c.do(ctx, http.MethodGet, c.bankPath(bankID, "/config"), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PatchBankConfig applies the given overrides.
func (c *Client) PatchBankConfig(ctx context.Context, bankID string, req BankConfigUpdate) (*BankConfigResponse, error) {
	var out BankConfigResponse
	if err := c.do(ctx, http.MethodPatch, c.bankPath(bankID, "/config"), nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Directives ---

// ListDirectives returns directives attached to a bank.
func (c *Client) ListDirectives(ctx context.Context, bankID string) (*DirectiveListResponse, error) {
	var out DirectiveListResponse
	if err := c.do(ctx, http.MethodGet, c.bankPath(bankID, "/directives"), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateDirective adds a directive to a bank.
func (c *Client) CreateDirective(ctx context.Context, bankID string, req CreateDirectiveRequest) (*Directive, error) {
	var out Directive
	if err := c.do(ctx, http.MethodPost, c.bankPath(bankID, "/directives"), nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateDirective patches a directive's fields.
func (c *Client) UpdateDirective(ctx context.Context, bankID, directiveID string, req UpdateDirectiveRequest) (*Directive, error) {
	var out Directive
	if err := c.do(ctx, http.MethodPatch, c.bankPath(bankID, "/directives/"+directiveID), nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteDirective removes a directive.
func (c *Client) DeleteDirective(ctx context.Context, bankID, directiveID string) error {
	return c.do(ctx, http.MethodDelete, c.bankPath(bankID, "/directives/"+directiveID), nil, nil, nil)
}

// --- Webhooks ---

// ListWebhooks returns webhooks registered on a bank.
func (c *Client) ListWebhooks(ctx context.Context, bankID string) (*WebhookListResponse, error) {
	var out WebhookListResponse
	if err := c.do(ctx, http.MethodGet, c.bankPath(bankID, "/webhooks"), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateWebhook registers a new webhook.
func (c *Client) CreateWebhook(ctx context.Context, bankID string, req CreateWebhookRequest) (*Webhook, error) {
	var out Webhook
	if err := c.do(ctx, http.MethodPost, c.bankPath(bankID, "/webhooks"), nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateWebhook patches a webhook.
func (c *Client) UpdateWebhook(ctx context.Context, bankID, webhookID string, req UpdateWebhookRequest) (*Webhook, error) {
	var out Webhook
	if err := c.do(ctx, http.MethodPatch, c.bankPath(bankID, "/webhooks/"+webhookID), nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteWebhook removes a webhook.
func (c *Client) DeleteWebhook(ctx context.Context, bankID, webhookID string) error {
	return c.do(ctx, http.MethodDelete, c.bankPath(bankID, "/webhooks/"+webhookID), nil, nil, nil)
}

// --- Consolidation ---

// Consolidate triggers a consolidation pass on the bank.
func (c *Client) Consolidate(ctx context.Context, bankID string) (ConsolidateResponse, error) {
	var out ConsolidateResponse
	if err := c.do(ctx, http.MethodPost, c.bankPath(bankID, "/consolidate"), nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// IsNotFound reports whether err is a Hindsight 404.
func IsNotFound(err error) bool {
	var he *Error
	if asE, ok := err.(*Error); ok {
		return asE.StatusCode == http.StatusNotFound
	}
	_ = he
	return false
}
