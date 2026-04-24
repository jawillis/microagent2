package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"microagent2/internal/agent"
	"microagent2/internal/config"
	"microagent2/internal/mcp"
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
	"microagent2/internal/skills"
	"microagent2/internal/tools"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	valkeyAddr := envOr("VALKEY_ADDR", "localhost:6379")
	agentID := envOr("AGENT_ID", "main-agent")
	priority := envInt("AGENT_PRIORITY", 0)
	preemptible := envOr("AGENT_PREEMPTIBLE", "false") == "true"
	heartbeatMS := envInt("HEARTBEAT_INTERVAL_MS", 3000)
	skillsDir := envOr("SKILLS_DIR", "./skills")
	maxIter := envInt("TOOL_LOOP_MAX_ITER", 10)

	client := messaging.NewClient(valkeyAddr)
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		logger.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}

	skillsStore := skills.NewStore(skillsDir, logger)
	toolRegistry := tools.NewRegistry(logger)
	if err := toolRegistry.Register(tools.NewListSkills(skillsStore)); err != nil {
		logger.Error("register list_skills", "error", err)
		os.Exit(1)
	}
	if err := toolRegistry.Register(tools.NewReadSkill(skillsStore)); err != nil {
		logger.Error("register read_skill", "error", err)
		os.Exit(1)
	}

	cfgStore := config.NewStore(client.Redis())
	mcpServers := config.ResolveMCPServers(ctx, cfgStore, logger)
	mcpMgr := mcp.NewManager(client.Redis(), logger)
	mcpMgr.Start(ctx, mcpServers, toolRegistry)

	reg := registry.NewAgentRegistrar(client, messaging.RegisterPayload{
		AgentID:             agentID,
		Priority:            priority,
		Preemptible:         preemptible,
		Capabilities:        []string{"chat"},
		Trigger:             "request-driven",
		HeartbeatIntervalMS: heartbeatMS,
	})

	if err := reg.Register(ctx); err != nil {
		logger.Error("failed to register agent", "error", err)
		os.Exit(1)
	}
	logger.Info("agent registered", "agent_id", agentID)

	go reg.RunHeartbeat(ctx)

	rt := agent.NewRuntime(client, agentID, priority, preemptible, logger)

	go handleSignals(ctx, cancel, reg, rt, mcpMgr, logger)

	stream := fmt.Sprintf(messaging.StreamAgentRequests, agentID)
	group := fmt.Sprintf(messaging.ConsumerGroupAgent, agentID)
	consumer := "worker"

	logger.Info("main agent ready, consuming requests", "skills_dir", skillsDir, "tool_loop_max_iter", maxIter)

	if err := client.ConsumeStream(ctx, stream, group, consumer, 1, 2*time.Second,
		func(ctx context.Context, msg *messaging.Message) error {
			handleRequest(ctx, client, rt, toolRegistry, skillsStore, maxIter, msg, logger)
			return nil
		}, logger, nil); err != nil && err != context.Canceled {
		logger.Error("consume stream exited", "error", err)
	}
}

