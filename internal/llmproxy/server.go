package llmproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"microagent2/internal/broker"
	"microagent2/internal/messaging"
)

const (
	slotRequestStream = "stream:broker:slot-requests"
	llmRequestStream  = "stream:broker:llm-requests"
)

type Config struct {
	Identity         string
	SlotTimeout      time.Duration
	RequestTimeout   time.Duration
	Model            string
	BrokerReplyGroup string
}

type Server struct {
	client *messaging.Client
	cfg    Config
	logger *slog.Logger
	mux    *http.ServeMux

	outstanding atomic.Int64
}

func New(client *messaging.Client, cfg Config, logger *slog.Logger) *Server {
	if cfg.Identity == "" {
		cfg.Identity = "llm-proxy"
	}
	if cfg.SlotTimeout <= 0 {
		cfg.SlotTimeout = 10 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 120 * time.Second
	}
	s := &Server{
		client: client,
		cfg:    cfg,
		logger: logger,
		mux:    http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_ = r.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// OpenAI-compatible chat-completions request shape (subset we understand).
type openAIChatRequest struct {
	Model      string                 `json:"model"`
	Messages   []messaging.ChatMsg    `json:"messages"`
	Stream     bool                   `json:"stream"`
	Tools      []messaging.ToolSchema `json:"tools,omitempty"`
	ToolChoice json.RawMessage        `json:"tool_choice,omitempty"`
}

type openAIChoice struct {
	Index        int                  `json:"index"`
	Message      openAIMessage        `json:"message"`
	FinishReason string               `json:"finish_reason"`
}

type openAIMessage struct {
	Role      string               `json:"role"`
	Content   string               `json:"content"`
	ToolCalls []messaging.ToolCall `json:"tool_calls,omitempty"`
}

type openAIChatResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
}

type openAIStreamDelta struct {
	Role      string               `json:"role,omitempty"`
	Content   string               `json:"content,omitempty"`
	ToolCalls []messaging.ToolCall `json:"tool_calls,omitempty"`
}

type openAIStreamChoice struct {
	Index        int                `json:"index"`
	Delta        openAIStreamDelta  `json:"delta"`
	FinishReason *string            `json:"finish_reason"`
}

type openAIStreamChunk struct {
	ID      string               `json:"id"`
	Object  string               `json:"object"`
	Created int64                `json:"created"`
	Model   string               `json:"model"`
	Choices []openAIStreamChoice `json:"choices"`
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	s.outstanding.Add(1)
	defer s.outstanding.Add(-1)

	corrID := r.Header.Get("X-Correlation-ID")
	if corrID == "" {
		corrID = messaging.NewCorrelationID()
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()

	var req openAIChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	tcString := decodeToolChoice(req.ToolChoice)

	s.logger.Info("llm_proxy_request_received",
		"correlation_id", corrID,
		"stream", req.Stream,
		"tool_count", len(req.Tools),
		"message_count", len(req.Messages),
	)

	// Acquire a hindsight-class slot via broker.
	slotID, slotCorrID, replyStream, err := s.acquireSlot(ctx, corrID)
	if err != nil {
		s.logger.Warn("llm_proxy_slot_acquire_failed",
			"correlation_id", corrID,
			"error", err.Error(),
		)
		s.defensiveRelease(ctx, corrID)
		writeJSONError(w, http.StatusServiceUnavailable, "slot_unavailable", err.Error())
		return
	}
	s.logger.Info("llm_proxy_slot_acquired",
		"correlation_id", corrID,
		"slot_correlation_id", slotCorrID,
		"slot", slotID,
	)

	// Ensure the slot is released exactly once.
	var released atomic.Bool
	release := func() {
		if !released.CompareAndSwap(false, true) {
			return
		}
		relCtx, relCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer relCancel()
		relMsg, err := messaging.NewMessage(messaging.TypeSlotRelease, s.cfg.Identity, messaging.SlotReleasePayload{
			AgentID: s.cfg.Identity,
			SlotID:  slotID,
		})
		if err != nil {
			s.logger.Error("llm_proxy_slot_release_encode_failed",
				"correlation_id", corrID,
				"slot", slotID,
				"error", err.Error(),
			)
			return
		}
		if _, err := s.client.Publish(relCtx, slotRequestStream, relMsg); err != nil {
			s.logger.Error("llm_proxy_slot_release_publish_failed",
				"correlation_id", corrID,
				"slot", slotID,
				"error", err.Error(),
			)
			return
		}
		s.logger.Info("llm_proxy_slot_released",
			"correlation_id", corrID,
			"slot", slotID,
		)
	}
	defer release()

	// Dispatch the LLM request over messaging.
	llmReplyStream := fmt.Sprintf("stream:llm-reply:%s:%d", s.cfg.Identity, time.Now().UnixNano())
	llmCorrID, err := s.publishLLMRequest(ctx, slotID, llmReplyStream, req)
	if err != nil {
		s.logger.Error("llm_proxy_request_publish_failed",
			"correlation_id", corrID,
			"error", err.Error(),
		)
		writeJSONError(w, http.StatusBadGateway, "publish_error", err.Error())
		return
	}

	group := fmt.Sprintf("cg:llm-reply:%s", llmCorrID)
	consumer := "llm-proxy"
	if err := s.client.EnsureGroup(ctx, llmReplyStream, group); err != nil {
		s.logger.Error("llm_proxy_reply_group_ensure_failed",
			"correlation_id", corrID,
			"error", err.Error(),
		)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	// Stream or aggregate.
	if req.Stream {
		s.streamResponse(ctx, w, r, corrID, llmReplyStream, group, consumer, req.Model)
	} else {
		s.aggregateResponse(ctx, w, corrID, llmReplyStream, group, consumer, req.Model)
	}

	// Correlation-aware reply stream cleanup is handled by valkey-level TTL; the group is ephemeral.
	_ = replyStream
	_ = tcString
}

// acquireSlot publishes a hindsight-class slot request and waits for the broker's assignment.
func (s *Server) acquireSlot(ctx context.Context, parentCorrID string) (int, string, string, error) {
	replyStream := fmt.Sprintf("stream:reply:%s:%d", s.cfg.Identity, time.Now().UnixNano())
	slotMsg, err := messaging.NewMessage(messaging.TypeSlotRequest, s.cfg.Identity, messaging.SlotRequestPayload{
		AgentID:   s.cfg.Identity,
		Priority:  0,
		SlotClass: string(broker.SlotClassHindsight),
	})
	if err != nil {
		return -1, "", "", err
	}
	slotMsg.ReplyStream = replyStream

	if _, err := s.client.Publish(ctx, slotRequestStream, slotMsg); err != nil {
		return -1, slotMsg.CorrelationID, replyStream, err
	}

	waitCtx, cancel := context.WithTimeout(ctx, s.cfg.SlotTimeout)
	defer cancel()

	reply, err := s.client.WaitForReply(waitCtx, replyStream, slotMsg.CorrelationID, s.cfg.SlotTimeout)
	if err != nil {
		return -1, slotMsg.CorrelationID, replyStream, err
	}

	var payload messaging.SlotAssignedPayload
	if err := reply.DecodePayload(&payload); err != nil {
		return -1, slotMsg.CorrelationID, replyStream, err
	}

	ackMsg, err := messaging.NewMessage(messaging.TypeSlotAssignedAck, s.cfg.Identity, messaging.SlotAssignedAckPayload{
		AgentID: s.cfg.Identity,
		SlotID:  payload.SlotID,
	})
	if err != nil {
		return -1, slotMsg.CorrelationID, replyStream, err
	}
	ackMsg.CorrelationID = slotMsg.CorrelationID
	if _, err := s.client.Publish(ctx, slotRequestStream, ackMsg); err != nil {
		return -1, slotMsg.CorrelationID, replyStream, err
	}

	return payload.SlotID, slotMsg.CorrelationID, replyStream, nil
}

func (s *Server) defensiveRelease(ctx context.Context, parentCorrID string) {
	relCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := messaging.NewMessage(messaging.TypeSlotRelease, s.cfg.Identity, messaging.SlotReleasePayload{
		AgentID: s.cfg.Identity,
		SlotID:  -1,
	})
	if err != nil {
		return
	}
	if _, err := s.client.Publish(relCtx, slotRequestStream, msg); err != nil {
		s.logger.Error("llm_proxy_defensive_release_failed",
			"correlation_id", parentCorrID,
			"error", err.Error(),
		)
	}
}

func (s *Server) publishLLMRequest(ctx context.Context, slotID int, replyStream string, req openAIChatRequest) (string, error) {
	reqMsg, err := messaging.NewMessage(messaging.TypeChatRequest, s.cfg.Identity, messaging.LLMRequestPayload{
		SlotID:     slotID,
		SlotClass:  string(broker.SlotClassHindsight),
		Messages:   req.Messages,
		Stream:     req.Stream,
		Tools:      req.Tools,
		ToolChoice: decodeToolChoice(req.ToolChoice),
	})
	if err != nil {
		return "", err
	}
	reqMsg.ReplyStream = replyStream
	if _, err := s.client.Publish(ctx, llmRequestStream, reqMsg); err != nil {
		return "", err
	}
	return reqMsg.CorrelationID, nil
}

// aggregateResponse consumes tokens and tool calls from the broker's reply stream,
// assembles a single OpenAI-compatible JSON response, and writes it.
func (s *Server) aggregateResponse(ctx context.Context, w http.ResponseWriter, corrID, replyStream, group, consumer, model string) {
	content, toolCalls, err := s.consumeReply(ctx, replyStream, group, consumer)
	if err != nil {
		s.logger.Error("llm_proxy_aggregate_failed",
			"correlation_id", corrID,
			"error", err.Error(),
		)
		writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	resp := openAIChatResponse{
		ID:      "chatcmpl-" + corrID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openAIChoice{
			{
				Index: 0,
				Message: openAIMessage{
					Role:      "assistant",
					Content:   content,
					ToolCalls: toolCalls,
				},
				FinishReason: finishReason(toolCalls),
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// streamResponse consumes the reply stream and emits OpenAI-compatible SSE chunks.
func (s *Server) streamResponse(ctx context.Context, w http.ResponseWriter, r *http.Request, corrID, replyStream, group, consumer, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	chunkID := "chatcmpl-" + corrID
	created := time.Now().Unix()

	emitChunk := func(delta openAIStreamDelta, finish *string) bool {
		chunk := openAIStreamChunk{
			ID:      chunkID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []openAIStreamChoice{{
				Index:        0,
				Delta:        delta,
				FinishReason: finish,
			}},
		}
		data, err := json.Marshal(chunk)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// emit role on first chunk, then content fragments, then tool calls (single-shot), then done.
	if !emitChunk(openAIStreamDelta{Role: "assistant"}, nil) {
		return
	}

	var toolCalls []messaging.ToolCall
	var mu sync.Mutex

	onToken := func(tok string) {
		emitChunk(openAIStreamDelta{Content: tok}, nil)
	}
	onTool := func(call messaging.ToolCall) {
		mu.Lock()
		toolCalls = append(toolCalls, call)
		mu.Unlock()
	}

	err := s.consumeReplyWithCallbacks(ctx, r, replyStream, group, consumer, onToken, onTool)
	if err != nil {
		s.logger.Error("llm_proxy_stream_failed",
			"correlation_id", corrID,
			"error", err.Error(),
		)
		// Can't change HTTP status at this point; emit a final SSE chunk with a synthetic finish.
	}

	mu.Lock()
	calls := toolCalls
	mu.Unlock()

	if len(calls) > 0 {
		// Emit tool calls in a single chunk, as OpenAI does when they're finalized.
		stop := "tool_calls"
		emitChunk(openAIStreamDelta{ToolCalls: calls}, &stop)
	} else {
		stop := "stop"
		emitChunk(openAIStreamDelta{}, &stop)
	}

	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// consumeReply pulls tokens and tool calls from the broker reply stream until
// a done-token arrives, the context ends, or an unrecoverable error occurs.
func (s *Server) consumeReply(ctx context.Context, stream, group, consumer string) (string, []messaging.ToolCall, error) {
	var content string
	var tools []messaging.ToolCall
	onTok := func(t string) { content += t }
	onTool := func(c messaging.ToolCall) { tools = append(tools, c) }
	err := s.consumeReplyWithCallbacks(ctx, nil, stream, group, consumer, onTok, onTool)
	return content, tools, err
}

// consumeReplyWithCallbacks drives the reply loop. If clientReq is non-nil,
// disconnection of its context cancels the loop.
func (s *Server) consumeReplyWithCallbacks(ctx context.Context, clientReq *http.Request, stream, group, consumer string, onToken func(string), onTool func(messaging.ToolCall)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if clientReq != nil {
			select {
			case <-clientReq.Context().Done():
				return clientReq.Context().Err()
			default:
			}
		}

		msgs, ids, err := s.client.ReadGroup(ctx, stream, group, consumer, 4, time.Second)
		if err != nil {
			// Retry transient read errors on the next iteration.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		for i, msg := range msgs {
			switch msg.Type {
			case messaging.TypeToolCall:
				var payload messaging.ToolCallPayload
				if err := msg.DecodePayload(&payload); err == nil {
					onTool(payload.Call)
				}
				_ = s.client.Ack(ctx, stream, group, ids[i])
			case messaging.TypeToken:
				var token messaging.TokenPayload
				if err := msg.DecodePayload(&token); err != nil {
					_ = s.client.Ack(ctx, stream, group, ids[i])
					continue
				}
				if token.Done {
					_ = s.client.Ack(ctx, stream, group, ids[i])
					return nil
				}
				if token.Token != "" {
					onToken(token.Token)
				}
				_ = s.client.Ack(ctx, stream, group, ids[i])
			default:
				_ = s.client.Ack(ctx, stream, group, ids[i])
			}
		}
	}
}

// decodeToolChoice flattens the OpenAI tool_choice shape to the string form
// the broker expects. "auto" and "none" pass through; object forms are serialized.
func decodeToolChoice(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func finishReason(tools []messaging.ToolCall) string {
	if len(tools) > 0 {
		return "tool_calls"
	}
	return "stop"
}

type errorBody struct {
	Error errorDetail `json:"error"`
}
type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorDetail{Code: code, Message: msg}})
}
