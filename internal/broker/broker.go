package broker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"microagent2/internal/messaging"
	"microagent2/internal/registry"
)

type Broker struct {
	client             *messaging.Client
	slots              *SlotTable
	registry           *registry.Registry
	regConsumer        *registry.RegistryConsumer
	logger             *slog.Logger
	llamaAddr          string
	llamaAPIKey        string
	model              string
	preemptTimeout     time.Duration
	provisionalTimeout time.Duration
	snapshotInterval   time.Duration

	mu           sync.Mutex
	pendingQueue []slotRequest
}

type slotRequest struct {
	agentID       string
	priority      int
	correlationID string
	replyStream   string
	class         SlotClass
}

func New(client *messaging.Client, reg *registry.Registry, logger *slog.Logger, llamaAddr, llamaAPIKey, model string, slotCount int, preemptTimeout, provisionalTimeout, snapshotInterval time.Duration) *Broker {
	return NewWithClasses(client, reg, logger, llamaAddr, llamaAPIKey, model, slotCount, 0, preemptTimeout, provisionalTimeout, snapshotInterval)
}

func NewWithClasses(client *messaging.Client, reg *registry.Registry, logger *slog.Logger, llamaAddr, llamaAPIKey, model string, agentSlotCount, hindsightSlotCount int, preemptTimeout, provisionalTimeout, snapshotInterval time.Duration) *Broker {
	if provisionalTimeout <= 0 {
		provisionalTimeout = 2 * time.Second
	}
	if snapshotInterval <= 0 {
		snapshotInterval = 30 * time.Second
	}
	b := &Broker{
		client:             client,
		slots:              NewSlotTableWithClasses(agentSlotCount, hindsightSlotCount),
		registry:           reg,
		logger:             logger,
		llamaAddr:          llamaAddr,
		llamaAPIKey:        llamaAPIKey,
		model:              model,
		preemptTimeout:     preemptTimeout,
		provisionalTimeout: provisionalTimeout,
		snapshotInterval:   snapshotInterval,
	}

	b.regConsumer = registry.NewRegistryConsumer(client, reg, logger, b.handleDeadAgent)

	return b
}

func (b *Broker) Run(ctx context.Context) error {
	go b.regConsumer.RunRegistrationConsumer(ctx)
	go b.regConsumer.RunHeartbeatMonitor(ctx)
	go b.consumeLLMRequests(ctx)
	go b.consumeSnapshotRequests(ctx)
	go b.runSnapshotLogger(ctx)
	return b.consumeSlotRequests(ctx)
}

// consumeSnapshotRequests serves on-demand slot snapshots requested
// via messaging (e.g. by the gateway's /v1/broker/slots endpoint).
// Each request's reply stream receives a SlotSnapshotResponse payload.
func (b *Broker) consumeSnapshotRequests(ctx context.Context) {
	group := messaging.ConsumerGroupBrokerSnapshot
	consumer := "slot-snapshot"
	err := b.client.ConsumeStream(ctx, messaging.StreamBrokerSlotSnapshot, group, consumer, 10, 2*time.Second,
		func(ctx context.Context, msg *messaging.Message) error {
			if msg.Type != messaging.TypeSlotSnapshotRequest {
				return nil
			}
			snap := b.slots.Snapshot()
			entries := make([]messaging.SlotSnapshotEntry, len(snap))
			for i, s := range snap {
				entries[i] = messaging.SlotSnapshotEntry{
					SlotID:   s.SlotID,
					Class:    s.Class,
					State:    s.State,
					AgentID:  s.AgentID,
					Priority: s.Priority,
					AgeS:     s.AgeS,
				}
			}
			reply, rerr := messaging.NewReply(msg, messaging.TypeSlotSnapshotResponse, "llm-broker",
				messaging.SlotSnapshotResponsePayload{Slots: entries})
			if rerr != nil {
				return nil
			}
			if msg.ReplyStream != "" {
				_, _ = b.client.Publish(ctx, msg.ReplyStream, reply)
			}
			return nil
		}, b.logger, nil)
	if err != nil && err != context.Canceled {
		b.logger.Error("slot_snapshot_consumer_exit", "error", err.Error())
	}
}

