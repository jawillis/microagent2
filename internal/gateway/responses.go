package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"microagent2/internal/messaging"
	"microagent2/internal/response"
)

type responsesRequest struct {
	Input              any     `json:"input"`
	Model              string  `json:"model"`
	PreviousResponseID string  `json:"previous_response_id,omitempty"`
	Tools              json.RawMessage `json:"tools,omitempty"`
	ToolChoice         json.RawMessage `json:"tool_choice,omitempty"`
	Stream             bool    `json:"stream"`
	Store              *bool   `json:"store,omitempty"`
}

type responsesResponse struct {
	ID                 string                `json:"id"`
	Object             string                `json:"object"`
	CreatedAt          int64                 `json:"created_at"`
	Model              string                `json:"model"`
	SessionID          string                `json:"session_id"`
	PreviousResponseID string                `json:"previous_response_id,omitempty"`
	Output             []response.OutputItem `json:"output"`
	Status             response.Status       `json:"status"`
}

func (s *Server) handleCreateResponse(w http.ResponseWriter, r *http.Request) {
	var req responsesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Failed to parse request body")
		return
	}

	inputItems, err := parseInput(req.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(inputItems) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "input is required and must not be empty")
		return
	}

	shouldStore := req.Store == nil || *req.Store

	decision, derr := s.decideSession(r.Context(), req.PreviousResponseID, shouldStore, inputItems)
	if derr != nil {
		writeError(w, derr.status, derr.code, derr.msg)
		return
	}
	sessionID := decision.SessionID
	effectivePrevRespID := decision.EffectivePrevRespID
	stitchPrefixHash := decision.StitchPrefixHash
	stitched := decision.Stitched

	var historyMsgs []messaging.ChatMsg
	if req.PreviousResponseID != "" {
		chain, err := s.responses.WalkChain(r.Context(), req.PreviousResponseID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("failed to resolve response chain: %s", err.Error()))
			return
		}
		historyMsgs = chainToMessages(chain)
	}

	currentMsgs := inputItemsToMessages(inputItems)
	allMsgs := append(historyMsgs, currentMsgs...)

	responseID := response.NewResponseID()
	correlationID := messaging.NewCorrelationID()
	replyStream := fmt.Sprintf("stream:gateway:reply:%s", correlationID)
	if stitchPrefixHash != "" {
		outcome := "minted"
		if stitched {
			outcome = "matched"
		}
		s.logger.Info("stitch_decision",
			"correlation_id", correlationID,
			"session_id", sessionID,
			"prefix_hash", stitchPrefixHash[:8],
			"outcome", outcome,
			"previous_response_id", effectivePrevRespID,
		)
	}

	msg, err := messaging.NewMessage(messaging.TypeChatRequest, "gateway", messaging.ChatRequestPayload{
		SessionID: sessionID,
		Messages:  allMsgs,
		Model:     req.Model,
		Stream:    req.Stream,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create message")
		return
	}
	msg.CorrelationID = correlationID
	msg.ReplyStream = replyStream

	s.logger.Info("gateway_request_received",
		"correlation_id", correlationID,
		"path", r.URL.Path,
		"session_id", sessionID,
		"previous_response_id", req.PreviousResponseID,
		"stream", req.Stream,
		"input_items", len(inputItems),
	)

	publishStart := time.Now()
	if _, err := s.client.Publish(r.Context(), messaging.StreamGatewayRequests, msg); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to publish request")
		return
	}
	s.logger.Info("gateway_request_published",
		"correlation_id", correlationID,
		"session_id", sessionID,
	)

	w.Header().Set("X-Session-ID", sessionID)

	if req.Stream {
		s.handleResponsesStreaming(r.Context(), w, responseID, sessionID, correlationID, req.Model, effectivePrevRespID, inputItems, shouldStore, publishStart)
	} else {
		s.handleResponsesNonStreaming(r.Context(), w, replyStream, responseID, sessionID, correlationID, req.Model, effectivePrevRespID, inputItems, shouldStore, publishStart)
	}
}

func (s *Server) handleResponsesNonStreaming(ctx context.Context, w http.ResponseWriter, replyStream, responseID, sessionID, correlationID, model, previousResponseID string, inputItems []response.InputItem, store bool, publishStart time.Time) {
	reply, err := s.client.WaitForReply(ctx, replyStream, correlationID, time.Duration(s.requestTimeoutS)*time.Second)
	if err != nil {
		s.logger.Warn("gateway_request_timeout", "correlation_id", correlationID, "session_id", sessionID, "elapsed_ms", time.Since(publishStart).Milliseconds())
		writeError(w, http.StatusGatewayTimeout, "timeout", "Request timed out waiting for response")
		return
	}

	var payload messaging.ChatResponsePayload
	if err := reply.DecodePayload(&payload); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to decode response")
		return
	}

	outputItems := textToOutputItems(payload.Content)

	if store {
		resp := &response.Response{
			ID:                 responseID,
			Input:              inputItems,
			Output:             outputItems,
			PreviousResponseID: previousResponseID,
			SessionID:          sessionID,
			Model:              model,
			CreatedAt:          time.Now().UTC().Format(time.RFC3339),
			Status:             response.StatusCompleted,
		}
		if err := s.responses.Save(ctx, resp); err != nil {
			s.logger.Error("failed to store response", "error", err, "response_id", responseID)
		} else {
			s.writeStitchIndex(ctx, sessionID, correlationID, inputItems, outputItems)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responsesResponse{
		ID:                 responseID,
		Object:             "response",
		CreatedAt:          time.Now().Unix(),
		Model:              model,
		SessionID:          sessionID,
		PreviousResponseID: previousResponseID,
		Output:             outputItems,
		Status:             response.StatusCompleted,
	})
	s.logger.Info("gateway_request_completed",
		"correlation_id", correlationID,
		"session_id", sessionID,
		"response_id", responseID,
		"elapsed_ms", time.Since(publishStart).Milliseconds(),
	)
}

