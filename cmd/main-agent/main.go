package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"microagent2/internal/agent"
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	valkeyAddr := envOr("VALKEY_ADDR", "localhost:6379")
	agentID := envOr("AGENT_ID", "main-agent")
	priority := envInt("AGENT_PRIORITY", 0)
	preemptible := envOr("AGENT_PREEMPTIBLE", "false") == "true"
	heartbeatMS := envInt("HEARTBEAT_INTERVAL_MS", 3000)

	client := messaging.NewClient(valkeyAddr)
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}

	reg := registry.NewAgentRegistrar(client, messaging.RegisterPayload{
		AgentID:            agentID,
		Priority:           priority,
		Preemptible:        preemptible,
		Capabilities:       []string{"chat"},
		Trigger:            "request-driven",
		HeartbeatIntervalMS: heartbeatMS,
	})

	if err := reg.Register(ctx); err != nil {
		logger.Error("failed to register agent", "error", err)
		os.Exit(1)
	}
	logger.Info("agent registered", "agent_id", agentID)

	go reg.RunHeartbeat(ctx)

	rt := agent.NewRuntime(client, agentID, priority, preemptible, logger)

	go handleSignals(ctx, cancel, reg, rt, logger)

	stream := fmt.Sprintf(messaging.StreamAgentRequests, agentID)
	group := fmt.Sprintf(messaging.ConsumerGroupAgent, agentID)
	consumer := "worker"

	if err := client.EnsureGroup(ctx, stream, group); err != nil {
		logger.Error("failed to ensure consumer group", "error", err)
		os.Exit(1)
	}

	logger.Info("main agent ready, consuming requests")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msgs, ids, err := client.ReadGroup(ctx, stream, group, consumer, 1, 2*time.Second)
		if err != nil {
			continue
		}

		for i, msg := range msgs {
			handleRequest(ctx, client, rt, msg, logger)
			_ = client.Ack(ctx, stream, group, ids[i])
		}
	}
}

func handleRequest(ctx context.Context, client *messaging.Client, rt *agent.Runtime, msg *messaging.Message, logger *slog.Logger) {
	var payload messaging.ContextAssembledPayload
	if err := msg.DecodePayload(&payload); err != nil {
		logger.Error("failed to decode context assembled message", "error", err)
		return
	}

	correlationID := msg.CorrelationID
	logger.Info("message_received", "correlation_id", correlationID, "session_id", payload.SessionID)

	slotID, err := rt.RequestSlotWithCorrelation(ctx, correlationID)
	if err != nil {
		logger.Error("failed to get slot", "correlation_id", correlationID, "error", err)
		return
	}
	defer rt.ReleaseSlotWithCorrelation(ctx, correlationID)

	tokenChannel := fmt.Sprintf(messaging.ChannelTokens, payload.SessionID)

	onToken := func(token string) {
		tokenMsg, err := messaging.NewMessage(messaging.TypeToken, "main-agent", messaging.TokenPayload{
			SessionID: payload.SessionID,
			Token:     token,
		})
		if err == nil {
			_ = client.PubSubPublish(ctx, tokenChannel, tokenMsg)
		}
	}

	execStart := time.Now()
	result, err := rt.ExecuteWithCorrelation(ctx, correlationID, payload.Messages, onToken)
	execElapsed := time.Since(execStart).Milliseconds()
	switch {
	case err == nil:
		logger.Info("execute_done", "correlation_id", correlationID, "slot", slotID, "elapsed_ms", execElapsed, "outcome", "ok")
	case err == messaging.ErrPreempted:
		logger.Info("execute_done", "correlation_id", correlationID, "slot", slotID, "elapsed_ms", execElapsed, "outcome", "preempted")
	default:
		logger.Error("execute_done", "correlation_id", correlationID, "slot", slotID, "elapsed_ms", execElapsed, "outcome", "error", "error", err)
		return
	}

	doneMsg, err := messaging.NewMessage(messaging.TypeToken, "main-agent", messaging.TokenPayload{
		SessionID: payload.SessionID,
		Done:      true,
	})
	if err == nil {
		_ = client.PubSubPublish(ctx, tokenChannel, doneMsg)
	}

	if payload.ReplyStream != "" {
		reply, err := messaging.NewReply(msg, messaging.TypeChatResponse, "main-agent", messaging.ChatResponsePayload{
			SessionID: payload.SessionID,
			Content:   result,
			Done:      true,
		})
		if err == nil {
			_, _ = client.Publish(ctx, payload.ReplyStream, reply)
		}
	}
}

func handleSignals(ctx context.Context, cancel context.CancelFunc, reg *registry.AgentRegistrar, rt *agent.Runtime, logger *slog.Logger) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("received signal, shutting down", "signal", sig)
	case <-ctx.Done():
		return
	}

	_ = rt.ReleaseSlot(context.Background())
	_ = reg.Deregister(context.Background())
	cancel()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
