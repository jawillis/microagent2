package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"microagent2/internal/hindsight"
	"microagent2/internal/memoryservice"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	hindsightAddr := os.Getenv("HINDSIGHT_ADDR")
	if hindsightAddr == "" {
		logger.Error("missing_required_env", "var", "HINDSIGHT_ADDR")
		os.Exit(1)
	}
	webhookSecret := os.Getenv("HINDSIGHT_WEBHOOK_SECRET")
	if webhookSecret == "" {
		logger.Error("missing_required_env", "var", "HINDSIGHT_WEBHOOK_SECRET")
		os.Exit(1)
	}
	apiKey := os.Getenv("HINDSIGHT_API_KEY")
	bankID := envOr("MEMORY_BANK_ID", "microagent2")
	httpAddr := envOr("MEMORY_SERVICE_HTTP_ADDR", ":8083")
	externalURL := envOr("MEMORY_SERVICE_EXTERNAL_URL", "http://memory-service:8083")
	yamlDir := envOr("MEMORY_YAML_DIR", "/etc/microagent2/memory")
	syncRetryInterval := 30 * time.Second

	hc := hindsight.New(hindsightAddr, apiKey)

	srv := memoryservice.New(hc, memoryservice.Config{
		BankID:        bankID,
		ExternalURL:   externalURL,
		WebhookSecret: webhookSecret,
	}, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	syncer := memoryservice.NewSyncer(hc, bankID, logger)

	// Run initial sync; retry on a timer until success.
	go runSyncLoop(ctx, logger, syncer, srv, yamlDir, syncRetryInterval)

	httpSrv := &http.Server{Addr: httpAddr, Handler: srv.Handler()}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down memory-service")
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		_ = httpSrv.Shutdown(shutCtx)
		cancel()
	}()

	logger.Info("memory_service_ready",
		"http_addr", httpAddr,
		"hindsight_addr", hindsightAddr,
		"bank_id", bankID,
		"external_url", externalURL,
		"yaml_dir", yamlDir,
	)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("memory_service_error", "error", err)
		os.Exit(1)
	}
}

func runSyncLoop(ctx context.Context, logger *slog.Logger, syncer *memoryservice.Syncer, srv *memoryservice.Server, yamlDir string, retry time.Duration) {
	for {
		if err := syncOnce(ctx, logger, syncer, srv, yamlDir); err != nil {
			logger.Error("memory_sync_failed", "error", err.Error(), "retry_in_s", int(retry.Seconds()))
			srv.MarkHindsightReachable(false)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retry):
				continue
			}
		}
		srv.MarkHindsightReachable(true)
		logger.Info("memory_sync_complete")
		return
	}
}

func syncOnce(ctx context.Context, logger *slog.Logger, syncer *memoryservice.Syncer, srv *memoryservice.Server, yamlDir string) error {
	seed, err := memoryservice.LoadSeedConfig(yamlDir)
	if err != nil {
		return err
	}
	if err := syncer.Apply(ctx, seed); err != nil {
		return err
	}
	if err := srv.RegisterWebhooks(ctx); err != nil {
		return err
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