func handleRequest(ctx context.Context, client *messaging.Client, rt *agent.Runtime, toolRegistry *tools.Registry, skillsStore *skills.Store, maxIter int, msg *messaging.Message, logger *slog.Logger) {
	var payload messaging.ContextAssembledPayload
	if err := msg.DecodePayload(&payload); err != nil {
		logger.Error("failed to decode context assembled message", "error", err)
		return
	}

	correlationID := msg.CorrelationID
	logger.Info("message_received", "correlation_id", correlationID, "session_id", payload.SessionID)

	messages := injectSkillManifest(payload.Messages, skillsStore)
	toolSchemas := toolRegistry.Schemas()

	tokenChannel := fmt.Sprintf(messaging.ChannelTokens, payload.SessionID)
	toolCallChannel := fmt.Sprintf(messaging.ChannelToolCalls, payload.SessionID)

	onToken := func(token string) {
		tokenMsg, err := messaging.NewMessage(messaging.TypeToken, "main-agent", messaging.TokenPayload{
			SessionID: payload.SessionID,
			Token:     token,
		})
		if err == nil {
			_ = client.PubSubPublish(ctx, tokenChannel, tokenMsg)
		}
	}

	onToolCall := func(call messaging.ToolCall) {
		tcMsg, err := messaging.NewMessage(messaging.TypeToolCall, "main-agent", messaging.ToolCallPayload{
			SessionID: payload.SessionID,
			Call:      call,
		})
		if err == nil {
			_ = client.PubSubPublish(ctx, toolCallChannel, tcMsg)
		}
	}

	publishToolResult := func(callID, output string) {
		trMsg, err := messaging.NewMessage(messaging.TypeToolResult, "main-agent", messaging.ToolResultPayload{
			SessionID: payload.SessionID,
			CallID:    callID,
			Output:    output,
		})
		if err == nil {
			_ = client.PubSubPublish(ctx, toolCallChannel, trMsg)
		}
	}

	var (
		finalContent   string
		allToolCalls   []messaging.ToolCall
		allToolResults []messaging.ToolResult
		preempted      bool
		loopErr        error
	)

	overallStart := time.Now()
	iter := 0
	for ; iter < maxIter; iter++ {
		slotID, err := rt.RequestSlotWithCorrelation(ctx, correlationID)
		if err != nil {
			logger.Error("failed to get slot", "correlation_id", correlationID, "iter", iter, "error", err)
			loopErr = err
			break
		}

		execStart := time.Now()
		content, toolCalls, err := rt.ExecuteWithCorrelation(ctx, correlationID, messages, toolSchemas, onToken, onToolCall)
		execElapsed := time.Since(execStart).Milliseconds()

		_ = rt.ReleaseSlotWithCorrelation(ctx, correlationID)

		switch {
		case err == nil:
			logger.Info("execute_done", "correlation_id", correlationID, "slot", slotID, "iter", iter, "elapsed_ms", execElapsed, "outcome", "ok")
		case err == messaging.ErrPreempted:
			logger.Info("execute_done", "correlation_id", correlationID, "slot", slotID, "iter", iter, "elapsed_ms", execElapsed, "outcome", "preempted")
			preempted = true
		default:
			logger.Error("execute_done", "correlation_id", correlationID, "slot", slotID, "iter", iter, "elapsed_ms", execElapsed, "outcome", "error", "error", err)
			loopErr = err
		}

		finalContent = content
		if len(toolCalls) == 0 || preempted || loopErr != nil {
			break
		}

		messages = append(messages, messaging.ChatMsg{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		})

		for _, call := range toolCalls {
			invStart := time.Now()
			res, _ := toolRegistry.Invoke(ctx, call.Function.Name, call.Function.Arguments)
			logger.Info("tool_invoked",
				"correlation_id", correlationID,
				"tool_name", call.Function.Name,
				"args_bytes", len(call.Function.Arguments),
				"elapsed_ms", res.ElapsedMS,
				"outcome", res.Outcome,
				"result_bytes", res.ResultSize,
				"iter", iter,
				"total_elapsed_ms", time.Since(invStart).Milliseconds(),
			)
			messages = append(messages, messaging.ChatMsg{
				Role:       "tool",
				Content:    res.Output,
				ToolCallID: call.ID,
			})
			allToolCalls = append(allToolCalls, call)
			allToolResults = append(allToolResults, messaging.ToolResult{CallID: call.ID, Output: res.Output})
			publishToolResult(call.ID, res.Output)
		}
	}

	if iter == maxIter {
		logger.Warn("tool_loop_max_iter_hit", "correlation_id", correlationID, "iterations", iter)
		if finalContent != "" && !strings.HasSuffix(finalContent, "\n") {
			finalContent += "\n"
		}
		finalContent += "(max iterations reached)"
	}

	doneMsg, err := messaging.NewMessage(messaging.TypeToken, "main-agent", messaging.TokenPayload{
		SessionID: payload.SessionID,
		Done:      true,
	})
	if err == nil {
		_ = client.PubSubPublish(ctx, tokenChannel, doneMsg)
	}

	logger.Info("turn_complete",
		"correlation_id", correlationID,
		"iterations", iter,
		"tool_calls", len(allToolCalls),
		"preempted", preempted,
		"elapsed_ms", time.Since(overallStart).Milliseconds(),
	)

	if payload.ReplyStream != "" && loopErr == nil {
		reply, err := messaging.NewReply(msg, messaging.TypeChatResponse, "main-agent", messaging.ChatResponsePayload{
			SessionID:   payload.SessionID,
			Content:     finalContent,
			ToolCalls:   allToolCalls,
			ToolResults: allToolResults,
			Done:        true,
		})
		if err == nil {
			_, _ = client.Publish(ctx, payload.ReplyStream, reply)
		}
	}
}

// injectSkillManifest appends an <available_skills> block to the first
// system-role message when the skills store has at least one skill.
// Returns a new slice; caller's slice is unchanged.
func injectSkillManifest(in []messaging.ChatMsg, store *skills.Store) []messaging.ChatMsg {
	manifests := store.List()
	if len(manifests) == 0 || len(in) == 0 || in[0].Role != "system" {
		return in
	}

	var b strings.Builder
	b.WriteString("\n\n<available_skills>\n")
	for _, m := range manifests {
		b.WriteString("- ")
		b.WriteString(m.Name)
		b.WriteString(": ")
		b.WriteString(m.Description)
		b.WriteString("\n")
	}
	b.WriteString("</available_skills>")

	out := make([]messaging.ChatMsg, len(in))
	copy(out, in)
	out[0] = messaging.ChatMsg{
		Role:       out[0].Role,
		Content:    out[0].Content + b.String(),
		ToolCalls:  out[0].ToolCalls,
		ToolCallID: out[0].ToolCallID,
	}
	return out
}

func handleSignals(ctx context.Context, cancel context.CancelFunc, reg *registry.AgentRegistrar, rt *agent.Runtime, mcpMgr *mcp.Manager, logger *slog.Logger) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("received signal, shutting down", "signal", sig)
	case <-ctx.Done():
		return
	}

	_ = rt.ReleaseSlot(context.Background())
	_ = reg.Deregister(context.Background())
	mcpMgr.Close(context.Background())
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

