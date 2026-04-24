package gateway

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"strings"

	"github.com/google/uuid"
	"microagent2/internal/config"
	appcontext "microagent2/internal/context"
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
	"microagent2/internal/retro"
)

//go:embed web/*
var webFiles embed.FS

type Server struct {
	client          *messaging.Client
	logger          *slog.Logger
	mux             *http.ServeMux
	configStore     *config.Store
	sessions        *appcontext.SessionStore
	requestTimeoutS int
	gatewayPort     string
	llamaAddr       string
	muninnAddr      string
}

func New(client *messaging.Client, logger *slog.Logger, configStore *config.Store, sessions *appcontext.SessionStore, requestTimeoutS int, gatewayPort, llamaAddr, muninnAddr string) *Server {
	s := &Server{
		client:          client,
		logger:          logger,
		mux:             http.NewServeMux(),
		configStore:     configStore,
		sessions:        sessions,
		requestTimeoutS: requestTimeoutS,
		gatewayPort:     gatewayPort,
		llamaAddr:       llamaAddr,
		muninnAddr:      muninnAddr,
	}
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	s.mux.HandleFunc("GET /v1/config", s.handleGetConfig)
	s.mux.HandleFunc("PUT /v1/config", s.handlePutConfig)
	s.mux.HandleFunc("GET /v1/sessions", s.handleListSessions)
	s.mux.HandleFunc("GET /v1/sessions/{id}", s.handleGetSession)
	s.mux.HandleFunc("DELETE /v1/sessions/{id}", s.handleDeleteSession)
	s.mux.HandleFunc("POST /v1/retro/{session}/trigger", s.handleRetroTrigger)
	s.mux.HandleFunc("GET /v1/status", s.handleStatus)

	webFS, _ := fs.Sub(webFiles, "web")
	s.mux.Handle("GET /", http.FileServer(http.FS(webFS)))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

type openAIRequest struct {
	Model     string      `json:"model"`
	Messages  []openAIMsg `json:"messages"`
	Stream    bool        `json:"stream"`
	SessionID string      `json:"session_id,omitempty"`
}

type openAIMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	ID        string         `json:"id"`
	Object    string         `json:"object"`
	Created   int64          `json:"created"`
	Model     string         `json:"model"`
	SessionID string         `json:"session_id,omitempty"`
	Choices   []openAIChoice `json:"choices"`
}

type openAIChoice struct {
	Index        int       `json:"index"`
	Message      openAIMsg `json:"message,omitempty"`
	Delta        *openAIMsg `json:"delta,omitempty"`
	FinishReason *string   `json:"finish_reason"`
}

type openAIError struct {
	Error openAIErrorDetail `json:"error"`
}

type openAIErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	chat := config.DefaultChatConfig()
	s.configStore.Load(r.Context(), config.KeyChat, &chat)
	model := chat.Model
	if model == "" {
		model = "default"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"id":       model,
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "microagent",
			},
		},
	})
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req openAIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Failed to parse request body")
		return
	}

	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "messages array is required and must not be empty")
		return
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	correlationID := messaging.NewCorrelationID()

	msgs := make([]messaging.ChatMsg, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = messaging.ChatMsg{Role: m.Role, Content: m.Content}
	}

	replyStream := fmt.Sprintf("stream:gateway:reply:%s", correlationID)

	msg, err := messaging.NewMessage(messaging.TypeChatRequest, "gateway", messaging.ChatRequestPayload{
		SessionID: sessionID,
		Messages:  msgs,
		Model:     req.Model,
		Stream:    req.Stream,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create message")
		return
	}
	msg.CorrelationID = correlationID
	msg.ReplyStream = replyStream

	if _, err := s.client.Publish(r.Context(), messaging.StreamGatewayRequests, msg); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to publish request")
		return
	}

	w.Header().Set("X-Session-ID", sessionID)

	if req.Stream {
		s.handleStreaming(r.Context(), w, sessionID, correlationID, req.Model)
	} else {
		s.handleNonStreaming(r.Context(), w, replyStream, correlationID, sessionID, req.Model)
	}
}

