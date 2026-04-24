package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"microagent2/internal/config"
	"microagent2/internal/gateway"
	"microagent2/internal/messaging"
	"microagent2/internal/response"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	valkeyAddr := envOr("VALKEY_ADDR", "localhost:6379")
	port := envOr("GATEWAY_PORT", "8080")
	llamaAddr := envOr("LLAMA_ADDR", "http://localhost:8081")
	muninnAddr := envOr("MUNINNDB_ADDR", "http://localhost:8100")

	client := messaging.NewClient(valkeyAddr)
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}

	cfgStore := config.NewStore(client.Redis())
	chatCfg := config.ResolveChat(ctx, cfgStore)
	logger.Info("config resolved", "model", chatCfg.Model, "request_timeout_s", chatCfg.RequestTimeoutS)

	sessionHashTTL := time.Duration(envInt("SESSION_HASH_TTL_HOURS", 24)) * time.Hour
	responses := response.NewStoreWithSessionHashTTL(client.Redis(), sessionHashTTL)
	srv := gateway.New(client, logger, cfgStore, responses, chatCfg.RequestTimeoutS, port, llamaAddr, muninnAddr)

	httpServer := &http.Server{
		Addr:    ":" + port,
		Handler: srv,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down gateway")
		httpServer.Close()
		cancel()
	}()

	logger.Info("gateway listening", "port", port)
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("gateway server error", "error", err)
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
