package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	gocontext "context"

	"microagent2/internal/config"
	appcontext "microagent2/internal/context"
	"microagent2/internal/messaging"
	"microagent2/internal/response"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	valkeyAddr := envOr("VALKEY_ADDR", "localhost:6379")
	muninnAddr := envOr("MUNINNDB_ADDR", "localhost:8100")
	muninnAPIKey := envOr("MUNINNDB_API_KEY", "")

	client := messaging.NewClient(valkeyAddr)
	defer client.Close()

	ctx, cancel := gocontext.WithCancel(gocontext.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}

	cfgStore := config.NewStore(client.Redis())
	chatCfg := config.ResolveChat(ctx, cfgStore)
	memCfg := config.ResolveMemory(ctx, cfgStore)

	responses := response.NewStore(client.Redis())
	muninn := appcontext.NewMuninnClient(muninnAddr, muninnAPIKey, memCfg.Vault, memCfg.RecallThreshold, memCfg.MaxHops, memCfg.StoreConfidence)
	assembler := appcontext.NewAssembler(chatCfg.SystemPrompt)
	mgr := appcontext.NewManager(client, responses, muninn, assembler, logger, memCfg.RecallLimit, memCfg.PrewarmLimit)

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down context manager")
		cancel()
	}()

	logger.Info("context manager ready")
	if err := mgr.Run(ctx); err != nil && err != gocontext.Canceled {
		logger.Error("context manager error", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