func (b *Broker) runSnapshotLogger(ctx context.Context) {
	t := time.NewTicker(b.snapshotInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.logger.Info("slot_table_snapshot", "slots", b.slots.Snapshot())
		}
	}
}

func (b *Broker) consumeSlotRequests(ctx context.Context) error {
	stream := "stream:broker:slot-requests"
	group := messaging.ConsumerGroupBroker
	consumer := "slot-arbiter"
	return b.client.ConsumeStream(ctx, stream, group, consumer, 10, 2*time.Second,
		func(ctx context.Context, msg *messaging.Message) error {
			b.handleMessage(ctx, msg)
			return nil
		}, b.logger, nil)
}

func (b *Broker) handleMessage(ctx context.Context, msg *messaging.Message) {
	switch msg.Type {
	case messaging.TypeSlotRequest:
		var payload messaging.SlotRequestPayload
		if err := msg.DecodePayload(&payload); err != nil {
			b.logger.Error("failed to decode slot request", "error", err)
			return
		}
		b.handleSlotRequest(ctx, msg, payload)

	case messaging.TypeSlotAssignedAck:
		var payload messaging.SlotAssignedAckPayload
		if err := msg.DecodePayload(&payload); err != nil {
			b.logger.Error("failed to decode slot assigned ack", "error", err)
			return
		}
		b.handleSlotAssignedAck(msg, payload)

	case messaging.TypeSlotRelease:
		var payload messaging.SlotReleasePayload
		if err := msg.DecodePayload(&payload); err != nil {
			b.logger.Error("failed to decode slot release", "error", err)
			return
		}
		b.handleSlotRelease(ctx, payload)
	}
}

func (b *Broker) handleSlotRequest(ctx context.Context, msg *messaging.Message, payload messaging.SlotRequestPayload) {
	class, ok := NormalizeClass(payload.SlotClass)
	if !ok {
		b.logger.Warn("unknown_slot_class", "slot_class", payload.SlotClass, "agent", payload.AgentID, "correlation_id", msg.CorrelationID)
		return
	}

	slotID, found := b.slots.FindUnassigned(class)
	if found {
		if !b.slots.AssignProvisional(slotID, payload.AgentID, payload.Priority, msg.CorrelationID) {
			b.logger.Warn("slot_assign_collision", "slot", slotID, "agent", payload.AgentID)
			// race: slot taken between find and assign; re-enqueue
			b.enqueue(msg, payload, class)
			return
		}
		b.logger.Info("slot_assigned_provisional", "slot", slotID, "class", class, "agent", payload.AgentID, "correlation_id", msg.CorrelationID)
		b.sendSlotAssigned(ctx, msg, slotID, payload.AgentID)
		b.scheduleReclaim(msg.CorrelationID)
		return
	}

	victimSlot, victimAgent, victimPriority, hasVictim := b.slots.FindLowestPriorityPreemptible(class, b)
	if hasVictim && payload.Priority < victimPriority {
		b.logger.Info("preempting agent", "victim", victimAgent, "slot", victimSlot, "class", class, "requester", payload.AgentID)
		b.preemptAgent(ctx, victimAgent, victimSlot, msg, payload)
		return
	}

	b.enqueue(msg, payload, class)
}

func (b *Broker) enqueue(msg *messaging.Message, payload messaging.SlotRequestPayload, class SlotClass) {
	b.mu.Lock()
	b.pendingQueue = append(b.pendingQueue, slotRequest{
		agentID:       payload.AgentID,
		priority:      payload.Priority,
		correlationID: msg.CorrelationID,
		replyStream:   msg.ReplyStream,
		class:         class,
	})
	b.mu.Unlock()
	b.logger.Info("slot request queued", "agent", payload.AgentID, "class", class, "correlation_id", msg.CorrelationID)
}

