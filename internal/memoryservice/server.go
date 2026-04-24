package memoryservice

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"microagent2/internal/config"
	"microagent2/internal/hindsight"
	"microagent2/internal/memoryclient"
)

// Config holds env-driven runtime configuration. Resolver is invoked
// per request to pick up runtime-tunable settings from Valkey without
// requiring a service restart.
type Config struct {
	BankID         string
	ExternalURL    string // advertised to Hindsight in webhook registrations
	WebhookSecret  string
	DefaultTimeout time.Duration

	// Resolver returns the current memory-service tunables. Expected to
	// be a thin wrapper over config.ResolveMemory so dashboard edits
	// take effect without restart. If nil, defaults are used.
	Resolver func(ctx context.Context) config.MemoryConfig
}

// Server is the memory-service HTTP handler. Policy (provenance defaulting,
// recall-types defaulting, numeric metadata coercion) lives here; Hindsight
// plumbing lives in `internal/hindsight`.
type Server struct {
	hc     *hindsight.Client
	cfg    Config
	logger *slog.Logger
	mux    *http.ServeMux

	hindsightReachable     atomic.Bool
	unknownSpeakerRetains  atomic.Int64
}

// New constructs a Server. It does not start sync; call SyncSeed separately at startup.
func New(hc *hindsight.Client, cfg Config, logger *slog.Logger) *Server {
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 30 * time.Second
	}
	s := &Server{hc: hc, cfg: cfg, logger: logger, mux: http.NewServeMux()}
	s.hindsightReachable.Store(true)
	s.routes()
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

// MarkHindsightReachable updates the health-endpoint status.
func (s *Server) MarkHindsightReachable(ok bool) { s.hindsightReachable.Store(ok) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /status", s.handleStatus)
	s.mux.HandleFunc("POST /retain", s.handleRetain)
	s.mux.HandleFunc("POST /recall", s.handleRecall)
	s.mux.HandleFunc("POST /reflect", s.handleReflect)
	s.mux.HandleFunc("POST /forget", s.handleForget)
	s.mux.HandleFunc("POST /hooks/hind/retain", s.handleWebhookRetain)
	s.mux.HandleFunc("POST /hooks/hind/consolidation", s.handleWebhookConsolidation)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	_ = r.Body.Close()
	writeJSON(w, http.StatusOK, map[string]any{
		"unknown_speaker_retains": s.unknownSpeakerRetains.Load(),
	})
}

// --- health ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_ = r.Body.Close()
	reachable := s.hindsightReachable.Load()
	status := http.StatusOK
	hStatus := "reachable"
	if !reachable {
		status = http.StatusServiceUnavailable
		hStatus = "unreachable"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":    pickStatus(reachable),
		"hindsight": hStatus,
		"bank":      s.cfg.BankID,
	})
}

func pickStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "degraded"
}

// --- retain ---

var validProvenance = map[string]bool{
	"explicit":   true,
	"implicit":   true,
	"inferred":   true,
	"researched": true,
}

