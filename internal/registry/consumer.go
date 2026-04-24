package registry

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"microagent2/internal/dashboard"
	"microagent2/internal/messaging"
)

type RegistryConsumer struct {
	client        *messaging.Client
	registry      *Registry
	logger        *slog.Logger
	onDead        func(agentID string)
	consumerGroup string
}

// NewRegistryConsumer constructs a consumer for the broker's default
// consumer group. Use NewRegistryConsumerWithGroup when a different
// service (e.g. the gateway) needs its own view of every registration.
func NewRegistryConsumer(client *messaging.Client, registry *Registry, logger *slog.Logger, onDead func(string)) *RegistryConsumer {
	return NewRegistryConsumerWithGroup(client, registry, logger, onDead, messaging.ConsumerGroupBroker)
}

// NewRegistryConsumerWithGroup lets callers pick the Valkey consumer
// group, so multiple services can each receive every registration
// message independently (each group gets its own copy of the stream).
func NewRegistryConsumerWithGroup(client *messaging.Client, registry *Registry, logger *slog.Logger, onDead func(string), group string) *RegistryConsumer {
	return &RegistryConsumer{
		client:        client,
		registry:      registry,
		logger:        logger,
		onDead:        onDead,
		consumerGroup: group,
	}
}

func (rc *RegistryConsumer) RunRegistrationConsumer(ctx context.Context) error {
	group := rc.consumerGroup
	consumer := "registry-consumer"

	if err := rc.client.EnsureGroup(ctx, messaging.StreamRegistryAnnounce, group); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msgs, ids, err := rc.client.ReadGroup(ctx, messaging.StreamRegistryAnnounce, group, consumer, 10, 2*time.Second)
		if err != nil {
			continue
		}

		for i, msg := range msgs {
			switch msg.Type {
			case messaging.TypeRegister:
				var payload messaging.RegisterPayload
				if err := msg.DecodePayload(&payload); err != nil {
					rc.logger.Error("failed to decode registration", "error", err)
					continue
				}
				// Validate the panel descriptor, if any. Invalid descriptors
				// are dropped with a WARN log; the agent is still registered
				// so its other contract obligations (slots, heartbeats) work.
				panel := payload.DashboardPanel
				if panel != nil {
					if err := dashboard.ValidateDescriptor(panel); err != nil {
						rc.logger.Warn("dashboard_panel_invalid",
							"agent_id", payload.AgentID,
							"reason", err.Error(),
						)
						panel = nil
					} else {
						rc.logger.Info("dashboard_panel_validated",
							"agent_id", payload.AgentID,
							"title", panel.Title,
						)
					}
				}
				rc.registry.Register(&AgentInfo{
					AgentID:             payload.AgentID,
					Priority:            payload.Priority,
					Preemptible:         payload.Preemptible,
					Capabilities:        payload.Capabilities,
					Trigger:             payload.Trigger,
					HeartbeatIntervalMS: payload.HeartbeatIntervalMS,
					DashboardPanel:      panel,
				})
				rc.logger.Info("agent registered", "agent_id", payload.AgentID, "priority", payload.Priority)

			case messaging.TypeDeregister:
				var payload messaging.DeregisterPayload
				if err := msg.DecodePayload(&payload); err != nil {
					rc.logger.Error("failed to decode deregistration", "error", err)
					continue
				}
				rc.registry.Deregister(payload.AgentID)
				rc.logger.Info("agent deregistered", "agent_id", payload.AgentID)
			}

			_ = rc.client.Ack(ctx, messaging.StreamRegistryAnnounce, group, ids[i])
		}
	}
}

func (rc *RegistryConsumer) RunHeartbeatMonitor(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	subscriptions := make(map[string]context.CancelFunc)

	for {
		select {
		case <-ctx.Done():
			for _, cancel := range subscriptions {
				cancel()
			}
			return
		case <-ticker.C:
			agents := rc.registry.ListAlive()
			for _, agent := range agents {
				if _, exists := subscriptions[agent.AgentID]; !exists {
					subCtx, cancel := context.WithCancel(ctx)
					subscriptions[agent.AgentID] = cancel
					go rc.monitorAgent(subCtx, agent)
				}
			}

			for agentID := range subscriptions {
				if _, ok := rc.registry.Get(agentID); !ok {
					subscriptions[agentID]()
					delete(subscriptions, agentID)
				}
			}
		}
	}
}

func (rc *RegistryConsumer) monitorAgent(ctx context.Context, agent *AgentInfo) {
	channel := fmt.Sprintf(messaging.ChannelHeartbeat, agent.AgentID)
	sub := rc.client.PubSubSubscribe(ctx, channel)
	defer sub.Close()

	missThreshold := time.Duration(agent.HeartbeatIntervalMS*3) * time.Millisecond
	timer := time.NewTimer(missThreshold)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			rc.registry.MarkDead(agent.AgentID)
			rc.logger.Warn("agent missed heartbeats, marked dead", "agent_id", agent.AgentID)
			if rc.onDead != nil {
				rc.onDead(agent.AgentID)
			}
			return
		case msg := <-sub.Channel():
			if msg != nil {
				rc.registry.MarkHeartbeat(agent.AgentID)
				timer.Reset(missThreshold)
			}
		}
	}
}
