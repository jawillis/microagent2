// Command exec is microagent2's sandboxed code-execution service. It serves
// an HTTP API at /v1/run, /v1/install, and /v1/health; full contract lives
// in openspec/specs/code-execution/spec.md.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"microagent2/internal/exec"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg := exec.Load(logger)
	logger.Info("exec_boot", "config", cfg.String())

	// Scan skills at boot. Prewarm runs concurrently; /v1/health reports
	// "starting" until the sweep completes.
	skills := exec.ScanSkills(cfg.SkillsDir, logger)

	health := exec.NewHealth()
	installer := exec.NewInstaller(cfg, health, logger)
	runner := exec.NewRunner(cfg, installer, logger)
	server := exec.NewServer(cfg, runner, installer, health, logger, skills)

	// Start GC goroutine.
	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gc := &exec.GC{
		Root:      cfg.WorkspaceDir,
		Retention: cfg.WorkspaceRetention,
		Interval:  cfg.GCInterval,
		Logger:    logger,
	}
	go gc.Run(rootCtx)

	// Prewarm then flip Ready. Workspace root is created lazily by Allocate.
	go func() {
		prewarmCtx, prewarmCancel := context.WithTimeout(rootCtx, cfg.InstallTimeout*time.Duration(cfg.PrewarmConcurrency+1))
		defer prewarmCancel()
		installer.Prewarm(prewarmCtx, skills)
		health.Ready()
		logger.Info("exec_prewarm_complete", "ready", true)
	}()

	// Install signal handler before listening.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("exec_shutdown_requested", "signal", sig.String())
		cancel()
	}()

	if err := server.Run(rootCtx); err != nil {
		logger.Error("exec_server_exit", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("exec_exit_clean")
}
