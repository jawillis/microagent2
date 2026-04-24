package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"microagent2/internal/messaging"
)

type Runtime struct {
	client      *messaging.Client
	agentID     string
	priority    int
	preemptible bool
	logger      *slog.Logger

	mu          sync.Mutex
	progressLog []string
	slotID      int
	preempted   bool
}

func NewRuntime(client *messaging.Client, agentID string, priority int, preemptible bool, logger *slog.Logger) *Runtime {
	return &Runtime{
		client:      client,
		agentID:     agentID,
		priority:    priority,
		preemptible: preemptible,
		logger:      logger,
		slotID:      -1,
	}
}

func (r *Runtime) RequestSlot(ctx context.Context) (int, error) {
	return r.RequestSlotWithCorrelation(ctx, "")
}

func (r *Runtime) RequestSlotWithCorrelation(ctx context.Context, parentCorrelationID string) (int, error) {
	start := time.Now()
	replyStream := fmt.Sprintf("stream:reply:%s:%d", r.agentID, time.Now().UnixNano())

	msg, err := messaging.NewMessage(messaging.TypeSlotRequest, r.agentID, messaging.SlotRequestPayload{
		AgentID:  r.agentID,
		Priority: r.priority,
	})
	if err != nil {
		return -1, err
	}
	msg.ReplyStream = replyStream

	_, err = r.client.Publish(ctx, "stream:broker:slot-requests", msg)
	if err != nil {
		return -1, err
	}

	reply, err := r.client.WaitForReply(ctx, replyStream, msg.CorrelationID, 30*time.Second)
	if err != nil {
		r.logger.Warn("slot_request_result",
			"correlation_id", parentCorrelationID,
			"slot_correlation_id", msg.CorrelationID,
			"outcome", "timeout",
			"elapsed_ms", time.Since(start).Milliseconds(),
			"error", err.Error(),
		)
		r.defensiveRelease(ctx, parentCorrelationID, msg.CorrelationID)
		return -1, err
	}

	var payload messaging.SlotAssignedPayload
	if err := reply.DecodePayload(&payload); err != nil {
		r.logger.Error("slot_request_result",
			"correlation_id", parentCorrelationID,
			"slot_correlation_id", msg.CorrelationID,
			"outcome", "decode_error",
			"error", err.Error(),
		)
		r.defensiveRelease(ctx, parentCorrelationID, msg.CorrelationID)
		return -1, err
	}

	ackMsg, err := messaging.NewMessage(messaging.TypeSlotAssignedAck, r.agentID, messaging.SlotAssignedAckPayload{
		AgentID: r.agentID,
		SlotID:  payload.SlotID,
	})
	if err != nil {
		r.defensiveRelease(ctx, parentCorrelationID, msg.CorrelationID)
		return -1, err
	}
	ackMsg.CorrelationID = msg.CorrelationID
	if _, err := r.client.Publish(ctx, "stream:broker:slot-requests", ackMsg); err != nil {
		r.logger.Error("slot_ack_publish_failed",
			"correlation_id", parentCorrelationID,
			"slot_correlation_id", msg.CorrelationID,
			"slot", payload.SlotID,
			"error", err.Error(),
		)
		r.defensiveRelease(ctx, parentCorrelationID, msg.CorrelationID)
		return -1, err
	}

	r.mu.Lock()
	r.slotID = payload.SlotID
	r.preempted = false
	r.mu.Unlock()

	r.logger.Info("slot_request_result",
		"correlation_id", parentCorrelationID,
		"slot_correlation_id", msg.CorrelationID,
		"outcome", "acquired",
		"slot", payload.SlotID,
		"elapsed_ms", time.Since(start).Milliseconds(),
	)
	return payload.SlotID, nil
}

func (r *Runtime) defensiveRelease(ctx context.Context, parentCorrelationID, slotCorrelationID string) {
	relMsg, err := messaging.NewMessage(messaging.TypeSlotRelease, r.agentID, messaging.SlotReleasePayload{
		AgentID: r.agentID,
		SlotID:  -1,
	})
	if err != nil {
		return
	}
	if _, err := r.client.Publish(ctx, "stream:broker:slot-requests", relMsg); err != nil {
		r.logger.Error("defensive_release_publish_failed",
			"correlation_id", parentCorrelationID,
			"slot_correlation_id", slotCorrelationID,
			"error", err.Error(),
		)
		return
	}
	r.logger.Warn("defensive_release_sent",
		"correlation_id", parentCorrelationID,
		"slot_correlation_id", slotCorrelationID,
	)
}

func (r *Runtime) ReleaseSlot(ctx context.Context) error {
	return r.ReleaseSlotWithCorrelation(ctx, "")
}

