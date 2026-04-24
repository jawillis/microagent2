package main

import (
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	gocontext "context"

	"github.com/jasonwillis/microagent2/internal/agent"
	appcontext "github.com/jasonwillis/microagent2/internal/context"
	"github.com/jasonwillis/microagent2/internal/messaging"
	"github.com/jasonwillis/microagent2/internal/registry"
	"github.com/jasonwillis/microagent2/internal/retro"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	valkeyAddr := envOr("VALKEY_ADDR", "localhost:6379")
	muninnAddr := envOr("MUNINNDB_ADDR", "localhost:8100")
	agentID := envOr("AGENT_ID", "retro-agent")
	priority := envInt("AGENT_PRIORITY", 1)
	heartbeatMS := envInt("HEARTBEAT_INTERVAL_MS", 3000)
	inactivityTimeoutS := envInt("INACTIVITY_TIMEOUT_S", 300)

	client := messaging.NewClient(valkeyAddr)
	defer client.Close()

	ctx, cancel := gocontext.WithCancel(gocontext.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}

	reg := registry.NewAgentRegistrar(client, messaging.RegisterPayload{
		AgentID:             agentID,
		Priority:            priority,
		Preemptible:         true,
		Capabilities:        []string{"memory_extraction", "skill_creation", "curation"},
		Trigger:             "event-driven",
		HeartbeatIntervalMS: heartbeatMS,
	})

	if err := reg.Register(ctx); err != nil {
		logger.Error("failed to register agent", "error", err)
		os.Exit(1)
	}
	logger.Info("retro agent registered", "agent_id", agentID)

	go reg.RunHeartbeat(ctx)

	rt := agent.NewRuntime(client, agentID, priority, true, logger)
	sessions := appcontext.NewSessionStore(client.Redis())
	muninn := appcontext.NewMuninnClient(muninnAddr)
	checkpoints := retro.NewCheckpointStore(client.Redis())

	memJob := retro.NewMemoryExtractionJob(rt, sessions, muninn, logger, checkpoints)
	skillJob := retro.NewSkillCreationJob(rt, sessions, muninn, logger, checkpoints)
	curationJob := retro.NewCurationJob(rt, muninn, logger)

	dispatch := func(sessionID string) {
		logger.Info("retro jobs triggered", "session", sessionID)

		cp := checkpoints.Load(sessionID, memJob.Type())
		if err := memJob.Run(ctx, sessionID, cp); err != nil && err != messaging.ErrPreempted {
			logger.Error("memory extraction failed", "session", sessionID, "error", err)
		}

		cp = checkpoints.Load(sessionID, skillJob.Type())
		if err := skillJob.Run(ctx, sessionID, cp); err != nil && err != messaging.ErrPreempted {
			logger.Error("skill creation failed", "session", sessionID, "error", err)
		}

		if err := curationJob.Run(ctx, sessionID, nil); err != nil && err != messaging.ErrPreempted {
			logger.Error("curation failed", "session", sessionID, "error", err)
		}
	}

	inactivityTimeout := time.Duration(inactivityTimeoutS) * time.Second
	trigger := retro.NewTrigger(client, logger, inactivityTimeout, dispatch)

	go trigger.RunInactivityTrigger(ctx)
	go trigger.RunSessionEndTrigger(ctx)

	go handleSignals(ctx, cancel, reg, rt, logger)

	logger.Info("retro agent ready",
		"inactivity_timeout", inactivityTimeout,
		"priority", priority,
	)

	<-ctx.Done()
}

func handleSignals(ctx gocontext.Context, cancel gocontext.CancelFunc, reg *registry.AgentRegistrar, rt *agent.Runtime, logger *slog.Logger) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("received signal, shutting down", "signal", sig)
	case <-ctx.Done():
		return
	}

	_ = rt.ReleaseSlot(gocontext.Background())
	_ = reg.Deregister(gocontext.Background())
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