func (s *Server) handleResponsesStreaming(ctx context.Context, w http.ResponseWriter, responseID, sessionID, correlationID, model, previousResponseID string, inputItems []response.InputItem, store bool, publishStart time.Time) {
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
	s.logger.Info("gateway_stream_subscribed", "correlation_id", correlationID, "session_id", sessionID)

	// Emit response.created first so clients can capture the id immediately
	// and thread subsequent turns via previous_response_id.
	createdEvent := responsesResponse{
		ID:                 responseID,
		Object:             "response",
		CreatedAt:          time.Now().Unix(),
		Model:              model,
		SessionID:          sessionID,
		PreviousResponseID: previousResponseID,
		Output:             []response.OutputItem{},
		Status:             response.StatusInProgress,
	}
	if data, err := json.Marshal(map[string]any{
		"type":     "response.created",
		"response": createdEvent,
	}); err == nil {
		fmt.Fprintf(w, "event: response.created\ndata: %s\n\n", data)
		flusher.Flush()
	}

	var fullContent string
	firstTokenSeen := false

	for {
		select {
		case <-ctx.Done():
			s.logger.Warn("gateway_client_disconnected", "correlation_id", correlationID, "session_id", sessionID, "elapsed_ms", time.Since(publishStart).Milliseconds())
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

			if !firstTokenSeen && !token.Done {
				firstTokenSeen = true
				s.logger.Info("gateway_stream_first_token", "correlation_id", correlationID, "elapsed_ms_since_published", time.Since(publishStart).Milliseconds())
			}

			if token.Done {
				outputItems := textToOutputItems(fullContent)

				if store {
					resp := &response.Response{
						ID:                 responseID,
						Input:              inputItems,
						Output:             outputItems,
						PreviousResponseID: previousResponseID,
						SessionID:          sessionID,
						Model:              model,
						CreatedAt:          time.Now().UTC().Format(time.RFC3339),
						Status:             response.StatusCompleted,
					}
					if err := s.responses.Save(ctx, resp); err != nil {
						s.logger.Error("failed to store response", "error", err, "response_id", responseID)
					} else {
						s.writeStitchIndex(ctx, sessionID, correlationID, inputItems, outputItems)
					}
				}

				doneEvent := responsesResponse{
					ID:                 responseID,
					Object:             "response",
					CreatedAt:          time.Now().Unix(),
					Model:              model,
					SessionID:          sessionID,
					PreviousResponseID: previousResponseID,
					Output:             outputItems,
					Status:             response.StatusCompleted,
				}
				data, _ := json.Marshal(map[string]any{
					"type":     "response.completed",
					"response": doneEvent,
				})
				fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", data)
				flusher.Flush()
				s.logger.Info("gateway_request_completed",
					"correlation_id", correlationID,
					"session_id", sessionID,
					"response_id", responseID,
					"elapsed_ms", time.Since(publishStart).Milliseconds(),
				)
				return
			}

			fullContent += token.Token

			deltaData, _ := json.Marshal(map[string]any{
				"type":  "response.output_text.delta",
				"delta": token.Token,
			})
			fmt.Fprintf(w, "event: response.output_text.delta\ndata: %s\n\n", deltaData)
			flusher.Flush()
		}
	}
}

func (s *Server) handleGetResponse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	resp, err := s.responses.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve response")
		return
	}
	if resp == nil {
		writeError(w, http.StatusNotFound, "not_found", "Response not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responsesResponse{
		ID:                 resp.ID,
		Object:             "response",
		CreatedAt:          parseTimestamp(resp.CreatedAt),
		Model:              resp.Model,
		SessionID:          resp.SessionID,
		PreviousResponseID: resp.PreviousResponseID,
		Output:             resp.Output,
		Status:             resp.Status,
	})
}

func parseInput(raw any) ([]response.InputItem, error) {
	switch v := raw.(type) {
	case string:
		return []response.InputItem{{
			Type:    "message",
			Role:    "user",
			Content: v,
		}}, nil
	case []any:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("invalid input format")
		}
		var items []response.InputItem
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("invalid input items format")
		}
		return items, nil
	default:
		return nil, fmt.Errorf("input must be a string or array")
	}
}