func (s *Server) handleRetain(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	corrID := corrIDFromReq(r)
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.DefaultTimeout)
	defer cancel()

	var req memoryclient.RetainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "content is required")
		return
	}
	// Provenance defaulting + validation. Default comes from the config
	// resolver so operators can change it via the dashboard without
	// requiring a service restart. An invalid config value falls back to
	// "explicit" with a WARN log.
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	if _, ok := req.Metadata["provenance"]; !ok {
		req.Metadata["provenance"] = s.defaultProvenance(ctx, corrID)
	}
	if !validProvenance[req.Metadata["provenance"]] {
		writeError(w, http.StatusBadRequest, "invalid_provenance",
			fmt.Sprintf("metadata.provenance must be one of explicit|implicit|inferred|researched; got %q", req.Metadata["provenance"]))
		return
	}

	if _, ok := req.Metadata["speaker_id"]; !ok {
		memCfg := s.resolveMemoryConfig(ctx)
		if memCfg.PrimaryUserID != "" {
			req.Metadata["speaker_id"] = memCfg.PrimaryUserID
		} else {
			req.Metadata["speaker_id"] = "unknown"
		}
	}
	if req.Metadata["speaker_id"] == "unknown" {
		s.unknownSpeakerRetains.Add(1)
	}

	if ft, ok := req.Metadata["fact_type"]; ok {
		if !validFactType(ft) {
			writeError(w, http.StatusBadRequest, "invalid_fact_type",
				fmt.Sprintf("metadata.fact_type must be one of person_fact|world_fact|context_fact|procedural_fact; got %q", ft))
			return
		}
	} else {
		req.Metadata["fact_type"] = inferFactType(req.Content, req.Entities)
	}

	s.logger.Info("memory_retain_received",
		"correlation_id", corrID,
		"bank", s.cfg.BankID,
		"provenance", req.Metadata["provenance"],
		"speaker_id", req.Metadata["speaker_id"],
		"fact_type", req.Metadata["fact_type"],
		"tag_count", len(req.Tags),
	)

	var entities []hindsight.EntityInput
	for _, e := range req.Entities {
		entities = append(entities, hindsight.EntityInput{Text: e})
	}

	start := time.Now()
	hsReq := hindsight.RetainRequest{
		Items: []hindsight.MemoryItem{{
			Content:           req.Content,
			Timestamp:         req.Timestamp,
			Context:           req.Context,
			Metadata:          req.Metadata,
			DocumentID:        req.DocumentID,
			Entities:          entities,
			Tags:              req.Tags,
			ObservationScopes: req.ObservationScopes,
		}},
	}
	resp, err := s.hc.Retain(ctx, s.cfg.BankID, hsReq)
	if err != nil {
		s.logger.Error("memory_hindsight_error",
			"correlation_id", corrID,
			"endpoint", "retain",
			"error", err.Error(),
		)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	s.logger.Info("memory_retain_completed",
		"correlation_id", corrID,
		"bank", s.cfg.BankID,
		"provenance", req.Metadata["provenance"],
		"tag_count", len(req.Tags),
		"elapsed_ms", time.Since(start).Milliseconds(),
		"outcome", "ok",
	)
	writeJSON(w, http.StatusOK, memoryclient.RetainResponse{
		Success:    resp.Success,
		BankID:     resp.BankID,
		ItemsCount: resp.ItemsCount,
		Async:      resp.Async,
	})
}

// --- recall ---

