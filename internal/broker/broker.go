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
	"strings"
	"sync"
	"time"

	"microagent2/internal/messaging"
	"microagent2/internal/registry"
)

type Broker struct {
	client         *messaging.Client
	slots          *SlotTable
	registry       *registry.Registry
	regConsumer    *registry.RegistryConsumer
	logger         *slog.Logger
	llamaAddr      string
	llamaAPIKey    string
	preemptTimeout time.Duration

	mu             sync.Mutex
	pendingQueue   []slotRequest
}

type slotRequest struct {
	agentID       string
	priority      int
	correlationID string
	replyStream   string
}

func New(client *messaging.Client, reg *registry.Registry, logger *slog.Logger, llamaAddr, llamaAPIKey string, slotCount int, preemptTimeout time.Duration) *Broker {
	b := &Broker{
		client:         client,
		slots:          NewSlotTable(slotCount),
		registry:       reg,
		logger:         logger,
		llamaAddr:      llamaAddr,
		llamaAPIKey:    llamaAPIKey,
		preemptTimeout: preemptTimeout,
	}

	b.regConsumer = registry.NewRegistryConsumer(client, reg, logger, b.handleDeadAgent)

	b.slots.PinSlot(0, "main-agent", 0)
	logger.Info("slot 0 pinned to main-agent")

	return b
}

func (b *Broker) Run(ctx context.Context) error {
	go b.regConsumer.RunRegistrationConsumer(ctx)
	go b.regConsumer.RunHeartbeatMonitor(ctx)
	go b.consumeLLMRequests(ctx)
	return b.consumeSlotRequests(ctx)
}

func (b *Broker) consumeSlotRequests(ctx context.Context) error {
	stream := "stream:broker:slot-requests"
	group := messaging.ConsumerGroupBroker
	consumer := "slot-arbiter"

	if err := b.client.EnsureGroup(ctx, stream, group); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msgs, ids, err := b.client.ReadGroup(ctx, stream, group, consumer, 10, 2*time.Second)
		if err != nil {
			continue
		}

		for i, msg := range msgs {
			b.handleMessage(ctx, msg)
			_ = b.client.Ack(ctx, stream, group, ids[i])
		}
	}
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
	slotID, found := b.slots.FindUnassigned()
	if found {
		b.slots.Assign(slotID, payload.AgentID, payload.Priority)
		b.logger.Info("slot assigned", "slot", slotID, "agent", payload.AgentID)
		b.sendSlotAssigned(ctx, msg, slotID)
		return
	}

	victimSlot, victimAgent, victimPriority, hasVictim := b.slots.FindLowestPriorityPreemptible(b)
	if hasVictim && payload.Priority < victimPriority {
		b.logger.Info("preempting agent", "victim", victimAgent, "slot", victimSlot, "requester", payload.AgentID)
		b.preemptAgent(ctx, victimAgent, victimSlot, msg, payload)
		return
	}

	b.mu.Lock()
	b.pendingQueue = append(b.pendingQueue, slotRequest{
		agentID:       payload.AgentID,
		priority:      payload.Priority,
		correlationID: msg.CorrelationID,
		replyStream:   msg.ReplyStream,
	})
	b.mu.Unlock()
	b.logger.Info("slot request queued", "agent", payload.AgentID)
}

func (b *Broker) handleSlotRelease(ctx context.Context, payload messaging.SlotReleasePayload) {
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
		b.sendSlotAssigned(ctx, originalMsg, slotID)
	}()
}

func (b *Broker) sendSlotAssigned(ctx context.Context, original *messaging.Message, slotID int) {
	if original.ReplyStream == "" {
		return
	}
	reply, err := messaging.NewReply(original, messaging.TypeSlotAssigned, "llm-broker", messaging.SlotAssignedPayload{SlotID: slotID})
	if err != nil {
		b.logger.Error("failed to create slot assigned reply", "error", err)
		return
	}
	_, _ = b.client.Publish(ctx, original.ReplyStream, reply)
}

