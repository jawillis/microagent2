package retro

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jasonwillis/microagent2/internal/messaging"
)

type TriggerType string

const (
	TriggerInactivity TriggerType = "inactivity"
	TriggerSessionEnd TriggerType = "session_end"
)

type Trigger struct {
	client           *messaging.Client
	logger           *slog.Logger
	inactivityTimeout time.Duration
	onActivate       func(sessionID string)
}

func NewTrigger(client *messaging.Client, logger *slog.Logger, inactivityTimeout time.Duration, onActivate func(string)) *Trigger {
	return &Trigger{
		client:           client,
		logger:           logger,
		inactivityTimeout: inactivityTimeout,
		onActivate:       onActivate,
	}
}

func (t *Trigger) RunInactivityTrigger(ctx context.Context) {
	sub := t.client.PubSubSubscribe(ctx, messaging.ChannelEvents)
	defer sub.Close()

	timers := make(map[string]*time.Timer)

	for {
		select {
		case <-ctx.Done():
			for _, timer := range timers {
				timer.Stop()
			}
			return
		case redisMsg := <-sub.Channel():
			if redisMsg == nil {
				continue
			}
			var msg messaging.Message
			if err := json.Unmarshal([]byte(redisMsg.Payload), &msg); err != nil {
				continue
			}

			var event messaging.SessionEventPayload
			if err := msg.DecodePayload(&event); err != nil {
				continue
			}

			if timer, exists := timers[event.SessionID]; exists {
				timer.Stop()
			}

			sessionID := event.SessionID
			timers[sessionID] = time.AfterFunc(t.inactivityTimeout, func() {
				t.logger.Info("inactivity timeout reached", "session", sessionID)
				t.onActivate(sessionID)
				delete(timers, sessionID)
			})
		}
	}
}

func (t *Trigger) RunSessionEndTrigger(ctx context.Context) {
	sub := t.client.PubSubSubscribe(ctx, messaging.ChannelEvents)
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

			var event messaging.SessionEventPayload
			if err := msg.DecodePayload(&event); err != nil {
				continue
			}

			if event.Event == "session_ended" {
				t.logger.Info("session ended, activating retro", "session", event.SessionID)
				t.onActivate(event.SessionID)
			}
		}
	}
}