func (r *Runtime) ReleaseSlotWithCorrelation(ctx context.Context, parentCorrelationID string) error {
	r.mu.Lock()
	slotID := r.slotID
	r.slotID = -1
	r.mu.Unlock()

	if slotID == -1 {
		return nil
	}

	msg, err := messaging.NewMessage(messaging.TypeSlotRelease, r.agentID, messaging.SlotReleasePayload{
		AgentID: r.agentID,
		SlotID:  slotID,
	})
	if err != nil {
		return err
	}
	_, err = r.client.Publish(ctx, "stream:broker:slot-requests", msg)
	if err != nil {
		r.logger.Error("slot_released",
			"correlation_id", parentCorrelationID,
			"slot", slotID,
			"outcome", "publish_error",
			"error", err.Error(),
		)
		return err
	}
	r.logger.Info("slot_released",
		"correlation_id", parentCorrelationID,
		"slot", slotID,
		"outcome", "ok",
	)
	return nil
}

func (r *Runtime) ListenForPreemption(ctx context.Context) {
	channel := fmt.Sprintf(messaging.ChannelPreempt, r.agentID)
	sub := r.client.PubSubSubscribe(ctx, channel)
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case redisMsg := <-sub.Channel():
			if redisMsg == nil {
				continue
			}
			r.mu.Lock()
			r.preempted = true
			r.mu.Unlock()
			r.logger.Warn("preemption signal received")

			ackMsg, err := messaging.NewMessage(messaging.TypePreemptAck, r.agentID, nil)
			if err == nil {
				_ = r.client.PubSubPublish(ctx, channel, ackMsg)
			}
			return
		}
	}
}

func (r *Runtime) IsPreempted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.preempted
}

func (r *Runtime) Execute(ctx context.Context, messages []messaging.ChatMsg, onToken func(string)) (string, error) {
	return r.ExecuteWithCorrelation(ctx, "", messages, onToken)
}

func (r *Runtime) ExecuteWithCorrelation(ctx context.Context, parentCorrelationID string, messages []messaging.ChatMsg, onToken func(string)) (string, error) {
	r.mu.Lock()
	slotID := r.slotID
	r.progressLog = nil
	r.mu.Unlock()

	if slotID == -1 {
		return "", messaging.ErrNoSlot
	}

	preemptCtx, preemptCancel := context.WithCancel(ctx)
	defer preemptCancel()

	if r.preemptible {
		go r.ListenForPreemption(preemptCtx)
	}

	replyStream := fmt.Sprintf("stream:llm-reply:%s:%d", r.agentID, time.Now().UnixNano())

	reqMsg, err := messaging.NewMessage(messaging.TypeChatRequest, r.agentID, messaging.LLMRequestPayload{
		SlotID:   slotID,
		Messages: messages,
		Stream:   true,
	})
	if err != nil {
		return "", err
	}
	reqMsg.ReplyStream = replyStream

	if _, err := r.client.Publish(ctx, "stream:broker:llm-requests", reqMsg); err != nil {
		return "", err
	}
	r.logger.Info("llm_request_published",
		"correlation_id", parentCorrelationID,
		"slot", slotID,
		"llm_correlation_id", reqMsg.CorrelationID,
	)

	var result strings.Builder
	group := fmt.Sprintf("cg:llm-reply:%s", reqMsg.CorrelationID)
	consumer := "token-reader"

	if err := r.client.EnsureGroup(ctx, replyStream, group); err != nil {
		return "", err
	}

	for {
		if r.IsPreempted() {
			preemptCancel()
			return result.String(), messaging.ErrPreempted
		}

		msgs, ids, err := r.client.ReadGroup(ctx, replyStream, group, consumer, 1, time.Second)
		if err != nil {
			continue
		}

		for i, msg := range msgs {
			var token messaging.TokenPayload
			if err := msg.DecodePayload(&token); err != nil {
				continue
			}

			if token.Done {
				_ = r.client.Ack(ctx, replyStream, group, ids[i])
				return result.String(), nil
			}

			result.WriteString(token.Token)

			r.mu.Lock()
			r.progressLog = append(r.progressLog, token.Token)
			r.mu.Unlock()

			if onToken != nil {
				onToken(token.Token)
			}

			_ = r.client.Ack(ctx, replyStream, group, ids[i])
		}
	}
}

func (r *Runtime) GetProgressLog() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	log := make([]string, len(r.progressLog))
	copy(log, r.progressLog)
	return log
}

func (r *Runtime) BuildResumptionContext(originalMessages []messaging.ChatMsg) []messaging.ChatMsg {
	partialOutput := strings.Join(r.GetProgressLog(), "")
	if partialOutput == "" {
		return originalMessages
	}

	resumed := make([]messaging.ChatMsg, len(originalMessages))
	copy(resumed, originalMessages)
	resumed = append(resumed, messaging.ChatMsg{
		Role:    "assistant",
		Content: partialOutput,
	})
	return resumed
}
