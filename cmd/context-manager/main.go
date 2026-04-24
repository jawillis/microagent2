package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	gocontext "context"

	appcontext "microagent2/internal/context"
	"microagent2/internal/messaging"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	valkeyAddr := envOr("VALKEY_ADDR", "localhost:6379")
	muninnAddr := envOr("MUNINNDB_ADDR", "localhost:8100")
	muninnAPIKey := envOr("MUNINNDB_API_KEY", "")
	systemPrompt := envOr("SYSTEM_PROMPT", "You are a helpful assistant.")

	client := messaging.NewClient(valkeyAddr)
	defer client.Close()

	ctx, cancel := gocontext.WithCancel(gocontext.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}

	sessions := appcontext.NewSessionStore(client.Redis())
	muninn := appcontext.NewMuninnClient(muninnAddr, muninnAPIKey)
	assembler := appcontext.NewAssembler(systemPrompt)
	mgr := appcontext.NewManager(client, sessions, muninn, assembler, logger)

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
