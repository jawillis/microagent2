package context

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"microagent2/internal/config"
	"microagent2/internal/memoryclient"
	"microagent2/internal/messaging"
	"microagent2/internal/response"
)

type Manager struct {
	client       *messaging.Client
	responses    *response.Store
	memory       *memoryclient.Client
	assembler    *Assembler
	logger       *slog.Logger
	recallLimit  int
	prewarmLimit int
	cfgStore     *config.Store
}

func NewManager(client *messaging.Client, responses *response.Store, memory *memoryclient.Client, assembler *Assembler, logger *slog.Logger, recallLimit, prewarmLimit int, cfgStore *config.Store) *Manager {
	return &Manager{
		client:       client,
		responses:    responses,
		memory:       memory,
		assembler:    assembler,
		logger:       logger,
		recallLimit:  recallLimit,
		prewarmLimit: prewarmLimit,
		cfgStore:     cfgStore,
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
		source  string
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
		canonical, err := m.getSessionHistory(ctx, payload.SessionID)
		if err == nil && len(canonical) > 0 {
			histCh <- historyResult{history: canonical, source: "store"}
			return
		}
		if err != nil {
			histCh <- historyResult{err: err}
			return
		}
		histCh <- historyResult{history: payload.Messages[:len(payload.Messages)-1], source: "payload"}
	}()

	go func() {
		start := time.Now()
		ctx := memoryclient.WithCorrelationID(ctx, correlationID)

		var memCfg config.MemoryConfig
		_ = m.cfgStore.Load(ctx, config.KeyMemory, &memCfg)

		recallReq := memoryclient.RecallRequest{
			Query: userMsg.Content,
			Limit: m.recallLimit,
		}

		switch memCfg.RecallDefaultSpeakerScope {
		case "primary":
			if payload.SpeakerID != "" && payload.SpeakerID != "unknown" {
				recallReq.SpeakerID = payload.SpeakerID
			} else if memCfg.PrimaryUserID != "" {
				recallReq.SpeakerID = memCfg.PrimaryUserID
			}
		case "explicit":
			if payload.SpeakerID == "" || payload.SpeakerID == "unknown" {
				m.logger.Warn("context_recall_skipped_no_speaker",
					"correlation_id", correlationID,
					"session_id", payload.SessionID,
				)
				memCh <- memoryResult{elapsed: time.Since(start)}
				return
			}
			recallReq.SpeakerID = payload.SpeakerID
		}

		resp, err := m.memory.Recall(ctx, recallReq)
		if err != nil {
			memCh <- memoryResult{err: err, elapsed: time.Since(start)}
			return
		}
		memories := make([]Memory, 0, len(resp.Memories))
		for _, ms := range resp.Memories {
			memories = append(memories, Memory{
				ID:      ms.ID,
				Content: ms.Content,
				Score:   ms.Score,
				Tags:    ms.Tags,
			})
		}
		memCh <- memoryResult{memories: memories, elapsed: time.Since(start)}
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
			"source", hr.source,
		)
	}
	if mr.err != nil {
		m.logger.Warn("context_memory_recall",
			"correlation_id", correlationID,
			"elapsed_ms", mr.elapsed.Milliseconds(),
			"memory_count", 0,
			"outcome", "error",
			"error", mr.err.Error(),
		)
	} else {
		m.logger.Info("context_memory_recall",
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
		SpeakerID:   payload.SpeakerID,
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

	go m.preWarmMemories(ctx, payload.SessionID, correlationID)
}

func (m *Manager) getSessionHistory(ctx context.Context, sessionID string) ([]messaging.ChatMsg, error) {
	return m.responses.GetSessionMessages(ctx, sessionID)
}

func (m *Manager) preWarmMemories(ctx context.Context, sessionID, correlationID string) {
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
	ctx = memoryclient.WithCorrelationID(ctx, correlationID)
	if _, err := m.memory.Recall(ctx, memoryclient.RecallRequest{
		Query: lastAssistant,
		Limit: m.prewarmLimit,
	}); err != nil {
		m.logger.Debug("pre-warm memory fetch failed", "error", err, "correlation_id", correlationID)
	}
}