func (b *Broker) scheduleReclaim(correlationID string) {
	go func() {
		time.Sleep(b.provisionalTimeout)
		slotID, reverted := b.slots.RevertProvisional(correlationID)
		if !reverted {
			return
		}
		b.logger.Warn("slot_provisional_reclaimed", "slot", slotID, "correlation_id", correlationID)
		b.assignFromQueue(context.Background())
	}()
}

func (b *Broker) handleSlotAssignedAck(msg *messaging.Message, payload messaging.SlotAssignedAckPayload) {
	slotID, ok := b.slots.CommitAssignment(msg.CorrelationID)
	if !ok {
		b.logger.Warn("slot_assigned_ack_no_match", "correlation_id", msg.CorrelationID, "agent", payload.AgentID, "slot", payload.SlotID)
		return
	}
	b.logger.Info("slot_assigned_committed", "slot", slotID, "agent", payload.AgentID, "correlation_id", msg.CorrelationID)
}

func (b *Broker) handleSlotRelease(ctx context.Context, payload messaging.SlotReleasePayload) {
	if payload.SlotID == -1 {
		released := b.slots.ReleaseByAgent(payload.AgentID)
		if len(released) == 0 {
			b.logger.Info("slot_release_by_agent_noop", "agent", payload.AgentID)
			return
		}
		b.logger.Info("slot_released_by_agent", "agent", payload.AgentID, "slots", released)
		b.assignFromQueue(ctx)
		return
	}
	b.slots.Release(payload.SlotID)
	b.logger.Info("slot released", "slot", payload.SlotID, "agent", payload.AgentID)
	b.assignFromQueue(ctx)
}

func (b *Broker) preemptAgent(ctx context.Context, victimAgent string, slotID int, originalMsg *messaging.Message, requester messaging.SlotRequestPayload) {
	channel := fmt.Sprintf(messaging.ChannelPreempt, victimAgent)
	preemptMsg, err := messaging.NewMessage(messaging.TypePreempt, "llm-broker", messaging.PreemptPayload{
		Reason: fmt.Sprintf("higher priority agent %s (priority %d) needs slot", requester.AgentID, requester.Priority),
	})
	if err != nil {
		b.logger.Error("failed to create preempt message", "error", err)
		return
	}

	_ = b.client.PubSubPublish(ctx, channel, preemptMsg)

	go func() {
		time.Sleep(b.preemptTimeout)
		if _, ok := b.slots.GetByAgent(victimAgent); ok {
			b.logger.Warn("preempt timeout, force-releasing slot", "agent", victimAgent, "slot", slotID)
			b.slots.Release(slotID)
			b.registry.MarkDead(victimAgent)
		}
		b.slots.ForceAssign(slotID, requester.AgentID, requester.Priority)
		b.sendSlotAssigned(ctx, originalMsg, slotID, requester.AgentID)
	}()
}

func (b *Broker) sendSlotAssigned(ctx context.Context, original *messaging.Message, slotID int, agentID string) {
	if original.ReplyStream == "" {
		return
	}
	reply, err := messaging.NewReply(original, messaging.TypeSlotAssigned, "llm-broker", messaging.SlotAssignedPayload{SlotID: slotID})
	if err != nil {
		b.logger.Error("failed to create slot assigned reply", "error", err)
		return
	}
	if _, err := b.client.Publish(ctx, original.ReplyStream, reply); err != nil {
		b.logger.Error("slot_assigned_reply_failed", "error", err, "slot", slotID, "agent", agentID, "correlation_id", original.CorrelationID)
		if _, reverted := b.slots.RevertProvisional(original.CorrelationID); reverted {
			b.logger.Warn("slot_provisional_reverted_after_publish_fail", "slot", slotID, "correlation_id", original.CorrelationID)
			b.assignFromQueue(ctx)
		}
		return
	}
	b.logger.Info("slot_assigned_reply_published", "slot", slotID, "agent", agentID, "correlation_id", original.CorrelationID)
}

