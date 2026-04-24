package main

import (
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	gocontext "context"

	"microagent2/internal/agent"
	"microagent2/internal/config"
	appcontext "microagent2/internal/context"
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
	"microagent2/internal/response"
	"microagent2/internal/retro"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	valkeyAddr := envOr("VALKEY_ADDR", "localhost:6379")
	muninnAddr := envOr("MUNINNDB_ADDR", "localhost:8100")
	muninnAPIKey := envOr("MUNINNDB_API_KEY", "")
	agentID := envOr("AGENT_ID", "retro-agent")
	priority := envInt("AGENT_PRIORITY", 1)
	heartbeatMS := envInt("HEARTBEAT_INTERVAL_MS", 3000)

	client := messaging.NewClient(valkeyAddr)
	defer client.Close()

	ctx, cancel := gocontext.WithCancel(gocontext.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}

	cfgStore := config.NewStore(client.Redis())
	retroCfg := config.ResolveRetro(ctx, cfgStore)
	memCfg := config.ResolveMemory(ctx, cfgStore)

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
	responses := response.NewStore(client.Redis())
	muninn := appcontext.NewMuninnClient(muninnAddr, muninnAPIKey, memCfg.Vault, memCfg.RecallThreshold, memCfg.MaxHops, memCfg.StoreConfidence)
	checkpoints := retro.NewCheckpointStore(client.Redis())

	memJob := retro.NewMemoryExtractionJob(rt, responses, muninn, logger, checkpoints)
	skillJob := retro.NewSkillCreationJob(rt, responses, muninn, logger, checkpoints, retroCfg.MinHistoryTurns, retroCfg.SkillDupThreshold)
	curationJob := retro.NewCurationJob(rt, muninn, logger, retroCfg.CurationCategories)

	runJob := func(sessionID string, job retro.Job, cp *retro.Checkpoint) {
		acquired, err := retro.AcquireLock(ctx, client.Redis(), sessionID, job.Type())
		if err != nil {
			logger.Error("failed to acquire retro lock", "session", sessionID, "job", job.Type(), "error", err)
			return
		}
		if !acquired {
			logger.Info("retro job already locked, skipping", "session", sessionID, "job", job.Type())
			return
		}
		defer retro.ReleaseLock(ctx, client.Redis(), sessionID, job.Type())

		if err := job.Run(ctx, sessionID, cp); err != nil && err != messaging.ErrPreempted {
			logger.Error("retro job failed", "session", sessionID, "job", job.Type(), "error", err)
		}
	}

	dispatch := func(sessionID string) {
		logger.Info("retro jobs triggered", "session", sessionID)
		runJob(sessionID, memJob, checkpoints.Load(sessionID, memJob.Type()))
		runJob(sessionID, skillJob, checkpoints.Load(sessionID, skillJob.Type()))
		runJob(sessionID, curationJob, nil)
	}

	jobs := map[string]retro.Job{
		"memory_extraction": memJob,
		"skill_creation":    skillJob,
		"curation":          curationJob,
	}

	dispatchSingle := func(sessionID, jobType string) {
		job, ok := jobs[jobType]
		if !ok {
			logger.Warn("unknown retro job type from trigger", "job_type", jobType)
			return
		}
		var cp *retro.Checkpoint
		if jobType != "curation" {
			cp = checkpoints.Load(sessionID, job.Type())
		}
		logger.Info("retro job externally triggered", "session", sessionID, "job", jobType)
		job.Run(ctx, sessionID, cp)
		retro.ReleaseLock(ctx, client.Redis(), sessionID, job.Type())
	}

	inactivityTimeout := time.Duration(retroCfg.InactivityTimeoutS) * time.Second
	trigger := retro.NewTrigger(client, logger, inactivityTimeout, dispatch)

	go trigger.RunInactivityTrigger(ctx)
	go trigger.RunSessionEndTrigger(ctx)

	go func() {
		_ = client.ConsumeStream(ctx, messaging.StreamRetroTriggers, messaging.ConsumerGroupRetro, agentID, 1, time.Second,
			func(ctx gocontext.Context, msg *messaging.Message) error {
				var payload messaging.RetroTriggerPayload
				if err := msg.DecodePayload(&payload); err != nil {
					logger.Warn("invalid retro trigger payload", "error", err)
					return nil // ack; can't retry a mal-decoded payload
				}
				dispatchSingle(payload.SessionID, payload.JobType)
				return nil
			}, logger, nil)
	}()

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
