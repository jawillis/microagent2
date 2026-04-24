package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"microagent2/internal/agent"
	"microagent2/internal/config"
	"microagent2/internal/execclient"
	"microagent2/internal/logstream"
	"microagent2/internal/mcp"
	"microagent2/internal/messaging"
	"microagent2/internal/registry"
	"microagent2/internal/sessionskill"
	"microagent2/internal/skills"
	"microagent2/internal/tools"
)

const activeSkillTTL = 24 * time.Hour

// sessionScopedTools lists built-in tool names whose argsJSON has
// session_id injected by main-agent before invocation, overriding any
// model-supplied value.
var sessionScopedTools = map[string]struct{}{
	"run_skill_script": {},
	"bash":             {},
}

// requestDeps bundles the per-request dependencies so handleRequest's signature
// stays readable after session-state plumbing landed.
type requestDeps struct {
	client    *messaging.Client
	runtime   *agent.Runtime
	registry  *tools.Registry
	store     *skills.Store
	rdb       *redis.Client
	baseTools []string
	maxIter   int
	logger    *slog.Logger
}

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
	logger = logstream.NewLogger("main-agent", client.Redis(), logstream.OptionsFromEnv())

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
	if err := toolRegistry.Register(tools.NewReadSkillFile(skillsStore)); err != nil {
		logger.Error("register read_skill_file", "error", err)
		os.Exit(1)
	}
	if err := toolRegistry.Register(tools.NewCurrentTime()); err != nil {
		logger.Error("register current_time", "error", err)
		os.Exit(1)
	}

	execAddr := envOr("EXEC_ADDR", "http://exec:8085")
	execMaxTimeoutS := envInt("EXEC_MAX_TIMEOUT_S", 120)
	execClient := execclient.New(execAddr,
		execclient.WithTimeout(time.Duration(execMaxTimeoutS+10)*time.Second),
	)
	if err := toolRegistry.Register(tools.NewRunSkillScript(execClient, logger)); err != nil {
		logger.Error("register run_skill_script", "error", err)
		os.Exit(1)
	}
	if err := toolRegistry.Register(tools.NewBash(execClient, logger)); err != nil {
		logger.Error("register bash", "error", err)
		os.Exit(1)
	}
	logger.Info("exec_client_configured", "addr", execAddr, "timeout_s", execMaxTimeoutS+10)

	cfgStore := config.NewStore(client.Redis())
	mcpServers := config.ResolveMCPServers(ctx, cfgStore, logger)
	mcpMgr := mcp.NewManager(client.Redis(), logger)
	mcpMgr.Start(ctx, mcpServers, toolRegistry)

	// Base toolset = everything registered after MCP start. Stable for the
	// process lifetime; a skill's allowed-tools expands visibility on top.
	baseTools := make([]string, 0, len(toolRegistry.Manifest()))
	for _, m := range toolRegistry.Manifest() {
		baseTools = append(baseTools, m.Name)
	}

	reg := registry.NewAgentRegistrar(client, messaging.RegisterPayload{
		AgentID:             agentID,
		Priority:            priority,
		Preemptible:         preemptible,
		Capabilities:        []string{"chat"},
		Trigger:             "request-driven",
		HeartbeatIntervalMS: heartbeatMS,
		DashboardPanel:      agent.BuildMCPPanelDescriptor(),
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

	logger.Info("main agent ready, consuming requests", "skills_dir", skillsDir, "tool_loop_max_iter", maxIter, "base_tools", baseTools)

	deps := &requestDeps{
		client:    client,
		runtime:   rt,
		registry:  toolRegistry,
		store:     skillsStore,
		rdb:       client.Redis(),
		baseTools: baseTools,
		maxIter:   maxIter,
		logger:    logger,
	}

	if err := client.ConsumeStream(ctx, stream, group, consumer, 1, 2*time.Second,
		func(ctx context.Context, msg *messaging.Message) error {
			handleRequest(ctx, deps, msg)
			return nil
		}, logger, nil); err != nil && err != context.Canceled {
		logger.Error("consume stream exited", "error", err)
	}
}

func handleRequest(ctx context.Context, d *requestDeps, msg *messaging.Message) {
	var payload messaging.ContextAssembledPayload
	if err := msg.DecodePayload(&payload); err != nil {
		d.logger.Error("failed to decode context assembled message", "error", err)
		return
	}

	correlationID := msg.CorrelationID
	d.logger.Info("message_received", "correlation_id", correlationID, "session_id", payload.SessionID)

	messages := injectSkillManifest(payload.Messages, d.store)

	// Resolve active skill for this session. Stale entries (skill removed
	// from disk since the record was written) are cleared silently.
	activeName, activeSkill := d.resolveActiveSkill(ctx, correlationID, payload.SessionID)
	d.warnUnknownAllowedTools(correlationID, activeName, activeSkill)

	tokenChannel := fmt.Sprintf(messaging.ChannelTokens, payload.SessionID)
	toolCallChannel := fmt.Sprintf(messaging.ChannelToolCalls, payload.SessionID)

	onToken := func(token string) {
		tokenMsg, err := messaging.NewMessage(messaging.TypeToken, "main-agent", messaging.TokenPayload{
			SessionID: payload.SessionID,
			Token:     token,
		})
		if err == nil {
			_ = d.client.PubSubPublish(ctx, tokenChannel, tokenMsg)
		}
	}

	onToolCall := func(call messaging.ToolCall) {
		tcMsg, err := messaging.NewMessage(messaging.TypeToolCall, "main-agent", messaging.ToolCallPayload{
			SessionID: payload.SessionID,
			Call:      call,
		})
		if err == nil {
			_ = d.client.PubSubPublish(ctx, toolCallChannel, tcMsg)
		}
	}

	publishToolResult := func(callID, output string) {
		trMsg, err := messaging.NewMessage(messaging.TypeToolResult, "main-agent", messaging.ToolResultPayload{
			SessionID: payload.SessionID,
			CallID:    callID,
			Output:    output,
		})
		if err == nil {
			_ = d.client.PubSubPublish(ctx, toolCallChannel, trMsg)
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
	for ; iter < d.maxIter; iter++ {
		toolSchemas := d.registry.SchemasFor(d.baseTools, allowedOf(activeSkill))
		visible := visibleSet(d.registry, d.baseTools, activeSkill)
		d.logger.Info("turn_iteration_start",
			"correlation_id", correlationID,
			"iter", iter,
			"tool_schema_count", len(toolSchemas),
			"visible_set_size", len(visible),
			"active_skill", activeName,
			"message_count", len(messages),
		)

		slotID, err := d.runtime.RequestSlotWithCorrelation(ctx, correlationID)
		if err != nil {
			d.logger.Error("failed to get slot", "correlation_id", correlationID, "iter", iter, "error", err)
			loopErr = err
			break
		}

		execStart := time.Now()
		content, toolCalls, err := d.runtime.ExecuteWithCorrelation(ctx, correlationID, messages, toolSchemas, onToken, onToolCall)
		execElapsed := time.Since(execStart).Milliseconds()

		_ = d.runtime.ReleaseSlotWithCorrelation(ctx, correlationID)

		switch {
		case err == nil:
			d.logger.Info("execute_done", "correlation_id", correlationID, "slot", slotID, "iter", iter, "elapsed_ms", execElapsed, "outcome", "ok")
		case err == messaging.ErrPreempted:
			d.logger.Info("execute_done", "correlation_id", correlationID, "slot", slotID, "iter", iter, "elapsed_ms", execElapsed, "outcome", "preempted")
			preempted = true
		default:
			d.logger.Error("execute_done", "correlation_id", correlationID, "slot", slotID, "iter", iter, "elapsed_ms", execElapsed, "outcome", "error", "error", err)
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
			output, elapsedMS, resultBytes, outcome := d.invokeOrGate(ctx, call, visible, activeName, payload.SessionID)

			d.logger.Info("tool_invoked",
				"correlation_id", correlationID,
				"tool_name", call.Function.Name,
				"args_bytes", len(call.Function.Arguments),
				"elapsed_ms", elapsedMS,
				"outcome", outcome,
				"result_bytes", resultBytes,
				"iter", iter,
				"total_elapsed_ms", time.Since(invStart).Milliseconds(),
				"active_skill", activeName,
			)
			messages = append(messages, messaging.ChatMsg{
				Role:       "tool",
				Content:    output,
				ToolCallID: call.ID,
			})
			allToolCalls = append(allToolCalls, call)
			allToolResults = append(allToolResults, messaging.ToolResult{CallID: call.ID, Output: output})
			publishToolResult(call.ID, output)

			// Side-effect activation: successful read_skill with a known name
			// updates the session's active skill for the NEXT iteration.
			if call.Function.Name == "read_skill" && outcome == "ok" {
				if newName, ok := parseReadSkillName(call.Function.Arguments); ok {
					if _, exists := d.store.Get(newName); exists && newName != activeName {
						d.logger.Info("active_skill_changed",
							"correlation_id", correlationID,
							"session_id", payload.SessionID,
							"active_skill", newName,
							"previous_skill", activeName,
						)
						if err := sessionskill.Set(ctx, d.rdb, payload.SessionID, newName, activeSkillTTL); err != nil {
							d.logger.Error("session_active_skill_write", "session_id", payload.SessionID, "error", err)
						}
						activeName = newName
						activeSkill, _ = d.store.Get(newName)
						d.warnUnknownAllowedTools(correlationID, activeName, activeSkill)
					}
				}
			}
		}
	}

	if iter == d.maxIter {
		d.logger.Warn("tool_loop_max_iter_hit", "correlation_id", correlationID, "iterations", iter)
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
		_ = d.client.PubSubPublish(ctx, tokenChannel, doneMsg)
	}

	d.logger.Info("turn_complete",
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
			_, _ = d.client.Publish(ctx, payload.ReplyStream, reply)
		}
	}
}

// resolveActiveSkill returns the active skill name and manifest for the
// session. If the stored name no longer matches a loaded skill, the stale
// Valkey record is cleared and a transition is logged.
func (d *requestDeps) resolveActiveSkill(ctx context.Context, correlationID, sessionID string) (string, *skills.Manifest) {
	name, err := sessionskill.Get(ctx, d.rdb, sessionID)
	if err != nil {
		d.logger.Error("session_active_skill_read", "session_id", sessionID, "error", err)
		return "", nil
	}
	if name == "" {
		return "", nil
	}
	if m, ok := d.store.Get(name); ok {
		return name, m
	}
	// Stale entry — skill was removed from disk. Clear and log the transition.
	d.logger.Info("active_skill_changed",
		"correlation_id", correlationID,
		"session_id", sessionID,
		"active_skill", "",
		"previous_skill", name,
		"reason", "skill_missing_from_store",
	)
	if err := sessionskill.Set(ctx, d.rdb, sessionID, "", 0); err != nil {
		d.logger.Error("session_active_skill_clear", "session_id", sessionID, "error", err)
	}
	return "", nil
}

// warnUnknownAllowedTools emits one WARN per unknown entry in the active
// skill's allowed-tools. Called at turn start and on activation change.
func (d *requestDeps) warnUnknownAllowedTools(correlationID, activeName string, active *skills.Manifest) {
	if active == nil {
		return
	}
	for _, name := range active.AllowedTools {
		if !d.registry.Has(name) {
			d.logger.Warn("skill_allowed_tool_unknown",
				"correlation_id", correlationID,
				"active_skill", activeName,
				"unknown_tool", name,
			)
		}
	}
}

// invokeOrGate either invokes the tool or emits a gated error envelope.
// Returns (output, elapsed_ms, result_bytes, outcome).
func (d *requestDeps) invokeOrGate(ctx context.Context, call messaging.ToolCall, visible map[string]struct{}, activeName, sessionID string) (string, int64, int, string) {
	if _, ok := visible[call.Function.Name]; !ok {
		envelope := fmt.Sprintf(`{"error":"tool not available under active skill: %s"}`, call.Function.Name)
		return envelope, 0, len(envelope), "gated"
	}
	args := call.Function.Arguments
	if _, scoped := sessionScopedTools[call.Function.Name]; scoped {
		args = injectSessionID(args, sessionID)
	}
	res, _ := d.registry.Invoke(ctx, call.Function.Name, args)
	return res.Output, res.ElapsedMS, res.ResultSize, res.Outcome
}

// injectSessionID rewrites the JSON object to set session_id = sessionID,
// overriding any model-supplied value. A malformed argsJSON is returned
// unchanged; argument validation is the tool's responsibility.
func injectSessionID(argsJSON, sessionID string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil || m == nil {
		return argsJSON
	}
	m["session_id"] = sessionID
	b, err := json.Marshal(m)
	if err != nil {
		return argsJSON
	}
	return string(b)
}

// visibleSet computes base ∪ activeSkill.allowed-tools, restricted to names
// actually registered. Used for invoke-time gating; mirrors what SchemasFor
// emits for the LLM.
func visibleSet(registry *tools.Registry, base []string, active *skills.Manifest) map[string]struct{} {
	v := make(map[string]struct{}, len(base)+len(allowedOf(active)))
	for _, n := range base {
		if registry.Has(n) {
			v[n] = struct{}{}
		}
	}
	for _, n := range allowedOf(active) {
		if registry.Has(n) {
			v[n] = struct{}{}
		}
	}
	return v
}

func allowedOf(m *skills.Manifest) []string {
	if m == nil {
		return nil
	}
	return m.AllowedTools
}

// parseReadSkillName extracts the "name" field from read_skill's arguments.
// Returns ("", false) when the JSON is malformed or the field is missing/empty.
func parseReadSkillName(argsJSON string) (string, bool) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", false
	}
	name := strings.TrimSpace(args.Name)
	return name, name != ""
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