func (b *Broker) assignFromQueue(ctx context.Context) {
	b.mu.Lock()
	if len(b.pendingQueue) == 0 {
		b.mu.Unlock()
		return
	}

	// Find the best (lowest priority value) queued request that has a free slot of its class.
	bestIdx := -1
	var chosenSlot int
	for i, req := range b.pendingQueue {
		sid, found := b.slots.FindUnassigned(req.class)
		if !found {
			continue
		}
		if bestIdx == -1 || req.priority < b.pendingQueue[bestIdx].priority {
			bestIdx = i
			chosenSlot = sid
		}
	}
	if bestIdx == -1 {
		b.mu.Unlock()
		return
	}
	slotID := chosenSlot

	req := b.pendingQueue[bestIdx]
	b.pendingQueue = append(b.pendingQueue[:bestIdx], b.pendingQueue[bestIdx+1:]...)
	b.mu.Unlock()

	if !b.slots.AssignProvisional(slotID, req.agentID, req.priority, req.correlationID) {
		b.logger.Warn("slot_assign_collision", "slot", slotID, "agent", req.agentID)
		// re-enqueue
		b.mu.Lock()
		b.pendingQueue = append(b.pendingQueue, req)
		b.mu.Unlock()
		return
	}
	b.logger.Info("slot_assigned_provisional_from_queue", "slot", slotID, "agent", req.agentID, "correlation_id", req.correlationID)

	if req.replyStream == "" {
		// Without a reply stream we can't confirm; treat as failed.
		b.slots.RevertProvisional(req.correlationID)
		return
	}
	reply, err := messaging.NewMessage(messaging.TypeSlotAssigned, "llm-broker", messaging.SlotAssignedPayload{SlotID: slotID})
	if err != nil {
		b.logger.Error("failed to create slot assigned reply", "error", err)
		b.slots.RevertProvisional(req.correlationID)
		return
	}
	reply.CorrelationID = req.correlationID
	if _, err := b.client.Publish(ctx, req.replyStream, reply); err != nil {
		b.logger.Error("slot_assigned_reply_failed", "error", err, "slot", slotID, "agent", req.agentID, "correlation_id", req.correlationID)
		b.slots.RevertProvisional(req.correlationID)
		return
	}
	b.logger.Info("slot_assigned_reply_published", "slot", slotID, "agent", req.agentID, "correlation_id", req.correlationID)
	b.scheduleReclaim(req.correlationID)
}

func (b *Broker) consumeLLMRequests(ctx context.Context) error {
	stream := "stream:broker:llm-requests"
	group := "cg:llm-broker"
	consumer := "llm-proxy"
	return b.client.ConsumeStream(ctx, stream, group, consumer, 10, 2*time.Second,
		func(ctx context.Context, msg *messaging.Message) error {
			b.handleLLMRequest(ctx, msg)
			return nil
		}, b.logger, nil)
}

