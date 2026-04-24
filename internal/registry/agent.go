package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"microagent2/internal/messaging"
)

type AgentInfo struct {
	AgentID            string
	Priority           int
	Preemptible        bool
	Capabilities       []string
	Trigger            string
	HeartbeatIntervalMS int
	LastHeartbeat      time.Time
	Alive              bool
}

type AgentRegistrar struct {
	client  *messaging.Client
	agentID string
	info    messaging.RegisterPayload
}

func NewAgentRegistrar(client *messaging.Client, info messaging.RegisterPayload) *AgentRegistrar {
	return &AgentRegistrar{
		client:  client,
		agentID: info.AgentID,
		info:    info,
	}
}

func (r *AgentRegistrar) Register(ctx context.Context) error {
	msg, err := messaging.NewMessage(messaging.TypeRegister, r.agentID, r.info)
	if err != nil {
		return err
	}
	if _, err := r.client.Publish(ctx, messaging.StreamRegistryAnnounce, msg); err != nil {
		return err
	}
	data, err := json.Marshal(r.info)
	if err != nil {
		return err
	}
	return r.client.Redis().Set(ctx, agentKey(r.agentID), data, 0).Err()
}

func (r *AgentRegistrar) Deregister(ctx context.Context) error {
	payload := messaging.DeregisterPayload{AgentID: r.agentID}
	msg, err := messaging.NewMessage(messaging.TypeDeregister, r.agentID, payload)
	if err != nil {
		return err
	}
	if _, err := r.client.Publish(ctx, messaging.StreamRegistryAnnounce, msg); err != nil {
		return err
	}
	return r.client.Redis().Del(ctx, agentKey(r.agentID)).Err()
}

func (r *AgentRegistrar) RunHeartbeat(ctx context.Context) {
	interval := time.Duration(r.info.HeartbeatIntervalMS) * time.Millisecond
	channel := fmt.Sprintf(messaging.ChannelHeartbeat, r.agentID)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			msg, err := messaging.NewMessage(messaging.TypeHeartbeat, r.agentID, nil)
			if err != nil {
				continue
			}
			_ = r.client.PubSubPublish(ctx, channel, msg)
		}
	}
}

func agentKey(agentID string) string {
	return fmt.Sprintf("agent:%s", agentID)
}

func ListRegistered(ctx context.Context, rdb interface {
	Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd
	Get(ctx context.Context, key string) *redis.StringCmd
}) ([]messaging.RegisterPayload, error) {
	var agents []messaging.RegisterPayload
	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, "agent:*", 100).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			data, err := rdb.Get(ctx, key).Bytes()
			if err != nil {
				continue
			}
			var info messaging.RegisterPayload
			if err := json.Unmarshal(data, &info); err != nil {
				continue
			}
			agents = append(agents, info)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return agents, nil
}

type Registry struct {
	mu     sync.RWMutex
	agents map[string]*AgentInfo
}

func NewRegistry() *Registry {
	return &Registry{
		agents: make(map[string]*AgentInfo),
	}
}

func (r *Registry) Register(info *AgentInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info.Alive = true
	info.LastHeartbeat = time.Now()
	r.agents[info.AgentID] = info
}

func (r *Registry) Deregister(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, agentID)
}

func (r *Registry) Get(agentID string) (*AgentInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.agents[agentID]
	return info, ok
}

func (r *Registry) MarkHeartbeat(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.agents[agentID]; ok {
		info.LastHeartbeat = time.Now()
		info.Alive = true
	}
}

func (r *Registry) MarkDead(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.agents[agentID]; ok {
		info.Alive = false
	}
}

func (r *Registry) ListAlive() []*AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*AgentInfo
	for _, info := range r.agents {
		if info.Alive {
			result = append(result, info)
		}
	}
	return result
}

func (r *Registry) ListPreemptible() []*AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*AgentInfo
	for _, info := range r.agents {
		if info.Alive && info.Preemptible {
			result = append(result, info)
		}
	}
	return result
}

func (r *Registry) ListDead() []*AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*AgentInfo
	for _, info := range r.agents {
		if !info.Alive {
			result = append(result, info)
		}
	}
	return result
}
