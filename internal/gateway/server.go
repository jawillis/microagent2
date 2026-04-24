package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"microagent2/internal/messaging"
)

type Server struct {
	client *messaging.Client
	logger *slog.Logger
	mux    *http.ServeMux
}

func New(client *messaging.Client, logger *slog.Logger) *Server {
	s := &Server{
		client: client,
		logger: logger,
		mux:    http.NewServeMux(),
	}
	s.mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

type openAIRequest struct {
	Model    string         `json:"model"`
	Messages []openAIMsg    `json:"messages"`
	Stream   bool           `json:"stream"`
}

type openAIMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
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

	sessionID := deriveSessionID(req.Messages)
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

	if req.Stream {
		s.handleStreaming(r.Context(), w, sessionID, correlationID, req.Model)
	} else {
		s.handleNonStreaming(r.Context(), w, replyStream, correlationID, req.Model)
	}
}

func (s *Server) handleNonStreaming(ctx context.Context, w http.ResponseWriter, replyStream, correlationID, model string) {
	reply, err := s.client.WaitForReply(ctx, replyStream, correlationID, 120*time.Second)
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
		ID:      "chatcmpl-" + correlationID[:8],
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
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

func deriveSessionID(messages []openAIMsg) string {
	return uuid.NewSHA1(uuid.NameSpaceDNS, []byte("microagent2")).String()
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
