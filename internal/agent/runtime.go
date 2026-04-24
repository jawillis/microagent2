package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"microagent2/internal/messaging"
)

func toolCallFinalizeTimeout() time.Duration {
	if v := os.Getenv("TOOL_CALL_FINALIZE_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 2 * time.Second
}

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

func (r *Runtime) Execute(ctx context.Context, messages []messaging.ChatMsg, tools []messaging.ToolSchema, onToken func(string), onToolCall func(messaging.ToolCall)) (string, []messaging.ToolCall, error) {
	return r.ExecuteWithCorrelation(ctx, "", messages, tools, onToken, onToolCall)
}

func (r *Runtime) ExecuteWithCorrelation(ctx context.Context, parentCorrelationID string, messages []messaging.ChatMsg, tools []messaging.ToolSchema, onToken func(string), onToolCall func(messaging.ToolCall)) (string, []messaging.ToolCall, error) {
	r.mu.Lock()
	slotID := r.slotID
	r.progressLog = nil
	r.mu.Unlock()

	if slotID == -1 {
		return "", nil, messaging.ErrNoSlot
	}

	preemptCtx, preemptCancel := context.WithCancel(ctx)
	defer preemptCancel()

	if r.preemptible {
		go r.ListenForPreemption(preemptCtx)
	}

	replyStream := fmt.Sprintf("stream:llm-reply:%s:%d", r.agentID, time.Now().UnixNano())

	reqMsg, err := messaging.NewMessage(messaging.TypeChatRequest, r.agentID, messaging.LLMRequestPayload{
		SlotID:     slotID,
		Messages:   messages,
		Stream:     true,
		Tools:      tools,
		ToolChoice: "",
	})
	if err != nil {
		return "", nil, err
	}
	reqMsg.ReplyStream = replyStream

	if _, err := r.client.Publish(ctx, "stream:broker:llm-requests", reqMsg); err != nil {
		return "", nil, err
	}
	r.logger.Info("llm_request_published",
		"correlation_id", parentCorrelationID,
		"slot", slotID,
		"llm_correlation_id", reqMsg.CorrelationID,
	)

	var result strings.Builder
	var toolCalls []messaging.ToolCall
	group := fmt.Sprintf("cg:llm-reply:%s", reqMsg.CorrelationID)
	consumer := "token-reader"

	if err := r.client.EnsureGroup(ctx, replyStream, group); err != nil {
		return "", nil, err
	}

	finalizeDeadline := time.Time{} // zero = not armed

	logObserved := func() {
		if len(toolCalls) == 0 {
			return
		}
		names := make([]string, 0, len(toolCalls))
		seen := map[string]bool{}
		for _, c := range toolCalls {
			if !seen[c.Function.Name] {
				names = append(names, c.Function.Name)
				seen[c.Function.Name] = true
			}
		}
		r.logger.Info("tool_calls_observed",
			"correlation_id", parentCorrelationID,
			"slot", slotID,
			"count", len(toolCalls),
			"names", names,
		)
	}

	for {
		if r.IsPreempted() && finalizeDeadline.IsZero() {
			finalizeDeadline = time.Now().Add(toolCallFinalizeTimeout())
		}

		if !finalizeDeadline.IsZero() && time.Now().After(finalizeDeadline) {
			preemptCancel()
			logObserved()
			return result.String(), toolCalls, messaging.ErrPreempted
		}

		msgs, ids, err := r.client.ReadGroup(ctx, replyStream, group, consumer, 1, time.Second)
		if err != nil {
			continue
		}

		for i, msg := range msgs {
			switch msg.Type {
			case messaging.TypeToolCall:
				var payload messaging.ToolCallPayload
				if err := msg.DecodePayload(&payload); err != nil {
					_ = r.client.Ack(ctx, replyStream, group, ids[i])
					continue
				}
				toolCalls = append(toolCalls, payload.Call)
				if onToolCall != nil {
					onToolCall(payload.Call)
				}
				_ = r.client.Ack(ctx, replyStream, group, ids[i])

			case messaging.TypeToken:
				var token messaging.TokenPayload
				if err := msg.DecodePayload(&token); err != nil {
					_ = r.client.Ack(ctx, replyStream, group, ids[i])
					continue
				}

				if token.Done {
					_ = r.client.Ack(ctx, replyStream, group, ids[i])
					logObserved()
					if !finalizeDeadline.IsZero() {
						return result.String(), toolCalls, messaging.ErrPreempted
					}
					return result.String(), toolCalls, nil
				}

				result.WriteString(token.Token)

				r.mu.Lock()
				r.progressLog = append(r.progressLog, token.Token)
				r.mu.Unlock()

				if onToken != nil {
					onToken(token.Token)
				}

				_ = r.client.Ack(ctx, replyStream, group, ids[i])

			default:
				_ = r.client.Ack(ctx, replyStream, group, ids[i])
			}
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
