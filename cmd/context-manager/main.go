package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	gocontext "context"

	"microagent2/internal/config"
	appcontext "microagent2/internal/context"
	"microagent2/internal/logstream"
	"microagent2/internal/memoryclient"
	"microagent2/internal/messaging"
	"microagent2/internal/response"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	valkeyAddr := envOr("VALKEY_ADDR", "localhost:6379")
	memoryServiceAddr := os.Getenv("MEMORY_SERVICE_ADDR")
	if memoryServiceAddr == "" {
		logger.Error("missing_required_env", "var", "MEMORY_SERVICE_ADDR")
		os.Exit(1)
	}

	client := messaging.NewClient(valkeyAddr)
	defer client.Close()

	ctx, cancel := gocontext.WithCancel(gocontext.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}

	logger = logstream.NewLogger("context-manager", client.Redis(), logstream.OptionsFromEnv())

	cfgStore := config.NewStore(client.Redis())
	chatCfg := config.ResolveChat(ctx, cfgStore)
	memCfg := config.ResolveMemory(ctx, cfgStore)

	responses := response.NewStore(client.Redis())
	mc := memoryclient.New(memoryServiceAddr)
	assembler := appcontext.NewAssembler(chatCfg.SystemPrompt)
	mgr := appcontext.NewManager(client, responses, mc, assembler, logger, memCfg.RecallLimit, memCfg.PrewarmLimit, cfgStore)

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down context manager")
		cancel()
	}()

	logger.Info("context manager ready", "memory_service_addr", memoryServiceAddr)
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