func (b *Broker) handleLLMRequest(ctx context.Context, msg *messaging.Message) {
	var payload messaging.LLMRequestPayload
	if err := msg.DecodePayload(&payload); err != nil {
		b.logger.Error("failed to decode LLM request", "error", err)
		return
	}

	requestedClass, ok := NormalizeClass(payload.SlotClass)
	if !ok {
		b.logger.Warn("unknown_slot_class", "slot_class", payload.SlotClass, "agent", msg.Source, "correlation_id", msg.CorrelationID)
		b.sendDoneOnReply(ctx, msg)
		return
	}
	slotClass := b.slots.ClassOf(payload.SlotID)
	if slotClass != requestedClass {
		b.logger.Warn("llm_request_class_mismatch",
			"slot_id", payload.SlotID,
			"slot_class_expected", slotClass,
			"slot_class_requested", requestedClass,
			"agent_id", msg.Source,
			"correlation_id", msg.CorrelationID,
		)
		b.sendDoneOnReply(ctx, msg)
		return
	}

	if !b.slots.IsOwnedBy(payload.SlotID, msg.Source) {
		b.logger.Error("llm_request_slot_not_owned", "slot", payload.SlotID, "agent", msg.Source, "correlation_id", msg.CorrelationID)
		b.sendDoneOnReply(ctx, msg)
		return
	}

	tokenCh, toolCallCh, errCh := b.ProxyLLMRequest(ctx, payload.SlotID, payload.Messages, payload.Tools, payload.ToolChoice, payload.Stream)

	replyStream := msg.ReplyStream
	if replyStream == "" {
		// Drain to avoid goroutine leak.
		for range tokenCh {
		}
		for range toolCallCh {
		}
		<-errCh
		return
	}

	var accumulated strings.Builder
	tokensClosed := false
	toolCallsClosed := false
	for !tokensClosed || !toolCallsClosed {
		select {
		case token, ok := <-tokenCh:
			if !ok {
				tokensClosed = true
				tokenCh = nil
				continue
			}
			accumulated.WriteString(token)
			tokenMsg, err := messaging.NewReply(msg, messaging.TypeToken, "llm-broker", messaging.TokenPayload{
				Token: token,
			})
			if err == nil {
				_, _ = b.client.Publish(ctx, replyStream, tokenMsg)
			}
		case call, ok := <-toolCallCh:
			if !ok {
				toolCallsClosed = true
				toolCallCh = nil
				continue
			}
			tcMsg, err := messaging.NewReply(msg, messaging.TypeToolCall, "llm-broker", messaging.ToolCallPayload{
				Call: call,
			})
			if err == nil {
				_, _ = b.client.Publish(ctx, replyStream, tcMsg)
			}
			b.logger.Info("tool_call_assembled",
				"correlation_id", msg.CorrelationID,
				"call_id", call.ID,
				"name", call.Function.Name,
				"args_bytes", len(call.Function.Arguments),
			)
		}
	}

	if err := <-errCh; err != nil {
		b.logger.Error("LLM proxy error", "error", err)
	}

	doneMsg, err := messaging.NewReply(msg, messaging.TypeToken, "llm-broker", messaging.TokenPayload{
		Done: true,
	})
	if err == nil {
		_, _ = b.client.Publish(ctx, replyStream, doneMsg)
	}
}

func (b *Broker) sendDoneOnReply(ctx context.Context, msg *messaging.Message) {
	if msg.ReplyStream == "" {
		return
	}
	doneMsg, err := messaging.NewReply(msg, messaging.TypeToken, "llm-broker", messaging.TokenPayload{Done: true})
	if err != nil {
		return
	}
	_, _ = b.client.Publish(ctx, msg.ReplyStream, doneMsg)
}

func (b *Broker) handleDeadAgent(agentID string) {
	released := b.slots.ReleaseByAgent(agentID)
	if len(released) > 0 {
		b.logger.Warn("force-released slots from dead agent", "agent", agentID, "slots", released)
		ctx := context.Background()
		b.assignFromQueue(ctx)
	}
}

func (b *Broker) IsPreemptible(agentID string) bool {
	info, ok := b.registry.Get(agentID)
	return ok && info.Preemptible
}

