package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"microagent2/internal/config"
	"microagent2/internal/tools"
)

const (
	healthKey        = "health:main-agent:mcp"
	heartbeatCadence = 30 * time.Second
)

// HealthEntry is the shape written to health:main-agent:mcp and exposed via
// /v1/status's mcp_servers field.
type HealthEntry struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	ToolCount int    `json:"tool_count"`
	LastError string `json:"last_error,omitempty"`
}

type Manager struct {
	rdb    *redis.Client
	logger *slog.Logger

	mu      sync.Mutex
	clients map[string]*Client
	health  map[string]*HealthEntry
	order   []string

	cancelHeartbeat context.CancelFunc
}

func NewManager(rdb *redis.Client, logger *slog.Logger) *Manager {
	return &Manager{
		rdb:     rdb,
		logger:  logger,
		clients: map[string]*Client{},
		health:  map[string]*HealthEntry{},
	}
}

func invokeTimeoutFromEnv() time.Duration {
	if v := os.Getenv("MCP_INVOKE_TIMEOUT_S"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 30 * time.Second
}

// Start spawns each enabled server in parallel, registers discovered tools,
// and begins the health heartbeat. Safe to call with an empty server list.
func (m *Manager) Start(ctx context.Context, servers []config.MCPServerConfig, registry *tools.Registry) {
	invokeTimeout := invokeTimeoutFromEnv()

	for _, s := range servers {
		m.mu.Lock()
		m.health[s.Name] = &HealthEntry{Name: s.Name, Enabled: s.Enabled}
		m.order = append(m.order, s.Name)
		m.mu.Unlock()
	}

	var wg sync.WaitGroup
	for _, s := range servers {
		if !s.Enabled {
			continue
		}
		wg.Add(1)
		go func(cfg config.MCPServerConfig) {
			defer wg.Done()
			m.startOne(ctx, cfg, registry, invokeTimeout)
		}(s)
	}
	wg.Wait()

	m.publishHealth(ctx)

	hbCtx, cancel := context.WithCancel(ctx)
	m.cancelHeartbeat = cancel
	go m.heartbeat(hbCtx)
}

func (m *Manager) startOne(ctx context.Context, cfg config.MCPServerConfig, registry *tools.Registry, invokeTimeout time.Duration) {
	m.logger.Info("mcp_server_spawned", "server", cfg.Name, "command", cfg.Command)

	client, err := NewClient(ctx, cfg.Name, cfg.Command, cfg.Args, cfg.Env, m.logger, invokeTimeout)
	if err != nil {
		m.setFailure(cfg.Name, "spawn", err)
		return
	}

	initCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := client.Initialize(initCtx); err != nil {
		cancel()
		_ = client.Close()
		m.setFailure(cfg.Name, "initialize", err)
		return
	}
	cancel()

	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	mcpTools, err := client.ListTools(listCtx)
	cancel()
	if err != nil {
		_ = client.Close()
		m.setFailure(cfg.Name, "tools/list", err)
		return
	}

	toolCount := 0
	for _, t := range mcpTools {
		wrapped := NewTool(cfg.Name, t, client)
		if err := registry.RegisterMCP(wrapped); err != nil {
			m.logger.Warn("mcp_tool_register_failed", "server", cfg.Name, "tool", t.Name, "error", err.Error())
			continue
		}
		toolCount++
	}
	m.logger.Info("mcp_tools_registered", "server", cfg.Name, "tool_count", toolCount)

	m.mu.Lock()
	m.clients[cfg.Name] = client
	m.health[cfg.Name] = &HealthEntry{
		Name:      cfg.Name,
		Enabled:   true,
		Connected: true,
		ToolCount: toolCount,
	}
	m.mu.Unlock()
}

func (m *Manager) setFailure(name, phase string, err error) {
	m.logger.Error("mcp_server_startup_failed", "name", name, "phase", phase, "error", err.Error())
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.health[name]
	if !ok {
		entry = &HealthEntry{Name: name, Enabled: true}
		m.health[name] = entry
	}
	entry.Connected = false
	entry.LastError = phase + ": " + err.Error()
}

func (m *Manager) publishHealth(ctx context.Context) {
	m.mu.Lock()
	out := make([]*HealthEntry, 0, len(m.order))
	for _, name := range m.order {
		if e, ok := m.health[name]; ok {
			// Refresh Connected flag from client state.
			if c, hasClient := m.clients[name]; hasClient {
				e.Connected = !c.Disconnected()
			}
			out = append(out, e)
		}
	}
	m.mu.Unlock()

	data, err := json.Marshal(out)
	if err != nil {
		m.logger.Error("mcp_health_marshal_failed", "error", err.Error())
		return
	}
	if err := m.rdb.Set(ctx, healthKey, data, 0).Err(); err != nil {
		m.logger.Warn("mcp_health_write_failed", "error", err.Error())
	}
}

func (m *Manager) heartbeat(ctx context.Context) {
	t := time.NewTicker(heartbeatCadence)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.publishHealth(ctx)
		}
	}
}

// Close tears down all subprocesses.
func (m *Manager) Close(ctx context.Context) {
	if m.cancelHeartbeat != nil {
		m.cancelHeartbeat()
	}
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(cli *Client) {
			defer wg.Done()
			_ = cli.Close()
		}(c)
	}
	wg.Wait()
}
