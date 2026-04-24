package context

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"microagent2/internal/messaging"
	"microagent2/internal/response"
)

type Manager struct {
	client       *messaging.Client
	responses    *response.Store
	muninn       *MuninnClient
	assembler    *Assembler
	logger       *slog.Logger
	recallLimit  int
	prewarmLimit int
}

func NewManager(client *messaging.Client, responses *response.Store, muninn *MuninnClient, assembler *Assembler, logger *slog.Logger, recallLimit, prewarmLimit int) *Manager {
	return &Manager{
		client:       client,
		responses:    responses,
		muninn:       muninn,
		assembler:    assembler,
		logger:       logger,
		recallLimit:  recallLimit,
		prewarmLimit: prewarmLimit,
	}
}

func (m *Manager) Run(ctx context.Context) error {
	group := messaging.ConsumerGroupContextManager
	consumer := "context-worker"
	return m.client.ConsumeStream(ctx, messaging.StreamGatewayRequests, group, consumer, 10, 2*time.Second,
		func(ctx context.Context, msg *messaging.Message) error {
			m.handleRequest(ctx, msg)
			return nil
		}, m.logger, nil)
}

func (m *Manager) handleRequest(ctx context.Context, msg *messaging.Message) {
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("context_handle_request_panic", "correlation_id", msg.CorrelationID, "panic", fmt.Sprint(r))
		}
	}()
	var payload messaging.ChatRequestPayload
	if err := msg.DecodePayload(&payload); err != nil {
		m.logger.Error("failed to decode chat request", "error", err)
		return
	}

	correlationID := msg.CorrelationID
	m.logger.Info("context_request_decoded",
		"correlation_id", correlationID,
		"session_id", payload.SessionID,
		"message_count", len(payload.Messages),
	)

	userMsg := payload.Messages[len(payload.Messages)-1]

	type historyResult struct {
		history []messaging.ChatMsg
		err     error
	}
	type memoryResult struct {
		memories []Memory
		err      error
		elapsed  time.Duration
	}

	histCh := make(chan historyResult, 1)
	memCh := make(chan memoryResult, 1)

	go func() {
		h, err := m.getSessionHistory(ctx, payload.SessionID)
		histCh <- historyResult{h, err}
	}()

	go func() {
		start := time.Now()
		mem, err := m.muninn.Recall(ctx, userMsg.Content, m.recallLimit)
		memCh <- memoryResult{mem, err, time.Since(start)}
	}()

	hr := <-histCh
	mr := <-memCh

	if hr.err != nil {
		m.logger.Error("failed to get session history", "correlation_id", correlationID, "error", hr.err, "session", payload.SessionID)
	} else {
		m.logger.Info("context_history_loaded",
			"correlation_id", correlationID,
			"session_id", payload.SessionID,
			"history_count", len(hr.history),
		)
	}
	if mr.err != nil {
		m.logger.Warn("context_muninn_recall",
			"correlation_id", correlationID,
			"elapsed_ms", mr.elapsed.Milliseconds(),
			"memory_count", 0,
			"outcome", "error",
			"error", mr.err.Error(),
		)
	} else {
		m.logger.Info("context_muninn_recall",
			"correlation_id", correlationID,
			"elapsed_ms", mr.elapsed.Milliseconds(),
			"memory_count", len(mr.memories),
			"outcome", "ok",
		)
	}

	assembled := m.assembler.Assemble(mr.memories, hr.history, userMsg)

	targetAgent := "main-agent"
	agentStream := fmt.Sprintf(messaging.StreamAgentRequests, targetAgent)
	replyStream := msg.ReplyStream

	contextMsg, err := messaging.NewMessage(messaging.TypeContextAssembled, "context-manager", messaging.ContextAssembledPayload{
		SessionID:   payload.SessionID,
		Messages:    assembled,
		TargetAgent: targetAgent,
		ReplyStream: replyStream,
	})
	if err != nil {
		m.logger.Error("failed to create context assembled message", "correlation_id", correlationID, "error", err)
		return
	}
	contextMsg.CorrelationID = correlationID

	if _, err := m.client.Publish(ctx, agentStream, contextMsg); err != nil {
		m.logger.Error("context_publish_failed", "correlation_id", correlationID, "error", err)
		return
	}
	m.logger.Info("context_published",
		"correlation_id", correlationID,
		"session_id", payload.SessionID,
		"target_agent", targetAgent,
		"assembled_count", len(assembled),
	)

	go m.preWarmMemories(ctx, payload.SessionID)
}

func (m *Manager) getSessionHistory(ctx context.Context, sessionID string) ([]messaging.ChatMsg, error) {
	return m.responses.GetSessionMessages(ctx, sessionID)
}

func (m *Manager) preWarmMemories(ctx context.Context, sessionID string) {
	history, err := m.getSessionHistory(ctx, sessionID)
	if err != nil || len(history) == 0 {
		return
	}

	lastAssistant := ""
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			lastAssistant = history[i].Content
			break
		}
	}
	if lastAssistant == "" {
		return
	}

	_, err = m.muninn.Recall(ctx, lastAssistant, m.prewarmLimit)
	if err != nil {
		m.logger.Debug("pre-warm memory fetch failed", "error", err)
	}
}