func (s *Server) handleRecall(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	corrID := corrIDFromReq(r)
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.DefaultTimeout)
	defer cancel()

	var req memoryclient.RecallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query is required")
		return
	}
	if len(req.Types) == 0 {
		req.Types = s.defaultRecallTypes(ctx, corrID)
	}

	memCfg := s.resolveMemoryConfig(ctx)
	speakerID := req.SpeakerID
	if speakerID == "" {
		switch memCfg.RecallDefaultSpeakerScope {
		case "primary":
			speakerID = memCfg.PrimaryUserID
		case "explicit":
			writeError(w, http.StatusBadRequest, "speaker_required",
				"recall_default_speaker_scope is 'explicit' and no speaker_id was provided")
			return
		}
	}

	metaFilter := map[string]string{}
	if speakerID != "" {
		metaFilter["speaker_id"] = speakerID
	}
	for _, ft := range req.FactTypes {
		if !validFactType(ft) {
			writeError(w, http.StatusBadRequest, "invalid_fact_type",
				fmt.Sprintf("fact_type %q is not valid; must be person_fact|world_fact|context_fact|procedural_fact", ft))
			return
		}
	}
	if len(req.FactTypes) == 1 {
		metaFilter["fact_type"] = req.FactTypes[0]
	}

	s.logger.Info("memory_recall_received",
		"correlation_id", corrID,
		"limit", req.Limit,
		"types", req.Types,
		"speaker_id", speakerID,
		"entity_count", len(req.Entities),
		"fact_type_count", len(req.FactTypes),
	)

	start := time.Now()
	hsReq := hindsight.RecallRequest{
		Query:     req.Query,
		Types:     req.Types,
		MaxTokens: limitToTokens(req.Limit),
		Tags:      req.Tags,
		Entities:  req.Entities,
	}
	if len(metaFilter) > 0 {
		hsReq.Metadata = metaFilter
	}
	resp, err := s.hc.Recall(ctx, s.cfg.BankID, hsReq)
	if err != nil {
		s.logger.Error("memory_hindsight_error", "correlation_id", corrID, "endpoint", "recall", "error", err.Error())
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	out := memoryclient.RecallResponse{
		Memories: make([]memoryclient.MemorySummary, 0, len(resp.Results)),
	}
	for _, m := range resp.Results {
		out.Memories = append(out.Memories, translateResult(m))
	}
	if req.Limit > 0 && len(out.Memories) > req.Limit {
		out.Memories = out.Memories[:req.Limit]
	}
	if len(resp.SourceFacts) > 0 {
		out.SourceFacts = make(map[string]memoryclient.MemorySummary, len(resp.SourceFacts))
		for id, m := range resp.SourceFacts {
			out.SourceFacts[id] = translateResult(m)
		}
	}

	s.logger.Info("memory_recall_completed",
		"correlation_id", corrID,
		"memory_count", len(out.Memories),
		"elapsed_ms", time.Since(start).Milliseconds(),
		"outcome", "ok",
	)
	writeJSON(w, http.StatusOK, out)
}

func translateResult(m hindsight.RecallResult) memoryclient.MemorySummary {
	return memoryclient.MemorySummary{
		ID:       m.ID,
		Content:  m.Text,
		Tags:     m.Tags,
		Score:    m.Score,
		Type:     orString(m.Type, m.FactType),
		Metadata: m.Metadata,
	}
}

func orString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// limitToTokens maps a memoryclient Limit (result count) to a reasonable
// Hindsight MaxTokens budget. Hindsight handles sort+score internally; the
// limit is applied client-side after translation.
func limitToTokens(limit int) int {
	if limit <= 0 {
		return 4096
	}
	// rough heuristic: 512 tokens per memory
	n := limit * 512
	if n < 1024 {
		return 1024
	}
	if n > 8192 {
		return 8192
	}
	return n
}

// --- reflect ---

func (s *Server) handleReflect(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	corrID := corrIDFromReq(r)
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.DefaultTimeout*2)
	defer cancel()

	var req memoryclient.ReflectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query is required")
		return
	}
	resp, err := s.hc.Reflect(ctx, s.cfg.BankID, hindsight.ReflectRequest{
		Query: req.Query,
		Tags:  req.Tags,
	})
	if err != nil {
		s.logger.Error("memory_hindsight_error", "correlation_id", corrID, "endpoint", "reflect", "error", err.Error())
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, memoryclient.ReflectResponse{Text: resp.Text})
}

// --- forget ---

func (s *Server) handleForget(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	corrID := corrIDFromReq(r)
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.DefaultTimeout)
	defer cancel()

	var req memoryclient.ForgetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	targetID := req.MemoryID
	if targetID == "" {
		if req.Query == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "memory_id or query required")
			return
		}
		resp, err := s.hc.Recall(ctx, s.cfg.BankID, hindsight.RecallRequest{
			Query: req.Query,
		})
		if err != nil {
			s.logger.Error("memory_hindsight_error", "correlation_id", corrID, "endpoint", "forget/recall", "error", err.Error())
			writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		if len(resp.Results) == 0 {
			writeError(w, http.StatusNotFound, "not_found", "no memory matched query")
			return
		}
		targetID = resp.Results[0].ID
	}

	if err := s.hc.DeleteMemory(ctx, s.cfg.BankID, targetID); err != nil {
		if hindsight.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "memory does not exist")
			return
		}
		s.logger.Error("memory_hindsight_error", "correlation_id", corrID, "endpoint", "forget/delete", "error", err.Error())
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	s.logger.Info("memory_forget_completed", "correlation_id", corrID, "memory_id", targetID)
	writeJSON(w, http.StatusOK, memoryclient.ForgetResponse{DeletedID: targetID})
}