func (s *Server) handleNonStreaming(ctx context.Context, w http.ResponseWriter, replyStream, correlationID, sessionID, model string) {
	reply, err := s.client.WaitForReply(ctx, replyStream, correlationID, time.Duration(s.requestTimeoutS)*time.Second)
	if err != nil {
		writeError(w, http.StatusGatewayTimeout, "timeout", "Request timed out waiting for response")
		return
	}

	var payload messaging.ChatResponsePayload
	if err := reply.DecodePayload(&payload); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to decode response")
		return
	}

	finish := "stop"
	resp := openAIResponse{
		ID:        "chatcmpl-" + correlationID[:8],
		Object:    "chat.completion",
		Created:   time.Now().Unix(),
		Model:     model,
		SessionID: sessionID,
		Choices: []openAIChoice{{
			Index:        0,
			Message:      openAIMsg{Role: "assistant", Content: payload.Content},
			FinishReason: &finish,
		}},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleStreaming(ctx context.Context, w http.ResponseWriter, sessionID, correlationID, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "Streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	channel := fmt.Sprintf(messaging.ChannelTokens, sessionID)
	sub := s.client.PubSubSubscribe(ctx, channel)
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case redisMsg := <-sub.Channel():
			if redisMsg == nil {
				continue
			}
			var msg messaging.Message
			if err := json.Unmarshal([]byte(redisMsg.Payload), &msg); err != nil {
				continue
			}

			var token messaging.TokenPayload
			if err := msg.DecodePayload(&token); err != nil {
				continue
			}

			if token.Done {
				finish := "stop"
				chunk := openAIResponse{
					ID:      "chatcmpl-" + correlationID[:8],
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   model,
					Choices: []openAIChoice{{
						Index:        0,
						Delta:        &openAIMsg{},
						FinishReason: &finish,
					}},
				}
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}

			chunk := openAIResponse{
				ID:      "chatcmpl-" + correlationID[:8],
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   model,
				Choices: []openAIChoice{{
					Index: 0,
					Delta: &openAIMsg{Role: "assistant", Content: token.Token},
				}},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	all, err := s.configStore.ReadAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read config")
		return
	}

	sections := map[string]json.RawMessage{
		"chat":   mustDefault[config.ChatConfig](all, "chat", config.DefaultChatConfig()),
		"memory": mustDefault[config.MemoryConfig](all, "memory", config.DefaultMemoryConfig()),
		"broker": mustDefault[config.BrokerConfig](all, "broker", config.DefaultBrokerConfig()),
		"retro":  mustDefault[config.RetroConfig](all, "retro", config.DefaultRetroConfig()),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sections)
}

func mustDefault[T any](all map[string]json.RawMessage, key string, def T) json.RawMessage {
	if raw, ok := all[key]; ok {
		return raw
	}
	data, _ := json.Marshal(def)
	return data
}

type configUpdateRequest struct {
	Section string          `json:"section"`
	Values  json.RawMessage `json:"values"`
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var req configUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Failed to parse request body")
		return
	}

	if !config.ValidSection(req.Section) {
		writeError(w, http.StatusBadRequest, "invalid_section", fmt.Sprintf("Invalid config section: %s", req.Section))
		return
	}

	if err := s.configStore.Save(r.Context(), req.Section, req.Values); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save config")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type sessionSummary struct {
	SessionID string `json:"session_id"`
	TurnCount int    `json:"turn_count"`
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	var sessions []sessionSummary
	var cursor uint64

	for {
		keys, next, err := s.client.Redis().Scan(r.Context(), cursor, "session:*:history", 100).Result()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to scan sessions")
			return
		}
		for _, key := range keys {
			parts := strings.Split(key, ":")
			if len(parts) != 3 {
				continue
			}
			sid := parts[1]
			count, _ := s.client.Redis().LLen(r.Context(), key).Result()
			sessions = append(sessions, sessionSummary{
				SessionID: sid,
				TurnCount: int(count),
			})
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}

	if sessions == nil {
		sessions = []sessionSummary{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	history, err := s.sessions.GetHistory(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read session")
		return
	}
	if len(history) == 0 {
		exists, _ := s.client.Redis().Exists(r.Context(), fmt.Sprintf("session:%s:history", sessionID)).Result()
		if exists == 0 {
			writeError(w, http.StatusNotFound, "not_found", "Session not found")
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"session_id": sessionID,
		"messages":   history,
	})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	key := fmt.Sprintf("session:%s:history", sessionID)

	deleted, err := s.client.Redis().Del(r.Context(), key).Result()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete session")
		return
	}
	if deleted == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Session not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

var validJobTypes = map[string]retro.JobType{
	"memory_extraction": retro.JobMemoryExtraction,
	"skill_creation":    retro.JobSkillCreation,
	"curation":          retro.JobCuration,
}

type retroTriggerRequest struct {
	JobType string `json:"job_type"`
}

func (s *Server) handleRetroTrigger(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session")

	var req retroTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Failed to parse request body")
		return
	}

	jobType, ok := validJobTypes[req.JobType]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_job_type", fmt.Sprintf("Invalid job_type: %s. Valid types: memory_extraction, skill_creation, curation", req.JobType))
		return
	}

	key := fmt.Sprintf("session:%s:history", sessionID)
	exists, err := s.client.Redis().Exists(r.Context(), key).Result()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to check session")
		return
	}
	if exists == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Session not found")
		return
	}

	acquired, err := retro.AcquireLock(r.Context(), s.client.Redis(), sessionID, jobType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to acquire lock")
		return
	}
	if !acquired {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "Job already running for this session"})
		return
	}

	msg, err := messaging.NewMessage(messaging.TypeRetroTrigger, "gateway", messaging.RetroTriggerPayload{
		SessionID: sessionID,
		JobType:   req.JobType,
	})
	if err != nil {
		retro.ReleaseLock(r.Context(), s.client.Redis(), sessionID, jobType)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create trigger message")
		return
	}

	if _, err := s.client.Publish(r.Context(), messaging.StreamRetroTriggers, msg); err != nil {
		retro.ReleaseLock(r.Context(), s.client.Redis(), sessionID, jobType)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to publish trigger")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"status":     "accepted",
		"session_id": sessionID,
		"job_type":   req.JobType,
	})
}

