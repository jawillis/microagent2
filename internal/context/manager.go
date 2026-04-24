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

	if err := m.client.EnsureGroup(ctx, messaging.StreamGatewayRequests, group); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msgs, ids, err := m.client.ReadGroup(ctx, messaging.StreamGatewayRequests, group, consumer, 10, 2*time.Second)
		if err != nil {
			continue
		}

		for i, msg := range msgs {
			m.handleRequest(ctx, msg)
			_ = m.client.Ack(ctx, messaging.StreamGatewayRequests, group, ids[i])
		}
	}
}

func (m *Manager) handleRequest(ctx context.Context, msg *messaging.Message) {
	var payload messaging.ChatRequestPayload
	if err := msg.DecodePayload(&payload); err != nil {
		m.logger.Error("failed to decode chat request", "error", err)
		return
	}

	userMsg := payload.Messages[len(payload.Messages)-1]

	type historyResult struct {
		history []messaging.ChatMsg
		err     error
	}
	type memoryResult struct {
		memories []Memory
		err      error
	}

	histCh := make(chan historyResult, 1)
	memCh := make(chan memoryResult, 1)

	go func() {
		h, err := m.getSessionHistory(ctx, payload.SessionID)
		histCh <- historyResult{h, err}
	}()

	go func() {
		mem, err := m.muninn.Recall(ctx, userMsg.Content, m.recallLimit)
		memCh <- memoryResult{mem, err}
	}()

	hr := <-histCh
	mr := <-memCh

	if hr.err != nil {
		m.logger.Error("failed to get session history", "error", hr.err, "session", payload.SessionID)
	}
	if mr.err != nil {
		m.logger.Warn("failed to recall memories, proceeding without", "error", mr.err)
	}

	assembled := m.assembler.Assemble(mr.memories, hr.history, userMsg)

	agentStream := fmt.Sprintf(messaging.StreamAgentRequests, "main-agent")
	replyStream := msg.ReplyStream

	contextMsg, err := messaging.NewMessage(messaging.TypeContextAssembled, "context-manager", messaging.ContextAssembledPayload{
		SessionID:   payload.SessionID,
		Messages:    assembled,
		TargetAgent: "main-agent",
		ReplyStream: replyStream,
	})
	if err != nil {
		m.logger.Error("failed to create context assembled message", "error", err)
		return
	}
	contextMsg.CorrelationID = msg.CorrelationID

	if _, err := m.client.Publish(ctx, agentStream, contextMsg); err != nil {
		m.logger.Error("failed to publish to agent stream", "error", err)
	}

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