// sessionDecision is the result of picking a session_id (and the
// previous_response_id the new response will carry) for an incoming
// /v1/responses call. Produced by decideSession; consumed by the
// two request handlers.
type sessionDecision struct {
	SessionID           string
	EffectivePrevRespID string
	StitchPrefixHash    string // set only when stitching was attempted
	Stitched            bool   // true if the hash index hit
}

type handlerErr struct {
	status int
	code   string
	msg    string
}

// decideSession picks the session_id and previous_response_id for this turn.
//
//  1. req.PreviousResponseID set -> inherit session from that response.
//  2. PreviousResponseID empty, store=true, len(inputItems) > 1 ->
//     client-side-state replay. Try to stitch via hash of inputItems[:-1].
//  3. Else -> mint a fresh session_id.
func (s *Server) decideSession(ctx context.Context, prevRespID string, shouldStore bool, inputItems []response.InputItem) (sessionDecision, *handlerErr) {
	if prevRespID != "" {
		sid, err := s.responses.InheritSessionID(ctx, prevRespID)
		if err != nil {
			return sessionDecision{}, &handlerErr{
				status: http.StatusBadRequest,
				code:   "invalid_request",
				msg:    fmt.Sprintf("previous_response_id not found: %s", prevRespID),
			}
		}
		return sessionDecision{SessionID: sid, EffectivePrevRespID: prevRespID}, nil
	}

	if shouldStore && len(inputItems) > 1 {
		hashHex := response.StitchHash(inputItems[:len(inputItems)-1])
		sid, ok, err := s.responses.LookupSessionByPrefixHash(ctx, hashHex)
		if err != nil {
			s.logger.Warn("stitch_lookup_failed", "error", err.Error(), "prefix_hash", hashHex[:8])
			return sessionDecision{SessionID: response.NewSessionID(), StitchPrefixHash: hashHex}, nil
		}
		if ok {
			prev, _ := s.responses.GetLastResponseID(ctx, sid)
			return sessionDecision{
				SessionID:           sid,
				EffectivePrevRespID: prev,
				StitchPrefixHash:    hashHex,
				Stitched:            true,
			}, nil
		}
		return sessionDecision{SessionID: response.NewSessionID(), StitchPrefixHash: hashHex}, nil
	}

	return sessionDecision{SessionID: response.NewSessionID()}, nil
}

// writeStitchIndex stores the canonical hash of the full turn
// (inputItems + the assistant outputItems) as an index entry pointing
// at sessionID, so the next client-side-state replay can stitch back to
// the same session. Failure is logged but non-fatal.
func (s *Server) writeStitchIndex(ctx context.Context, sessionID, correlationID string, inputItems []response.InputItem, outputItems []response.OutputItem) {
	full := make([]response.InputItem, 0, len(inputItems)+len(outputItems))
	full = append(full, inputItems...)
	for _, out := range outputItems {
		full = append(full, response.OutputItemToInputItem(out))
	}
	hashHex := response.StitchHash(full)
	if err := s.responses.StoreSessionPrefixHash(ctx, hashHex, sessionID); err != nil {
		s.logger.Warn("stitch_index_write_failed",
			"correlation_id", correlationID,
			"session_id", sessionID,
			"error", err.Error(),
		)
		return
	}
	s.logger.Info("stitch_index_wrote",
		"correlation_id", correlationID,
		"session_id", sessionID,
		"prefix_hash", hashHex[:8],
	)
}

func inputItemsToMessages(items []response.InputItem) []messaging.ChatMsg {
	var msgs []messaging.ChatMsg
	for _, item := range items {
		role := item.Role
		if role == "" {
			role = "user"
		}
		msgs = append(msgs, messaging.ChatMsg{Role: role, Content: response.FlattenContent(item.Content)})
	}
	return msgs
}

func chainToMessages(chain []*response.Response) []messaging.ChatMsg {
	var msgs []messaging.ChatMsg
	for _, resp := range chain {
		msgs = append(msgs, inputItemsToMessages(resp.Input)...)
		for _, out := range resp.Output {
			if out.Type == "message" && out.Role == "assistant" {
				var text string
				for _, part := range out.Content {
					if part.Type == "output_text" || part.Type == "text" {
						text += part.Text
					}
				}
				if text != "" {
					msgs = append(msgs, messaging.ChatMsg{Role: "assistant", Content: text})
				}
			}
		}
	}
	return msgs
}

func textToOutputItems(content string) []response.OutputItem {
	return []response.OutputItem{{
		Type: "message",
		Role: "assistant",
		Content: []response.ContentPart{{
			Type: "output_text",
			Text: content,
		}},
	}}
}

func messagesToInputItems(msgs []openAIMsg) []response.InputItem {
	items := make([]response.InputItem, len(msgs))
	for i, m := range msgs {
		items[i] = response.InputItem{
			Type:    "message",
			Role:    m.Role,
			Content: m.Content,
		}
	}
	return items
}

func parseTimestamp(ts string) int64 {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Now().Unix()
	}
	return t.Unix()
}