type agentStatus struct {
	AgentID      string   `json:"agent_id"`
	Priority     int      `json:"priority"`
	Preemptible  bool     `json:"preemptible"`
	Capabilities []string `json:"capabilities"`
	Trigger      string   `json:"trigger"`
}

type statusResponse struct {
	Services []ServiceHealth `json:"services"`
	Agents   []agentStatus   `json:"agents"`
	System   systemInfo      `json:"system"`
}

type systemInfo struct {
	GatewayPort string `json:"gateway_port"`
	LlamaAddr   string `json:"llama_addr"`
	MuninnAddr  string `json:"muninn_addr"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	services := []ServiceHealth{
		checkValkey(ctx, s),
		checkHTTPService(ctx, "llama.cpp", s.llamaAddr+"/health", 5*time.Second),
		checkHTTPService(ctx, "muninndb", s.muninnAddr+"/api/health", 5*time.Second),
	}

	registered, err := registry.ListRegistered(ctx, s.client.Redis())
	if err != nil {
		s.logger.Warn("failed to list registered agents", "error", err)
	}

	var agents []agentStatus
	for _, a := range registered {
		agents = append(agents, agentStatus{
			AgentID:      a.AgentID,
			Priority:     a.Priority,
			Preemptible:  a.Preemptible,
			Capabilities: a.Capabilities,
			Trigger:      a.Trigger,
		})
	}
	if agents == nil {
		agents = []agentStatus{}
	}

	resp := statusResponse{
		Services: services,
		Agents:   agents,
		System: systemInfo{
			GatewayPort: s.gatewayPort,
			LlamaAddr:   s.llamaAddr,
			MuninnAddr:  s.muninnAddr,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(openAIError{
		Error: openAIErrorDetail{
			Message: message,
			Type:    "invalid_request_error",
			Code:    code,
		},
	})
}
