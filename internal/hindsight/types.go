package hindsight

// Types mirror Hindsight's REST API (v0.5.0). Fields we don't use are omitted
// from the Go structs; Hindsight's JSON passes unknown fields silently.
//
// Hindsight metadata values are `Map<string,string>` — numeric fields like
// confidence and salience must be serialized as strings at the memory-service
// layer before they reach this client.

// EntityInput is an entity to associate with retained content.
type EntityInput struct {
	Text string `json:"text"`
	Type string `json:"type,omitempty"`
}

// MemoryItem is a single retain item.
type MemoryItem struct {
	Content           string            `json:"content"`
	Timestamp         string            `json:"timestamp,omitempty"`
	Context           string            `json:"context,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	DocumentID        string            `json:"document_id,omitempty"`
	Entities          []EntityInput     `json:"entities,omitempty"`
	Tags              []string          `json:"tags,omitempty"`
	ObservationScopes string            `json:"observation_scopes,omitempty"`
}

// RetainRequest is the POST /memories body.
type RetainRequest struct {
	Items []MemoryItem `json:"items"`
	Async bool         `json:"async,omitempty"`
}

// TokenUsage reports LLM token consumption for a call.
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// RetainResponse is the POST /memories response.
type RetainResponse struct {
	Success      bool        `json:"success"`
	BankID       string      `json:"bank_id"`
	ItemsCount   int         `json:"items_count"`
	Async        bool        `json:"async"`
	OperationID  string      `json:"operation_id,omitempty"`
	OperationIDs []string    `json:"operation_ids,omitempty"`
	Usage        *TokenUsage `json:"usage,omitempty"`
}

// RecallRequest is the POST /memories/recall body.
type RecallRequest struct {
	Query          string            `json:"query"`
	Types          []string          `json:"types,omitempty"`
	Budget         string            `json:"budget,omitempty"` // "low" | "mid" | "high"
	MaxTokens      int               `json:"max_tokens,omitempty"`
	Trace          bool              `json:"trace,omitempty"`
	QueryTimestamp string            `json:"query_timestamp,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	Entities       []string          `json:"entities,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// RecallResult is a single memory or observation returned by recall.
type RecallResult struct {
	ID             string            `json:"id"`
	Text           string            `json:"text"`
	Type           string            `json:"type,omitempty"`
	Entities       []string          `json:"entities,omitempty"`
	Context        string            `json:"context,omitempty"`
	OccurredStart  string            `json:"occurred_start,omitempty"`
	OccurredEnd    string            `json:"occurred_end,omitempty"`
	MentionedAt    string            `json:"mentioned_at,omitempty"`
	DocumentID     string            `json:"document_id,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Score          float64           `json:"score,omitempty"`
	ChunkID        string            `json:"chunk_id,omitempty"`
	ProofCount     int               `json:"proof_count,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	FactType       string            `json:"fact_type,omitempty"`
}

// RecallResponse is the POST /memories/recall response.
type RecallResponse struct {
	Results     []RecallResult          `json:"results"`
	SourceFacts map[string]RecallResult `json:"source_facts,omitempty"`
}

// ReflectRequest is the POST /reflect body.
type ReflectRequest struct {
	Query          string                 `json:"query"`
	Budget         string                 `json:"budget,omitempty"`
	MaxTokens      int                    `json:"max_tokens,omitempty"`
	Tags           []string               `json:"tags,omitempty"`
	ResponseSchema map[string]interface{} `json:"response_schema,omitempty"`
}

// ReflectResponse is the POST /reflect response.
type ReflectResponse struct {
	Text             string                 `json:"text"`
	StructuredOutput map[string]interface{} `json:"structured_output,omitempty"`
	Usage            *TokenUsage            `json:"usage,omitempty"`
}

// MemoryHistoryEntry is one entry in the GET /memories/{id}/history list.
// Response schema is defined as `any` in the OpenAPI spec; we model the
// fields we observe in practice.
type MemoryHistoryEntry struct {
	ID           string            `json:"id,omitempty"`
	Text         string            `json:"text,omitempty"`
	Type         string            `json:"type,omitempty"`
	SourceFacts  []string          `json:"source_facts,omitempty"`
	ChangedAt    string            `json:"changed_at,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// DispositionTraits are the bank's disposition trait triple.
type DispositionTraits struct {
	Skepticism int `json:"skepticism"`
	Literalism int `json:"literalism"`
	Empathy    int `json:"empathy"`
}

// BankListItem is a bank summary in BankListResponse.
type BankListItem struct {
	BankID      string             `json:"bank_id"`
	Name        string             `json:"name,omitempty"`
	Disposition DispositionTraits  `json:"disposition"`
	Mission     string             `json:"mission,omitempty"`
	CreatedAt   string             `json:"created_at,omitempty"`
	UpdatedAt   string             `json:"updated_at,omitempty"`
}

// BankListResponse is the GET /banks response.
type BankListResponse struct {
	Banks []BankListItem `json:"banks"`
}

// CreateBankRequest is the POST /banks body.
// Only the non-deprecated fields are surfaced; bank config is applied via
// PATCH /banks/{id}/config after creation.
type CreateBankRequest struct {
	BankID string `json:"bank_id,omitempty"`
	Name   string `json:"name,omitempty"`
}

// BankConfigResponse is the GET /banks/{id}/config response.
type BankConfigResponse struct {
	BankID    string                 `json:"bank_id"`
	Config    map[string]interface{} `json:"config"`
	Overrides map[string]interface{} `json:"overrides"`
}

// BankConfigUpdate is the PATCH /banks/{id}/config body.
type BankConfigUpdate struct {
	Updates map[string]interface{} `json:"updates"`
}

// CreateDirectiveRequest is the POST /directives body.
type CreateDirectiveRequest struct {
	Name     string   `json:"name"`
	Content  string   `json:"content"`
	Priority int      `json:"priority,omitempty"`
	IsActive bool     `json:"is_active"`
	Tags     []string `json:"tags,omitempty"`
}

// UpdateDirectiveRequest is the PATCH /directives/{id} body; all fields optional.
type UpdateDirectiveRequest struct {
	Name     *string  `json:"name,omitempty"`
	Content  *string  `json:"content,omitempty"`
	Priority *int     `json:"priority,omitempty"`
	IsActive *bool    `json:"is_active,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// Directive is a configured directive.
type Directive struct {
	ID        string   `json:"id"`
	BankID    string   `json:"bank_id"`
	Name      string   `json:"name"`
	Content   string   `json:"content"`
	Priority  int      `json:"priority"`
	IsActive  bool     `json:"is_active"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// DirectiveListResponse is the GET /directives response.
type DirectiveListResponse struct {
	Items []Directive `json:"items"`
}

// WebhookHTTPConfig is embedded in webhook requests.
type WebhookHTTPConfig struct {
	Method         string            `json:"method,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Params         map[string]string `json:"params,omitempty"`
}

// CreateWebhookRequest is the POST /webhooks body.
type CreateWebhookRequest struct {
	URL        string             `json:"url"`
	Secret     string             `json:"secret,omitempty"`
	EventTypes []string           `json:"event_types,omitempty"`
	Enabled    bool               `json:"enabled"`
	HTTPConfig *WebhookHTTPConfig `json:"http_config,omitempty"`
}

// UpdateWebhookRequest is the PATCH /webhooks/{id} body.
type UpdateWebhookRequest struct {
	URL        *string            `json:"url,omitempty"`
	EventTypes []string           `json:"event_types,omitempty"`
	Enabled    *bool              `json:"enabled,omitempty"`
	HTTPConfig *WebhookHTTPConfig `json:"http_config,omitempty"`
}

// Webhook is a registered webhook.
type Webhook struct {
	ID         string             `json:"id"`
	BankID     string             `json:"bank_id,omitempty"`
	URL        string             `json:"url"`
	EventTypes []string           `json:"event_types"`
	Enabled    bool               `json:"enabled"`
	HTTPConfig *WebhookHTTPConfig `json:"http_config,omitempty"`
	CreatedAt  string             `json:"created_at,omitempty"`
	UpdatedAt  string             `json:"updated_at,omitempty"`
}

// WebhookListResponse is the GET /webhooks response.
type WebhookListResponse struct {
	Items []Webhook `json:"items"`
}

// ConsolidateResponse is the POST /consolidate response. The exact schema is
// not documented; we accept any JSON object to avoid brittle coupling.
type ConsolidateResponse map[string]interface{}