func (b *Broker) assignFromQueue(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.pendingQueue) == 0 {
		return
	}

	slotID, found := b.slots.FindUnassigned()
	if !found {
		return
	}

	bestIdx := 0
	for i, req := range b.pendingQueue {
		if req.priority < b.pendingQueue[bestIdx].priority {
			bestIdx = i
		}
	}

	req := b.pendingQueue[bestIdx]
	b.pendingQueue = append(b.pendingQueue[:bestIdx], b.pendingQueue[bestIdx+1:]...)

	b.slots.Assign(slotID, req.agentID, req.priority)
	b.logger.Info("slot assigned from queue", "slot", slotID, "agent", req.agentID)

	if req.replyStream != "" {
		reply, err := messaging.NewMessage(messaging.TypeSlotAssigned, "llm-broker", messaging.SlotAssignedPayload{SlotID: slotID})
		if err == nil {
			reply.CorrelationID = req.correlationID
			_, _ = b.client.Publish(ctx, req.replyStream, reply)
		}
	}
}

func (b *Broker) consumeLLMRequests(ctx context.Context) error {
	stream := "stream:broker:llm-requests"
	group := "cg:llm-broker"
	consumer := "llm-proxy"

	if err := b.client.EnsureGroup(ctx, stream, group); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msgs, ids, err := b.client.ReadGroup(ctx, stream, group, consumer, 10, 2*time.Second)
		if err != nil {
			continue
		}

		for i, msg := range msgs {
			b.handleLLMRequest(ctx, msg)
			_ = b.client.Ack(ctx, stream, group, ids[i])
		}
	}
}

func (b *Broker) handleLLMRequest(ctx context.Context, msg *messaging.Message) {
	var payload messaging.LLMRequestPayload
	if err := msg.DecodePayload(&payload); err != nil {
		b.logger.Error("failed to decode LLM request", "error", err)
		return
	}

	tokenCh, errCh := b.ProxyLLMRequest(ctx, payload.SlotID, payload.Messages, payload.Stream)

	replyStream := msg.ReplyStream
	if replyStream == "" {
		return
	}

	var accumulated strings.Builder
	for token := range tokenCh {
		accumulated.WriteString(token)
		tokenMsg, err := messaging.NewReply(msg, messaging.TypeToken, "llm-broker", messaging.TokenPayload{
			Token: token,
		})
		if err == nil {
			_, _ = b.client.Publish(ctx, replyStream, tokenMsg)
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
	Model    string             `json:"model"`
	Messages []messaging.ChatMsg `json:"messages"`
	Stream   bool               `json:"stream"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type chatCompletionChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

func (b *Broker) ProxyLLMRequest(ctx context.Context, slotID int, messages []messaging.ChatMsg, stream bool) (<-chan string, <-chan error) {
	tokenCh := make(chan string, 100)
	errCh := make(chan error, 1)

	go func() {
		defer close(tokenCh)
		defer close(errCh)

		url := fmt.Sprintf("http://%s/v1/chat/completions", b.llamaAddr)

		reqBody, err := json.Marshal(chatCompletionRequest{
			Model:    "default",
			Messages: messages,
			Stream:   stream,
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
			b.readSSEStream(resp.Body, tokenCh, errCh)
		} else {
			b.readFullResponse(resp.Body, tokenCh, errCh)
		}
	}()

	return tokenCh, errCh
}

func (b *Broker) readFullResponse(body io.Reader, tokenCh chan<- string, errCh chan<- error) {
	var resp chatCompletionResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		errCh <- fmt.Errorf("decode response: %w", err)
		return
	}
	if len(resp.Choices) > 0 {
		tokenCh <- resp.Choices[0].Message.Content
	}
}

func (b *Broker) readSSEStream(body io.Reader, tokenCh chan<- string, errCh chan<- error) {
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return
		}
		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			tokenCh <- chunk.Choices[0].Delta.Content
		}
	}
	if err := scanner.Err(); err != nil {
		errCh <- err
	}
}
