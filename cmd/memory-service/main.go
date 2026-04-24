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
	"microagent2/internal/hindsight"
	"microagent2/internal/memoryservice"
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
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
	valkeyAddr := envOr("VALKEY_ADDR", "valkey:6379")
	apiKey := os.Getenv("HINDSIGHT_API_KEY")
	bankID := envOr("MEMORY_BANK_ID", "microagent2")
	httpAddr := envOr("MEMORY_SERVICE_HTTP_ADDR", ":8083")
	externalURL := envOr("MEMORY_SERVICE_EXTERNAL_URL", "http://memory-service:8083")
	yamlDir := envOr("MEMORY_YAML_DIR", "/etc/microagent2/memory")
	// cpURL is the externally-reachable URL for Hindsight's Control Plane —
	// the URL the operator's browser uses when rendering the iframe. Differs
	// from hindsightAddr, which is the in-network API URL.
	cpURL := envOr("MEMORY_SERVICE_CP_URL", "http://localhost:9999")
	heartbeatMS := envInt("HEARTBEAT_INTERVAL_MS", 3000)
	syncRetryInterval := 30 * time.Second

	hc := hindsight.New(hindsightAddr, apiKey)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Messaging client for service registration + heartbeat.
	msgClient := messaging.NewClient(valkeyAddr)
	defer msgClient.Close()
	if err := msgClient.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err.Error())
		os.Exit(1)
	}
	cfgStore := config.NewStore(msgClient.Redis())

	srv := memoryservice.New(hc, memoryservice.Config{
		BankID:        bankID,
		ExternalURL:   externalURL,
		WebhookSecret: webhookSecret,
		Resolver: func(ctx context.Context) config.MemoryConfig {
			return config.ResolveMemory(ctx, cfgStore)
		},
	}, logger)

	// Register memory-service + start heartbeat so the dashboard
	// aggregator picks up our panel descriptor.
	agentReg := registry.NewAgentRegistrar(msgClient, messaging.RegisterPayload{
		AgentID:             "memory-service",
		Priority:            0,
		Preemptible:         false,
		Capabilities:        []string{"memory"},
		Trigger:             "http",
		HeartbeatIntervalMS: heartbeatMS,
		DashboardPanel:      memoryservice.BuildPanelDescriptor(cpURL, bankID),
	})
	if err := agentReg.Register(ctx); err != nil {
		logger.Error("failed_to_register", "error", err.Error())
		os.Exit(1)
	}
	go agentReg.RunHeartbeat(ctx)
	logger.Info("memory_service_registered", "agent_id", "memory-service", "cp_url", cpURL)

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
		_ = agentReg.Deregister(shutCtx)
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

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