type chatCompletionRequest struct {
	Model      string                 `json:"model"`
	Messages   []messaging.ChatMsg    `json:"messages"`
	Stream     bool                   `json:"stream"`
	Tools      []messaging.ToolSchema `json:"tools,omitempty"`
	ToolChoice string                 `json:"tool_choice,omitempty"`
	// IDSlot is a llama.cpp extension to the OpenAI chat completions API
	// that pins the request to a specific llama-server KV slot. Without it,
	// llama.cpp's scheduler picks a slot via prompt-prefix matching; with
	// it, we deterministically land each logical slot (our broker's
	// SlotTable) on the same llama-server KV slot so cache locality
	// actually materializes. Always sent; broker validates that SlotID is
	// a legitimate slot before proxying.
	IDSlot int `json:"id_slot"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content   string               `json:"content"`
			ToolCalls []messaging.ToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

type chatCompletionChunk struct {
	Choices []struct {
		Delta struct {
			Content      string                `json:"content"`
			ToolCalls    []toolCallDelta       `json:"tool_calls"`
			FunctionCall json.RawMessage       `json:"function_call"`
		} `json:"delta"`
	} `json:"choices"`
}

type toolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolCallAcc struct {
	ID      string
	Type    string
	Name    string
	ArgsBuf strings.Builder
}

func (b *Broker) ProxyLLMRequest(ctx context.Context, slotID int, messages []messaging.ChatMsg, tools []messaging.ToolSchema, toolChoice string, stream bool) (<-chan string, <-chan messaging.ToolCall, <-chan error) {
	tokenCh := make(chan string, 100)
	toolCallCh := make(chan messaging.ToolCall, 8)
	errCh := make(chan error, 1)

	go func() {
		defer close(tokenCh)
		defer close(toolCallCh)
		defer close(errCh)

		url := fmt.Sprintf("http://%s/v1/chat/completions", b.llamaAddr)

		reqBody, err := json.Marshal(chatCompletionRequest{
			Model:      b.model,
			Messages:   messages,
			Stream:     stream,
			Tools:      tools,
			ToolChoice: toolChoice,
			IDSlot:     slotID,
		})
		if err != nil {
			errCh <- err
			return
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
		if err != nil {
			errCh <- err
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if b.llamaAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+b.llamaAPIKey)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			errCh <- fmt.Errorf("llm server returned %d: %s", resp.StatusCode, string(respBody))
			return
		}

		if stream {
			b.readSSEStream(resp.Body, tokenCh, toolCallCh, errCh)
		} else {
			b.readFullResponse(resp.Body, tokenCh, toolCallCh, errCh)
		}
	}()

	return tokenCh, toolCallCh, errCh
}

func (b *Broker) readFullResponse(body io.Reader, tokenCh chan<- string, toolCallCh chan<- messaging.ToolCall, errCh chan<- error) {
	var resp chatCompletionResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		errCh <- fmt.Errorf("decode response: %w", err)
		return
	}
	if len(resp.Choices) > 0 {
		if resp.Choices[0].Message.Content != "" {
			tokenCh <- resp.Choices[0].Message.Content
		}
		for _, call := range resp.Choices[0].Message.ToolCalls {
			toolCallCh <- call
		}
	}
}

func (b *Broker) readSSEStream(body io.Reader, tokenCh chan<- string, toolCallCh chan<- messaging.ToolCall, errCh chan<- error) {
	scanner := bufio.NewScanner(body)
	// Increase buffer so long arguments don't overflow the default 64KB.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	accs := map[int]*toolCallAcc{}
	legacyWarned := false

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			tokenCh <- delta.Content
		}

		if len(delta.ToolCalls) > 0 {
			for _, frag := range delta.ToolCalls {
				acc, ok := accs[frag.Index]
				if !ok {
					acc = &toolCallAcc{}
					accs[frag.Index] = acc
				}
				if acc.ID == "" && frag.ID != "" {
					acc.ID = frag.ID
				}
				if acc.Type == "" && frag.Type != "" {
					acc.Type = frag.Type
				}
				if acc.Name == "" && frag.Function.Name != "" {
					acc.Name = frag.Function.Name
				}
				if frag.Function.Arguments != "" {
					acc.ArgsBuf.WriteString(frag.Function.Arguments)
				}
			}
		} else if len(delta.FunctionCall) > 0 {
			if !legacyWarned {
				b.logger.Warn("tool_call_legacy_unsupported")
				legacyWarned = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		errCh <- err
	}

	indices := make([]int, 0, len(accs))
	for idx := range accs {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	for _, idx := range indices {
		acc := accs[idx]
		typ := acc.Type
		if typ == "" {
			typ = "function"
		}
		toolCallCh <- messaging.ToolCall{
			ID:   acc.ID,
			Type: typ,
			Function: messaging.ToolCallFunction{
				Name:      acc.Name,
				Arguments: acc.ArgsBuf.String(),
			},
		}
	}
}
