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

	"microagent2/internal/llmproxy"
	"microagent2/internal/logstream"
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	valkeyAddr := envOr("VALKEY_ADDR", "localhost:6379")
	httpAddr := envOr("LLM_PROXY_HTTP_ADDR", ":8082")
	identity := envOr("LLM_PROXY_IDENTITY", "llm-proxy")
	slotTimeoutMS := envInt("LLM_PROXY_SLOT_TIMEOUT_MS", 10000)
	reqTimeoutMS := envInt("LLM_PROXY_REQUEST_TIMEOUT_MS", 300000)

	client := messaging.NewClient(valkeyAddr)
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}
	logger = logstream.NewLogger("llm-proxy", client.Redis(), logstream.OptionsFromEnv())

	srv := llmproxy.New(client, llmproxy.Config{
		Identity:       identity,
		SlotTimeout:    time.Duration(slotTimeoutMS) * time.Millisecond,
		RequestTimeout: time.Duration(reqTimeoutMS) * time.Millisecond,
	}, logger)

	httpSrv := &http.Server{
		Addr:    httpAddr,
		Handler: srv.Handler(),
	}

	// Self-registration + heartbeat for the dashboard panel.
	heartbeatMS := envInt("HEARTBEAT_INTERVAL_MS", 3000)
	selfReg := registry.NewAgentRegistrar(client, messaging.RegisterPayload{
		AgentID:             "llm-proxy",
		Priority:            0,
		Preemptible:         false,
		Capabilities:        []string{"llm-proxy"},
		Trigger:             "http",
		HeartbeatIntervalMS: heartbeatMS,
		DashboardPanel:      llmproxy.BuildPanelDescriptor(identity),
	})
	if err := selfReg.Register(ctx); err != nil {
		logger.Error("failed_to_register", "error", err.Error())
	}
	go selfReg.RunHeartbeat(ctx)

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down llm-proxy")
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		_ = selfReg.Deregister(shutCtx)
		_ = httpSrv.Shutdown(shutCtx)
		cancel()
	}()

	logger.Info("llm_proxy_ready",
		"http_addr", httpAddr,
		"identity", identity,
		"valkey_addr", valkeyAddr,
	)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("llm-proxy server error", "error", err)
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
