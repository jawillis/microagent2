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
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	valkeyAddr := envOr("VALKEY_ADDR", "localhost:6379")
	llamaAddr := envOr("LLAMA_SERVER_ADDR", "localhost:8081")
	llamaAPIKey := envOr("LLAMA_API_KEY", "")
	slotCount := envInt("SLOT_COUNT", 4)
	preemptTimeoutMS := envInt("PREEMPT_TIMEOUT_MS", 5000)

	client := messaging.NewClient(valkeyAddr)
	defer client.Close()

	ctx, cancel := gocontext.WithCancel(gocontext.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}

	reg := registry.NewRegistry()
	preemptTimeout := time.Duration(preemptTimeoutMS) * time.Millisecond
	b := broker.New(client, reg, logger, llamaAddr, llamaAPIKey, slotCount, preemptTimeout)

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down broker")
		cancel()
	}()

	logger.Info("llm broker ready", "slots", slotCount, "llama_addr", llamaAddr)
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
