package main

import (
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	gocontext "context"

	"microagent2/internal/broker"
	"microagent2/internal/config"
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	valkeyAddr := envOr("VALKEY_ADDR", "localhost:6379")
	llamaAddr := envOr("LLAMA_SERVER_ADDR", "localhost:8081")
	llamaAPIKey := envOr("LLAMA_API_KEY", "")

	client := messaging.NewClient(valkeyAddr)
	defer client.Close()

	ctx, cancel := gocontext.WithCancel(gocontext.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}

	cfgStore := config.NewStore(client.Redis())
	brokerCfg := config.ResolveBroker(ctx, cfgStore)
	chatCfg := config.ResolveChat(ctx, cfgStore)

	reg := registry.NewRegistry()
	preemptTimeout := time.Duration(brokerCfg.PreemptTimeoutMS) * time.Millisecond
	provisionalTimeout := time.Duration(envInt("PROVISIONAL_TIMEOUT_MS", 2000)) * time.Millisecond
	snapshotInterval := time.Duration(envInt("SLOT_SNAPSHOT_INTERVAL_S", 30)) * time.Second

	agentSlotCount := envInt("AGENT_SLOT_COUNT", brokerCfg.SlotCount)
	hindsightSlotCount := envInt("HINDSIGHT_SLOT_COUNT", 0)
	if agentSlotCount+hindsightSlotCount > brokerCfg.SlotCount {
		logger.Error("slot_class_budget_exceeds_total",
			"agent_slot_count", agentSlotCount,
			"hindsight_slot_count", hindsightSlotCount,
			"total_slot_count", brokerCfg.SlotCount,
		)
		os.Exit(1)
	}
	b := broker.NewWithClasses(client, reg, logger, llamaAddr, llamaAPIKey, chatCfg.Model, agentSlotCount, hindsightSlotCount, preemptTimeout, provisionalTimeout, snapshotInterval)

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down broker")
		cancel()
	}()

	logger.Info("llm broker ready",
		"agent_slots", agentSlotCount,
		"hindsight_slots", hindsightSlotCount,
		"total_slots", agentSlotCount+hindsightSlotCount,
		"llama_addr", llamaAddr,
	)
	if err := b.Run(ctx); err != nil && err != gocontext.Canceled {
		logger.Error("broker error", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