// --- webhooks ---

// Hindsight signs webhook bodies with HMAC-SHA256. The signature header
// format is not uniformly documented; we accept the common shapes (`X-Hub-Signature-256`,
// `X-Hindsight-Signature`, `X-Signature-256`) containing either the raw hex
// digest or `sha256=<hex>`.
var webhookSigHeaders = []string{"X-Hindsight-Signature", "X-Signature-256", "X-Hub-Signature-256", "X-Signature"}

func (s *Server) handleWebhookRetain(w http.ResponseWriter, r *http.Request) {
	s.handleWebhook(w, r, "retain.completed")
}

func (s *Server) handleWebhookConsolidation(w http.ResponseWriter, r *http.Request) {
	s.handleWebhook(w, r, "consolidation.completed")
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request, expectedEvent string) {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.verifySignature(r, body) {
		s.logger.Warn("memory_webhook_signature_invalid", "expected_event", expectedEvent)
		writeError(w, http.StatusUnauthorized, "invalid_signature", "signature did not match")
		return
	}

	// Decode enough of the payload to log useful fields; tolerate missing keys.
	var payload struct {
		Event       string `json:"event"`
		BankID      string `json:"bank_id"`
		OperationID string `json:"operation_id"`
	}
	_ = json.Unmarshal(body, &payload)

	s.logger.Info("memory_webhook_received",
		"event", payload.Event,
		"expected_event", expectedEvent,
		"bank_id", payload.BankID,
		"operation_id", payload.OperationID,
	)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) verifySignature(r *http.Request, body []byte) bool {
	if s.cfg.WebhookSecret == "" {
		return true // secret not configured; allow for local smoke
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.WebhookSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	for _, h := range webhookSigHeaders {
		if sig := r.Header.Get(h); sig != "" {
			if strings.EqualFold(sig, expected) {
				return true
			}
			if strings.EqualFold(strings.TrimPrefix(sig, "sha256="), expected) {
				return true
			}
		}
	}
	return false
}

// --- webhook registration (called by main) ---

// RegisterWebhooks ensures Hindsight knows about memory-service's webhook
// endpoints. It creates new webhooks pointing at externalURL and updates
// existing ones whose URL matches memory-service's prefix so configuration
// drift doesn't accumulate.
func (s *Server) RegisterWebhooks(ctx context.Context) error {
	if s.cfg.ExternalURL == "" {
		return fmt.Errorf("memoryservice: MEMORY_SERVICE_EXTERNAL_URL required for webhook registration")
	}
	existing, err := s.hc.ListWebhooks(ctx, s.cfg.BankID)
	if err != nil {
		return err
	}
	targets := []struct {
		urlPath string
		events  []string
	}{
		{"/hooks/hind/retain", []string{"retain.completed"}},
		{"/hooks/hind/consolidation", []string{"consolidation.completed"}},
	}
	for _, t := range targets {
		wantURL := strings.TrimRight(s.cfg.ExternalURL, "/") + t.urlPath
		var match *hindsight.Webhook
		for i, wh := range existing.Items {
			if wh.URL == wantURL {
				match = &existing.Items[i]
				break
			}
		}
		if match == nil {
			if _, err := s.hc.CreateWebhook(ctx, s.cfg.BankID, hindsight.CreateWebhookRequest{
				URL:        wantURL,
				Secret:     s.cfg.WebhookSecret,
				EventTypes: t.events,
				Enabled:    true,
			}); err != nil {
				return fmt.Errorf("create webhook %s: %w", wantURL, err)
			}
			s.logger.Info("memory_webhook_registered", "url", wantURL, "events", t.events)
			continue
		}
		// Update if event types differ or disabled.
		needsUpdate := !sameStringSet(match.EventTypes, t.events) || !match.Enabled
		if needsUpdate {
			enabled := true
			if _, err := s.hc.UpdateWebhook(ctx, s.cfg.BankID, match.ID, hindsight.UpdateWebhookRequest{
				EventTypes: t.events,
				Enabled:    &enabled,
			}); err != nil {
				return fmt.Errorf("update webhook %s: %w", wantURL, err)
			}
			s.logger.Info("memory_webhook_updated", "url", wantURL, "id", match.ID)
		}
	}
	return nil
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, s := range a {
		m[s] = true
	}
	for _, s := range b {
		if !m[s] {
			return false
		}
	}
	return true
}

// --- speaker / fact_type helpers ---

var validFactTypes = map[string]bool{
	"person_fact":     true,
	"world_fact":      true,
	"context_fact":    true,
	"procedural_fact": true,
}

func validFactType(ft string) bool { return validFactTypes[ft] }

var timeScuedCues = []string{
	"yesterday", "today", "tomorrow", "last week", "this week",
	"last month", "this morning", "tonight", "recently",
	"just now", "earlier", "later", "ago",
}

func inferFactType(content string, entities []string) string {
	lower := strings.ToLower(content)
	for _, cue := range timeScuedCues {
		if strings.Contains(lower, cue) {
			return "context_fact"
		}
	}
	for _, e := range entities {
		if !strings.HasPrefix(e, "class:") {
			return "person_fact"
		}
	}
	return "world_fact"
}

// UnknownSpeakerRetains returns the in-process count of retains with speaker_id="unknown".
func (s *Server) UnknownSpeakerRetains() int64 {
	return s.unknownSpeakerRetains.Load()
}

// --- helpers ---

type errBody struct {
	Error errDetail `json:"error"`
}
type errDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errBody{Error: errDetail{Code: code, Message: msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func corrIDFromReq(r *http.Request) string {
	if id := r.Header.Get("X-Correlation-ID"); id != "" {
		return id
	}
	return ""
}

// resolveMemoryConfig returns the current memory config, preferring the
// injected resolver (runtime-tunable via Valkey) and falling back to
// hardcoded defaults when no resolver is configured.
func (s *Server) resolveMemoryConfig(ctx context.Context) config.MemoryConfig {
	if s.cfg.Resolver != nil {
		return s.cfg.Resolver(ctx)
	}
	return config.DefaultMemoryConfig()
}

// defaultProvenance returns the provenance value to use when a /retain
// request omits metadata.provenance. An invalid configured value falls
// back to "explicit" with a WARN log so a misconfiguration doesn't
// reject writes.
func (s *Server) defaultProvenance(ctx context.Context, corrID string) string {
	p := s.resolveMemoryConfig(ctx).DefaultProvenance
	if validProvenance[p] {
		return p
	}
	s.logger.Warn("memory_default_provenance_invalid",
		"correlation_id", corrID,
		"configured_value", p,
		"fallback", config.DefaultProvenance,
	)
	return config.DefaultProvenance
}

// defaultRecallTypes maps the config's enum value ("observation" |
// "world_experience" | "all") to the Hindsight `types` list. An
// unrecognized value falls back to "observation".
func (s *Server) defaultRecallTypes(ctx context.Context, corrID string) []string {
	v := s.resolveMemoryConfig(ctx).RecallDefaultTypes
	switch v {
	case "", "observation":
		return []string{"observation"}
	case "world_experience":
		return []string{"world", "experience"}
	case "all":
		return []string{"observation", "world", "experience"}
	default:
		s.logger.Warn("memory_default_recall_types_invalid",
			"correlation_id", corrID,
			"configured_value", v,
			"fallback", "observation",
		)
		return []string{"observation"}
	}
}
